/*
Copyright (c) 2017 SAP SE or an SAP affiliate company. All rights reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package controller is used to provide the core functionalities of machine-controller-manager
package controller

import (
	"errors"
	"fmt"
	"strings"
	"time"

	machineapi "github.com/gardener/machine-controller-manager/pkg/apis/machine"
	"github.com/gardener/machine-controller-manager/pkg/apis/machine/v1alpha1"
	"github.com/gardener/machine-controller-manager/pkg/apis/machine/validation"
	"github.com/gardener/machine-controller-manager/pkg/cmiclient"
	"github.com/golang/glog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/cache"
)

const (
	// MachinePriority is the annotation used to specify priority
	// associated with a machine while deleting it. The less its
	// priority the more likely it is to be deleted first
	// Default priority for a machine is set to 3
	MachinePriority = "machinepriority.machine.sapcloud.io"
)

/*
	SECTION
	Machine controller - Machine add, update, delete watches
*/
func (c *controller) addMachine(obj interface{}) {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		glog.Errorf("Couldn't get key for object %+v: %v", obj, err)
		return
	}
	glog.V(4).Infof("Add/Update/Delete machine object %q", key)
	c.machineQueue.Add(key)
}

func (c *controller) updateMachine(oldObj, newObj interface{}) {
	glog.V(4).Info("Updating machine object")
	c.addMachine(newObj)
}

func (c *controller) deleteMachine(obj interface{}) {
	glog.V(4).Info("Deleting machine object")
	c.addMachine(obj)
}

func (c *controller) enqueueMachine(obj interface{}) {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		return
	}
	c.machineQueue.Add(key)
}

func (c *controller) enqueueMachineAfter(obj interface{}, after time.Duration) {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		return
	}
	c.machineQueue.AddAfter(key, after)
}

func (c *controller) reconcileClusterMachineKey(key string) error {
	glog.Info(key)
	_, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return err
	}

	machine, err := c.machineLister.Machines(c.namespace).Get(name)
	if apierrors.IsNotFound(err) {
		glog.V(4).Infof("Machine %q: Not doing work because it is not found", key)
		return nil
	}
	if err != nil {
		glog.Errorf("Machine %q: Unable to retrieve object from store: %v", key, err)
		return err
	}

	durationToNextSync := 10 * time.Minute
	retryStatus, err := c.reconcileClusterMachine(machine)
	glog.Info(retryStatus, err)
	if err != nil {
		if retryStatus == RetryOp {
			durationToNextSync = 15 * time.Second
		}
	}

	c.enqueueMachineAfter(machine, durationToNextSync)

	return nil
}

func (c *controller) reconcileClusterMachine(machine *v1alpha1.Machine) (bool, error) {
	glog.V(3).Infof("Start Reconciling machine %q", machine.Name)
	defer glog.V(3).Infof("Stop Reconciling machine %q", machine.Name)

	if c.safetyOptions.MachineControllerFrozen && machine.DeletionTimestamp == nil {
		// If Machine controller is frozen and
		// machine is not set for termination don't process it
		err := fmt.Errorf("Machine controller has frozen. Retrying reconcile after resync period")
		glog.Error(err)
		return DoNotRetryOp, err
	}

	internalMachine := &machineapi.Machine{}
	if err := c.internalExternalScheme.Convert(machine, internalMachine, nil); err != nil {
		glog.Error(err)
		return DoNotRetryOp, err
	}

	validationerr := validation.ValidateMachine(internalMachine)
	if validationerr.ToAggregate() != nil && len(validationerr.ToAggregate().Errors()) > 0 {
		err := fmt.Errorf("Validation of Machine failed %s", validationerr.ToAggregate().Error())
		glog.Error(err)
		return DoNotRetryOp, err
	}

	MachineClass, secretRef, err := c.ValidateMachineClass(&machine.Spec.Class)
	if err != nil {
		glog.Error(err)
		return DoNotRetryOp, err
	}

	lastKnownState := machine.Status.LastKnownState
	if lastKnownState == "" {
		lastKnownState = machine.Annotations["lastKnownState"]
	}

	driver, err := cmiclient.NewCMIPluginClient(
		machine.Spec.ProviderID,
		MachineClass.(*v1alpha1.MachineClass).Provider,
		secretRef,
		MachineClass,
		machine.Name,
		lastKnownState,
	)
	if err != nil {
		glog.Errorf("Error while creating CMIPluginClient: %s", err)
		return RetryOp, err
	}

	/*
		NOT NEEDED?
		else if actualProviderID == "fake" {
			glog.Warning("Fake driver type")
			return false, nil
		}
		// Get the latest version of the machine so that we can avoid conflicts
		machine, err = c.controlMachineClient.Machines(machine.Namespace).Get(machine.Name, metav1.GetOptions{})
		if err != nil {
			glog.Errorf("Could GET machine object %s", err)
			return RetryOp, err
		}
	*/

	if machine.DeletionTimestamp != nil {
		// Process a delete event
		return c.triggerDeletionFlow(machine, driver)
	}

	if machine.Status.Node != "" {
		// If reference to node object exists execute the below

		retry, err := c.reconcileMachineHealth(machine)
		if err != nil {
			return retry, err
		}

		retry, err = c.syncMachineNodeTemplates(machine)
		if err != nil {
			return retry, err
		}
	}

	/*
		if machine.Status.CurrentStatus.Phase == v1alpha1.MachineFailed {
			// If machine status is failed, ignore it
			return DoNotRetryOp, nil
		} else
	*/

	if machine.Spec.ProviderID == "" || machine.Status.CurrentStatus.Phase == "" {
		return c.triggerCreationFlow(machine, driver)
	}

	/*
		TODO: re-introduce this when in-place updates can be done.
		else if actualProviderID != machine.Spec.ProviderID {
			// If provider-ID has changed, update the machine
			return c.triggerUpdationFlow(machine, actualProviderID)
		}
	*/

	return DoNotRetryOp, nil
}

/*
	SECTION
	Machine controller - nodeToMachine
*/
func (c *controller) addNodeToMachine(obj interface{}) {

	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		glog.Errorf("Couldn't get key for object %+v: %v", obj, err)
		return
	}

	machine, err := c.getMachineFromNode(key)
	if err != nil {
		glog.Errorf("Couldn't fetch machine %s, Error: %s", key, err)
		return
	} else if machine == nil {
		return
	}

	glog.V(4).Infof("Add machine object backing node %q", machine.Name)
	c.enqueueMachine(machine)
}

func (c *controller) updateNodeToMachine(oldObj, newObj interface{}) {
	c.addNodeToMachine(newObj)
}

func (c *controller) deleteNodeToMachine(obj interface{}) {
	c.addNodeToMachine(obj)
}

/*
	SECTION
	NodeToMachine operations
*/

func (c *controller) getMachineFromNode(nodeName string) (*v1alpha1.Machine, error) {
	var (
		list     = []string{nodeName}
		selector = labels.NewSelector()
		req, _   = labels.NewRequirement("node", selection.Equals, list)
	)

	selector = selector.Add(*req)
	machines, _ := c.machineLister.List(selector)

	if len(machines) > 1 {
		return nil, errors.New("Multiple machines matching node")
	} else if len(machines) < 1 {
		return nil, nil
	}

	return machines[0], nil
}

/*
	Move to update method?
	clone := machine.DeepCopy()
	if clone.Labels == nil {
		clone.Labels = make(map[string]string)
	}

	if _, ok := clone.Labels["node"]; !ok {
		clone.Labels["node"] = machine.Status.Node
		machine, err = c.controlMachineClient.Machines(clone.Namespace).Update(clone)
		if err != nil {
			glog.Warningf("Machine update failed. Retrying, error: %s", err)
			return machine, err
		}
	}
*/

/*
	SECTION
	Machine operations - Create, Update, Delete
*/

func (c *controller) triggerCreationFlow(machine *v1alpha1.Machine, driver cmiclient.CMIClient) (bool, error) {
	var (
		providerID     string
		nodeName       string
		lastKnownState string
		err            error
	)

	// Add finalizers if not present
	retry, err := c.addMachineFinalizers(machine)
	if err != nil {
		return retry, err
	}

	//
	providerID, nodeName, lastKnownState, err = driver.GetMachineStatus()
	if err == nil {
		// Found VM with required machine name
		glog.V(2).Infof("Found VM with required machine name. Adopting existing machine: %q with ProviderID: %s", machine.Name, providerID)
	} else {
		// VM with required name is not found.

		grpcErr, ok := status.FromError(err)
		if !ok {
			// Error occurred with decoding gRPC error status, abort with retry.
			glog.Errorf("Error occurred while decoding gRPC error for machine %q: %s", machine.Name, err)
			return RetryOp, err
		}

		// Decoding gRPC error code
		switch grpcErr.Code() {
		case codes.NotFound, codes.Unimplemented:
			// Either VM is not found
			// or GetMachineStatus() call is not implemented
			// In this case, invoke a CreateMachine() call
			glog.V(2).Infof("Creating a VM for machine %q, please wait!", machine.Name)
			if _, present := machine.Labels["node"]; !present {
				// If node label is not present
				providerID, nodeName, lastKnownState, err = driver.CreateMachine()
				if err != nil {
					// Create call returned an error.
					glog.Errorf("Error while creating machine %s: %s", machine.Name, err.Error())
					return c.machineCreateErrorHandler(machine, lastKnownState, err)
				}
			} else {
				nodeName = machine.Labels["node"]
				lastKnownState = machine.Annotations["lastKnownState"]
			}

			// Creation was successful
			glog.V(2).Infof("Created new VM for machine: %q with ProviderID: %s", machine.Name, providerID)
			break

		case codes.Unknown, codes.DeadlineExceeded, codes.Aborted, codes.Unavailable:
			// GetMachineStatus() returned with one of the above error codes.
			// Retry operation.
			return RetryOp, err

		default:
			return DoNotRetryOp, err
		}

	}

	_, machineNodeLabelPresent := machine.Labels["node"]
	_, machinePriorityAnnotationPresent := machine.Annotations[MachinePriority]

	if !machineNodeLabelPresent || !machinePriorityAnnotationPresent || machine.Spec.ProviderID == "" {
		clone := machine.DeepCopy()
		if clone.Labels == nil {
			clone.Labels = make(map[string]string)
		}
		clone.Labels["node"] = nodeName
		if clone.Annotations == nil {
			clone.Annotations = make(map[string]string)
		}
		if clone.Annotations[MachinePriority] == "" {
			clone.Annotations[MachinePriority] = "3"
		}
		// TODO: To check if dependency can be eliminated
		clone.Annotations["lastKnownState"] = lastKnownState
		clone.Spec.ProviderID = providerID
		_, err = c.controlMachineClient.Machines(clone.Namespace).Update(clone)
		if err != nil {
			glog.Warningf("Machine UPDATE failed for %q. Retrying, error: %s", machine.Name, err)
		} else {
			glog.V(2).Infof("Machine labels/annotations UPDATE for %q", machine.Name)

			// Return error even when machine object is updated
			err = fmt.Errorf("Machine creation in process. Machine UPDATE successful")
		}
		return RetryOp, err
	}

	if machine.Status.Node == "" || machine.Status.CurrentStatus.Phase == "" {
		clone := machine.DeepCopy()

		clone.Status.Node = nodeName
		clone.Status.LastOperation = v1alpha1.LastOperation{
			Description:    "Creating machine on cloud provider",
			State:          v1alpha1.MachineStateProcessing,
			Type:           v1alpha1.MachineOperationCreate,
			LastUpdateTime: metav1.Now(),
		}
		clone.Status.CurrentStatus = v1alpha1.CurrentStatus{
			Phase:          v1alpha1.MachinePending,
			TimeoutActive:  true,
			LastUpdateTime: metav1.Now(),
		}
		clone.Status.LastKnownState = lastKnownState

		_, err = c.controlMachineClient.Machines(clone.Namespace).UpdateStatus(clone)
		if err != nil {
			glog.Warningf("Machine/status UPDATE failed for %q. Retrying, error: %s", machine.Name, err)
		} else {
			glog.V(2).Infof("Machine/status UPDATE for %q during creation", machine.Name)

			// Return error even when machine object is updated
			err = fmt.Errorf("Machine creation in process. Machine/Status UPDATE successful")
		}

		return RetryOp, err
	}

	return DoNotRetryOp, nil
}

func (c *controller) triggerUpdationFlow(machine *v1alpha1.Machine, actualProviderID string) (bool, error) {
	glog.V(2).Infof("Setting ProviderID of %s to %s", machine.Name, actualProviderID)

	for {
		machine, err := c.controlMachineClient.Machines(machine.Namespace).Get(machine.Name, metav1.GetOptions{})
		if err != nil {
			glog.Warningf("Machine GET failed. Retrying, error: %s", err)
			continue
		}

		clone := machine.DeepCopy()
		clone.Spec.ProviderID = actualProviderID
		machine, err = c.controlMachineClient.Machines(clone.Namespace).Update(clone)
		if err != nil {
			glog.Warningf("Machine UPDATE failed. Retrying, error: %s", err)
			continue
		}

		clone = machine.DeepCopy()
		lastOperation := v1alpha1.LastOperation{
			Description:    "Updated provider ID",
			State:          v1alpha1.MachineStateSuccessful,
			Type:           v1alpha1.MachineOperationUpdate,
			LastUpdateTime: metav1.Now(),
		}
		clone.Status.LastOperation = lastOperation
		_, err = c.controlMachineClient.Machines(clone.Namespace).UpdateStatus(clone)
		if err != nil {
			glog.Warningf("Machine/status UPDATE failed. Retrying, error: %s", err)
			continue
		}
		// Update went through, exit out of infinite loop
		break
	}

	return DoNotRetryOp, nil
}

func (c *controller) triggerDeletionFlow(machine *v1alpha1.Machine, driver cmiclient.CMIClient) (bool, error) {
	finalizers := sets.NewString(machine.Finalizers...)

	switch {
	case !finalizers.Has(DeleteFinalizerName):
		// If Finalizers are not present on machine
		err := fmt.Errorf("Machine %q is missing finalizers. Deletion cannot proceed", machine.Name)
		return DoNotRetryOp, err

	case machine.Status.CurrentStatus.Phase != v1alpha1.MachineTerminating:
		return c.setMachineTerminationStatus(machine, driver)

	case strings.Contains(machine.Status.LastOperation.Description, getVMStatus):
		return c.getVMStatus(machine, driver)

	case strings.Contains(machine.Status.LastOperation.Description, initiateDrain):
		return c.drainNode(machine, driver)

	case strings.Contains(machine.Status.LastOperation.Description, initiateVMDeletion):
		return c.deleteVM(machine, driver)

	case strings.Contains(machine.Status.LastOperation.Description, initiateNodeDeletion):
		return c.deleteNodeObject(machine)

	case strings.Contains(machine.Status.LastOperation.Description, initiateFinalizerRemoval):
		_, err := c.deleteMachineFinalizers(machine)
		if err != nil {
			// Keep retrying until update goes through
			glog.Errorf("Machine finalizer REMOVAL failed for machine %q. Retrying, error: %s", machine.Name, err)
			return RetryOp, err
		}

	default:
		err := fmt.Errorf("Unable to decode deletion flow state for machine %s", machine.Name)
		glog.Error(err)
		return DoNotRetryOp, err
	}

	/*
		// Delete machine object
		err := c.controlMachineClient.Machines(machine.Namespace).Delete(machine.Name, &metav1.DeleteOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			// If its an error, and anyother error than object not found
			glog.Errorf("Deletion of Machine Object %q failed due to error: %s", machine.Name, err)
			return RetryOp, err
		}
	*/

	glog.V(2).Infof("Machine %q deleted successfully", machine.Name)
	return DoNotRetryOp, nil
}
