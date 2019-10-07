/*
Copyright (c) 2019 SAP SE or an SAP affiliate company. All rights reserved.

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

// Package driver contains the cloud provider specific implementations to manage machines
package driver

import (
	"context"
	"fmt"
	"github.com/pkg/errors"
	"net/url"
	"strings"

	"github.com/gardener/machine-controller-manager/pkg/apis/machine/v1alpha1"
	"github.com/gardener/machine-controller-manager/pkg/driver/vmware"
	"github.com/golang/glog"
	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"
	corev1 "k8s.io/api/core/v1"
)

// VMwareDriver is the driver struct for holding VMware machine information
type VMwareDriver struct {
	VMwareMachineClass   *v1alpha1.VMwareMachineClass
	CloudConfig          *corev1.Secret
	UserData             string
	MachineID            string
	MachineName          string
	MachineInventoryPath string
}

func (d *VMwareDriver) createVMwareClient(ctx context.Context) (*govmomi.Client, error) {
	client, err := d.doCreateVMwareClient(ctx)
	if err != nil {
		glog.Errorf("Could not create VMware client: %s", err)
		return nil, errors.Wrap(err, "create VMware client failed")
	}
	return client, nil
}

func (d *VMwareDriver) doCreateVMwareClient(ctx context.Context) (*govmomi.Client, error) {
	host, ok := d.CloudConfig.Data[v1alpha1.VMwareHost]
	if !ok {
		return nil, fmt.Errorf("missing %s in secret", v1alpha1.VMwareHost)
	}
	username, ok := d.CloudConfig.Data[v1alpha1.VMwareUsername]
	if !ok {
		return nil, fmt.Errorf("missing %s in secret", v1alpha1.VMwareUsername)
	}
	password, ok := d.CloudConfig.Data[v1alpha1.VMwarePassword]
	if !ok {
		return nil, fmt.Errorf("missing %s in secret", v1alpha1.VMwarePassword)
	}
	insecure, ok := d.CloudConfig.Data[v1alpha1.VMwareInsecure]
	if !ok {
		return nil, fmt.Errorf("missing %s in secret", v1alpha1.VMwareInsecure)
	}

	clientUrl, err := url.Parse("https://" + string(host) + "/sdk")
	if err != nil {
		return nil, err
	}

	clientUrl.User = url.UserPassword(string(username), string(password))

	// Connect and log in to ESX or vCenter
	return govmomi.NewClient(ctx, clientUrl, string(insecure) == "true")
}

// Create method is used to create a VMware machine
func (d *VMwareDriver) Create() (string, string, error) {
	if d.MachineID != "" {
		glog.Warning("create: expected MachineID to be empty")
		d.MachineID = ""
	}

	ctx := context.TODO()
	client, err := d.createVMwareClient(ctx)
	if err != nil {
		return "", "", err
	}
	defer client.Logout(ctx)

	spec := &d.VMwareMachineClass.Spec
	cmd := vmware.NewClone(d.MachineName, spec, d.UserData)
	err = cmd.Run(ctx, client)
	if err != nil {
		return "", "", err
	}
	vm := cmd.Clone
	d.MachineID = vm.UUID(ctx)

	glog.V(4).Infof("[DEBUG] %s: Create complete, id=%s", d.MachineName, d.MachineID)

	return d.MachineID, d.MachineName, nil
}

// Delete method is used to delete a VMware machine
func (d *VMwareDriver) Delete() error {
	if d.MachineID == "" {
		return fmt.Errorf("missing MachineID")
	}

	ctx := context.TODO()
	client, err := d.createVMwareClient(ctx)
	if err != nil {
		return err
	}
	defer client.Logout(ctx)

	spec := &d.VMwareMachineClass.Spec
	return vmware.Delete(ctx, client, spec, d.MachineID)
}

// GetExisting method is used to get machineID for existing VMware machine
func (d *VMwareDriver) GetExisting() (string, error) {
	return d.MachineID, nil
}

// GetVMs returns a machine matching the machineID
// If machineID is an empty string then it returns all matching instances
func (d *VMwareDriver) GetVMs(machineID string) (VMs, error) {
	ctx := context.TODO()
	client, err := d.createVMwareClient(ctx)
	if err != nil {
		return nil, err
	}
	defer client.Logout(ctx)

	listOfVMs := make(map[string]string)
	spec := &d.VMwareMachineClass.Spec
	if machineID == "" {
		clusterName := ""
		nodeRole := ""

		for key := range d.VMwareMachineClass.Spec.Tags {
			if strings.HasPrefix(key, "kubernetes.io/cluster/") {
				clusterName = key
			} else if strings.HasPrefix(key, "kubernetes.io/role/") {
				nodeRole = key
			}
		}

		if clusterName == "" || nodeRole == "" {
			return listOfVMs, nil
		}

		visitor := func(vm *object.VirtualMachine, obj mo.ManagedEntity, field object.CustomFieldDefList) error {
			matchedCluster := false
			matchedRole := false
			for _, cv := range obj.CustomValue {
				sv := cv.(*types.CustomFieldStringValue)
				switch field.ByKey(sv.Key).Name {
				case clusterName:
					matchedCluster = true
				case nodeRole:
					matchedRole = true
				}
			}
			if matchedCluster && matchedRole {
				listOfVMs[vm.UUID(ctx)] = obj.Name
			}
			return nil
		}

		err := vmware.VisitVirtualMachines(ctx, client, spec, visitor)
		if err != nil {
			glog.Errorf("could not visit virtual machines for datacenter '%s': %v", spec.Datacenter, err)
			return nil, errors.Wrap(err, "VisitVirtualMachines failed")
		}
	} else {
		vm, err := vmware.Find(ctx, client, spec, machineID)
		if err != nil {
			return nil, errors.Wrapf(err, "Find machineID %s failed", machineID)
		}
		listOfVMs[machineID] = vm.Name()
	}
	return listOfVMs, nil
}

// GetVolNames parses volume names from pv specs
func (d *VMwareDriver) GetVolNames(specs []corev1.PersistentVolumeSpec) ([]string, error) {
	names := []string{}
	for i := range specs {
		spec := &specs[i]
		if spec.VsphereVolume == nil {
			// Not a vsphere volume
			continue
		}
		name := spec.VsphereVolume.VolumePath
		names = append(names, name)
	}
	return names, nil
}
