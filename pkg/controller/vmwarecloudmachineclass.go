/*
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
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/cache"

	"github.com/golang/glog"

	"github.com/gardener/machine-controller-manager/pkg/apis/machine"
	"github.com/gardener/machine-controller-manager/pkg/apis/machine/v1alpha1"
	"github.com/gardener/machine-controller-manager/pkg/apis/machine/validation"
)

// VMwareMachineClassKind is used to identify the machineClassKind as VMware
const VMwareMachineClassKind = "VMwareMachineClass"

func (c *controller) machineDeploymentToVMwareMachineClassDelete(obj interface{}) {
	machineDeployment, ok := obj.(*v1alpha1.MachineDeployment)
	if machineDeployment == nil || !ok {
		return
	}
	if machineDeployment.Spec.Template.Spec.Class.Kind == VMwareMachineClassKind {
		c.vmwareMachineClassQueue.Add(machineDeployment.Spec.Template.Spec.Class.Name)
	}
}

func (c *controller) machineSetToVMwareMachineClassDelete(obj interface{}) {
	machineSet, ok := obj.(*v1alpha1.MachineSet)
	if machineSet == nil || !ok {
		return
	}
	if machineSet.Spec.Template.Spec.Class.Kind == VMwareMachineClassKind {
		c.vmwareMachineClassQueue.Add(machineSet.Spec.Template.Spec.Class.Name)
	}
}

func (c *controller) machineToVMwareMachineClassDelete(obj interface{}) {
	machine, ok := obj.(*v1alpha1.Machine)
	if machine == nil || !ok {
		return
	}
	if machine.Spec.Class.Kind == VMwareMachineClassKind {
		c.vmwareMachineClassQueue.Add(machine.Spec.Class.Name)
	}
}

func (c *controller) vmwareMachineClassAdd(obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		glog.Errorf("Couldn't get key for object %+v: %v", obj, err)
		return
	}
	c.vmwareMachineClassQueue.Add(key)
}

func (c *controller) vmwareMachineClassUpdate(oldObj, newObj interface{}) {
	old, ok := oldObj.(*v1alpha1.VMwareMachineClass)
	if old == nil || !ok {
		return
	}
	new, ok := newObj.(*v1alpha1.VMwareMachineClass)
	if new == nil || !ok {
		return
	}

	c.vmwareMachineClassAdd(newObj)
}

// reconcileClusterVMwareMachineClassKey reconciles a VMwareMachineClass due to controller resync
// or an event on the vmwareMachineClass.
func (c *controller) reconcileClusterVmwareMachineClassKey(key string) error {
	_, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return err
	}

	class, err := c.vmwareMachineClassLister.VMwareMachineClasses(c.namespace).Get(name)
	if errors.IsNotFound(err) {
		glog.Infof("%s %q: Not doing work because it has been deleted", VMwareMachineClassKind, key)
		return nil
	}
	if err != nil {
		glog.Infof("%s %q: Unable to retrieve object from store: %v", VMwareMachineClassKind, key, err)
		return err
	}

	return c.reconcileClusterVMwareMachineClass(class)
}

func (c *controller) reconcileClusterVMwareMachineClass(class *v1alpha1.VMwareMachineClass) error {
	glog.V(4).Info("Start Reconciling vmwaremachineclass: ", class.Name)
	defer func() {
		c.enqueueVMwareMachineClassAfter(class, 10*time.Minute)
		glog.V(4).Info("Stop Reconciling vmwaremachineclass: ", class.Name)
	}()

	internalClass := &machine.VMwareMachineClass{}
	err := c.internalExternalScheme.Convert(class, internalClass, nil)
	if err != nil {
		return err
	}
	// TODO this should be put in own API server
	validationerr := validation.ValidateVMwareMachineClass(internalClass)
	if validationerr.ToAggregate() != nil && len(validationerr.ToAggregate().Errors()) > 0 {
		glog.V(2).Infof("Validation of %s failed %s", VMwareMachineClassKind, validationerr.ToAggregate().Error())
		return nil
	}

	// Manipulate finalizers
	if class.DeletionTimestamp == nil {
		err = c.addVMwareMachineClassFinalizers(class)
		if err != nil {
			return err
		}
	}

	machines, err := c.findMachinesForClass(VMwareMachineClassKind, class.Name)
	if err != nil {
		return err
	}

	if class.DeletionTimestamp != nil {
		if finalizers := sets.NewString(class.Finalizers...); !finalizers.Has(DeleteFinalizerName) {
			return nil
		}

		machineDeployments, err := c.findMachineDeploymentsForClass(VMwareMachineClassKind, class.Name)
		if err != nil {
			return err
		}
		machineSets, err := c.findMachineSetsForClass(VMwareMachineClassKind, class.Name)
		if err != nil {
			return err
		}
		if len(machineDeployments) == 0 && len(machineSets) == 0 && len(machines) == 0 {
			return c.deleteVMwareMachineClassFinalizers(class)
		}

		glog.V(3).Infof("Cannot remove finalizer of %s because still Machine[s|Sets|Deployments] are referencing it", class.Name)
		return nil
	}

	for _, machine := range machines {
		c.addMachine(machine)
	}
	return nil
}

/*
	SECTION
	Manipulate Finalizers
*/

func (c *controller) addVMwareMachineClassFinalizers(class *v1alpha1.VMwareMachineClass) error {
	clone := class.DeepCopy()

	if finalizers := sets.NewString(clone.Finalizers...); !finalizers.Has(DeleteFinalizerName) {
		finalizers.Insert(DeleteFinalizerName)
		return c.updateVMwareMachineClassFinalizers(clone, finalizers.List())
	}
	return nil
}

func (c *controller) deleteVMwareMachineClassFinalizers(class *v1alpha1.VMwareMachineClass) error {
	clone := class.DeepCopy()

	if finalizers := sets.NewString(clone.Finalizers...); finalizers.Has(DeleteFinalizerName) {
		finalizers.Delete(DeleteFinalizerName)
		return c.updateVMwareMachineClassFinalizers(clone, finalizers.List())
	}
	return nil
}

func (c *controller) updateVMwareMachineClassFinalizers(class *v1alpha1.VMwareMachineClass, finalizers []string) error {
	// Get the latest version of the class so that we can avoid conflicts
	class, err := c.controlMachineClient.VMwareMachineClasses(class.Namespace).Get(class.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}

	clone := class.DeepCopy()
	clone.Finalizers = finalizers
	_, err = c.controlMachineClient.VMwareMachineClasses(class.Namespace).Update(clone)
	if err != nil {
		glog.Warning("Updating VMwareMachineClasses failed, retrying. ", class.Name, err)
		return err
	}
	glog.V(3).Infof("Successfully added/removed finalizer on the vmwaremachineclass %q", class.Name)
	return err
}

func (c *controller) enqueueVMwareMachineClassAfter(obj interface{}, after time.Duration) {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		return
	}
	c.vmwareMachineClassQueue.AddAfter(key, after)
}
