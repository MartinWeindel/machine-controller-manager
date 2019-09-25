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
	"net/url"

	"github.com/gardener/machine-controller-manager/pkg/apis/machine/v1alpha1"
	"github.com/gardener/machine-controller-manager/pkg/driver/vmware"
	"github.com/golang/glog"
	"github.com/vmware/govmomi"
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
	ctx := context.TODO()
	client, err := d.createVMwareClient(ctx)
	if err != nil {
		glog.Errorf("Could not create VMware client: %s", err)
		return "", "", err
	}
	defer client.Logout(ctx)

	spec := &d.VMwareMachineClass.Spec
	cmd, err := vmware.NewClone(d.MachineName)
	if err != nil {
		return "", "", err
	}

	err = cmd.Run(ctx, client, spec)
	if err != nil {
		return "", "", err
	}
	vm := cmd.Clone
	d.MachineID = vm.UUID(ctx)

	// All done!
	glog.V(4).Infof("[DEBUG] %s: Create complete, id=%s", d.MachineName, d.MachineID)

	return d.MachineID, d.MachineName, nil
}

// Delete method is used to delete a VMware machine
func (d *VMwareDriver) Delete() error {
	// TODO Delete
	return nil
	/*
		svc := d.createSVC()
		if svc == nil {
			return fmt.Errorf("nil VMware service returned")
		}
		machineID := d.decodeMachineID(d.MachineID)
		resp, err := svc.Devices.Delete(machineID)
		if err != nil {
			if resp.StatusCode == 404 {
				glog.V(2).Infof("No machine matching the machine-ID found on the provider %q", d.MachineID)
				return nil
			}
			glog.Errorf("Could not terminate machine %s: %v", d.MachineID, err)
			return err
		}
		return nil
	*/
}

// GetExisting method is used to get machineID for existing VMware machine
func (d *VMwareDriver) GetExisting() (string, error) {
	return d.MachineID, nil
}

// GetVMs returns a machine matching the machineID
// If machineID is an empty string then it returns all matching instances
func (d *VMwareDriver) GetVMs(machineID string) (VMs, error) {
	listOfVMs := make(map[string]string)

	//TODO GetVMs
	return listOfVMs, nil
	/*
		clusterName := ""
		nodeRole := ""

		for _, key := range d.VMwareMachineClass.Spec.Tags {
			if strings.Contains(key, "kubernetes.io/cluster/") {
				clusterName = key
			} else if strings.Contains(key, "kubernetes.io/role/") {
				nodeRole = key
			}
		}

		if clusterName == "" || nodeRole == "" {
			return listOfVMs, nil
		}

		svc := d.createSVC()
		if svc == nil {
			return nil, fmt.Errorf("nil VMware service returned")
		}
		if machineID == "" {
			devices, _, err := svc.Devices.List(d.VMwareMachineClass.Spec.ProjectID, &packngo.ListOptions{})
			if err != nil {
				glog.Errorf("Could not list devices for project %s: %v", d.VMwareMachineClass.Spec.ProjectID, err)
				return nil, err
			}
			for _, d := range devices {
				matchedCluster := false
				matchedRole := false
				for _, tag := range d.Tags {
					switch tag {
					case clusterName:
						matchedCluster = true
					case nodeRole:
						matchedRole = true
					}
				}
				if matchedCluster && matchedRole {
					listOfVMs[d.ID] = d.Hostname
				}
			}
		} else {
			machineID = d.decodeMachineID(machineID)
			device, _, err := svc.Devices.Get(machineID, &packngo.GetOptions{})
			if err != nil {
				glog.Errorf("Could not get device %s: %v", machineID, err)
				return nil, err
			}
			listOfVMs[machineID] = device.Hostname
		}
		return listOfVMs, nil
	*/
}

// GetVolNames parses volume names from pv specs
func (d *VMwareDriver) GetVolNames(specs []corev1.PersistentVolumeSpec) ([]string, error) {
	names := []string{}
	return names, fmt.Errorf("Not implemented yet")
}
