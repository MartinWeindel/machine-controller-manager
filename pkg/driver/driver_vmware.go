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
	"reflect"
	"strings"

	"github.com/gardener/machine-controller-manager/pkg/apis/machine/v1alpha1"
	"github.com/gardener/machine-controller-manager/pkg/driver/vmware/helper/folder"
	"github.com/gardener/machine-controller-manager/pkg/driver/vmware/helper/resourcepool"
	"github.com/gardener/machine-controller-manager/pkg/driver/vmware/helper/viapi"
	"github.com/gardener/machine-controller-manager/pkg/driver/vmware/helper/virtualmachine"
	"github.com/gardener/machine-controller-manager/pkg/driver/vmware/virtualdevice"
	"github.com/gardener/machine-controller-manager/pkg/driver/vmware/vmworkflow"
	"github.com/gardener/machine-controller-manager/pkg/driver/vmware/vsphere"
	"github.com/golang/glog"
	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25/types"
	corev1 "k8s.io/api/core/v1"
)

// TODO review rollback error and consequences

// formatVirtualMachinePostCloneRollbackError defines the verbose error when
// rollback fails on a post-clone virtual machine operation.
const formatVirtualMachinePostCloneRollbackError = `
WARNING:
There was an error performing post-clone changes to virtual machine %q:
%s
Additionally, there was an error removing the cloned virtual machine:
%s

The virtual machine may still exist in Terraform state. If it does, the
resource will need to be tainted before trying again. For more information on
how to do this, see the following page:
https://www.terraform.io/docs/commands/taint.html

If the virtual machine does not exist in state, manually delete it to try again.
`

// formatVirtualMachineCustomizationWaitError defines the verbose error that is
// sent when the customization waiter returns an error. This can either be due
// to timeout waiting for respective events or a guest-specific customization
// error. The resource does not roll back in this case, to assist with
// troubleshooting.
const formatVirtualMachineCustomizationWaitError = `
Virtual machine customization failed on %q:

%s

The virtual machine has not been deleted to assist with troubleshooting. If
corrective steps are taken without modifying the "customize" block of the
resource configuration, the resource will need to be tainted before trying
again. For more information on how to do this, see the following page:
https://www.terraform.io/docs/commands/taint.html
`

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

	vm, err := d.resourceVSphereVirtualMachineCreateClone(client)
	if err != nil {
		glog.Errorf("resourceVSphereVirtualMachineCreateClone failed with: %s", err)
		return "", "", err
	}

	// Wait for guest IP address if we have been set to wait for one
	err = virtualmachine.WaitForGuestIP(client, vm, 5, nil)
	if err != nil {
		return "", "", err
	}

	// Wait for a routable address if we have been set to wait for one
	err = virtualmachine.WaitForGuestNet(client, vm, true, 5, nil)
	if err != nil {
		return "", "", err
	}

	// All done!
	glog.V(4).Infof("[DEBUG] %s: Create complete, id=%s", d.MachineName, d.MachineID)

	return d.MachineID, d.MachineName, nil
}

// resourceVSphereVirtualMachineCreateClone contains the clone VM deploy
// path. The VM is returned.
func (d *VMwareDriver) resourceVSphereVirtualMachineCreateClone(client *govmomi.Client) (*object.VirtualMachine, error) {
	glog.V(4).Infof("[DEBUG] %s: VM being created from clone", d.MachineName)

	spec := &d.VMwareMachineClass.Spec

	// Find the folder based off the path to the resource pool. Basically what we
	// are saying here is that the VM folder that we are placing this VM in needs
	// to be in the same hierarchy as the resource pool - so in other words, the
	// same datacenter.
	poolID := spec.ResourcePoolId
	pool, err := resourcepool.FromID(client, poolID)
	if err != nil {
		return nil, fmt.Errorf("could not find resource pool ID %q: %s", poolID, err)
	}
	fo, err := folder.VirtualMachineFolderFromObject(client, pool, spec.Folder)
	if err != nil {
		return nil, err
	}
	// Expand the clone spec. We get the source VM here too.
	cloneSpec, srcVM, err := vmworkflow.ExpandVirtualMachineCloneSpec(spec, client)
	if err != nil {
		return nil, err
	}

	// Start the clone
	name := d.MachineName
	timeout := 30 // minutes
	if spec.Clone.Timeout != nil {
		timeout = *spec.Clone.Timeout
	}
	vm, err := virtualmachine.Clone(client, srcVM, fo, name, cloneSpec, timeout)
	if err != nil {
		return nil, fmt.Errorf("error cloning virtual machine: %s", err)
	}

	// The VM has been created. We still need to do post-clone configuration, and
	// while the resource should have an ID until this is done, we need it to go
	// through post-clone rollback workflows. All rollback functions will remove
	// the ID after it has done its rollback.
	//
	// It's generally safe to not rollback after the initial re-configuration is
	// fully complete and we move on to sending the customization spec.
	vprops, err := virtualmachine.Properties(vm)
	if err != nil {
		return nil, d.resourceVSphereVirtualMachineRollbackCreate(
			client,
			vm,
			fmt.Errorf("cannot fetch properties of created virtual machine: %s", err),
		)
	}
	glog.V(4).Infof("[DEBUG] VM %q - UUID is %q", vm.InventoryPath, vprops.Config.Uuid)
	d.MachineID = vprops.Config.Uuid
	d.MachineInventoryPath = vm.InventoryPath

	// Before starting or proceeding any further, we need to normalize the
	// configuration of the newly cloned VM. This is basically a subset of update
	// with the stipulation that there is currently no state to help move this
	// along.
	cfgSpec, err := d.expandVirtualMachineConfigSpec(client)
	if err != nil {
		return nil, d.resourceVSphereVirtualMachineRollbackCreate(
			client,
			vm,
			fmt.Errorf("error in virtual machine configuration: %s", err),
		)
	}

	// To apply device changes, we need the current devicecfgSpec from the config
	// info. We then filter this list through the same apply process we did for
	// create, which will apply the changes in an incremental fashion.
	devices := object.VirtualDeviceList(vprops.Config.Hardware.Device)
	var delta []types.BaseVirtualDeviceConfigSpec

	// First check the state of our SCSI bus. Normalize it if we need to.
	scsiType := d.getMorePropertiesString("scsi_type")
	scsiControllerCount := d.getMorePropertiesInt("scsi_controller_count")
	scsiBusSharing := d.getMorePropertiesString("scsi_bus_sharing")
	devices, delta, err = virtualdevice.NormalizeSCSIBus(devices, scsiType, scsiControllerCount, scsiBusSharing)
	if err != nil {
		return nil, d.resourceVSphereVirtualMachineRollbackCreate(
			client,
			vm,
			fmt.Errorf("error normalizing SCSI bus post-clone: %s", err),
		)
	}
	cfgSpec.DeviceChange = virtualdevice.AppendDeviceChangeSpec(cfgSpec.DeviceChange, delta...)
	// Disks
	devices, delta, err = virtualdevice.DiskPostCloneOperation(&d.VMwareMachineClass.Spec, client, devices)
	if err != nil {
		return nil, d.resourceVSphereVirtualMachineRollbackCreate(
			client,
			vm,
			fmt.Errorf("error processing disk changes post-clone: %s", err),
		)
	}
	cfgSpec.DeviceChange = virtualdevice.AppendDeviceChangeSpec(cfgSpec.DeviceChange, delta...)
	// Network devices
	devices, delta, err = virtualdevice.NetworkInterfacePostCloneOperation(d.VMwareMachineClass.Spec.NetworkInterfaces, client, devices)
	if err != nil {
		return nil, d.resourceVSphereVirtualMachineRollbackCreate(
			client,
			vm,
			fmt.Errorf("error processing network device changes post-clone: %s", err),
		)
	}
	cfgSpec.DeviceChange = virtualdevice.AppendDeviceChangeSpec(cfgSpec.DeviceChange, delta...)
	/* TODO martin: support CDROM
	// CDROM
	devices, delta, err = virtualdevice.CdromPostCloneOperation(d, client, devices)
	if err != nil {
		return nil, d.resourceVSphereVirtualMachineRollbackCreate(
			client,
			vm,
			fmt.Errorf("error processing CDROM device changes post-clone: %s", err),
		)
	}
	cfgSpec.DeviceChange = virtualdevice.AppendDeviceChangeSpec(cfgSpec.DeviceChange, delta...)
	*/
	glog.V(4).Infof("[DEBUG] %s: Final device list: %s", d.MachineName, virtualdevice.DeviceListString(devices))
	glog.V(4).Infof("[DEBUG] %s: Final device change cfgSpec: %s", d.MachineName, virtualdevice.DeviceChangeString(cfgSpec.DeviceChange))

	// Perform updates
	err = virtualmachine.Reconfigure(vm, cfgSpec)
	if err != nil {
		return nil, d.resourceVSphereVirtualMachineRollbackCreate(
			client,
			vm,
			fmt.Errorf("error reconfiguring virtual machine: %s", err),
		)
	}

	var cw *vsphere.VirtualMachineCustomizationWaiter
	/* TODO martin: support customization ???
	// Send customization spec if any has been defined.
	if len(d.Get("clone.0.customize").([]interface{})) > 0 {
		family, err := resourcepool.OSFamily(client, pool, d.Get("guest_id").(string))
		if err != nil {
			return nil, fmt.Errorf("cannot find OS family for guest ID %q: %s", d.Get("guest_id").(string), err)
		}
		custSpec := vmworkflow.ExpandCustomizationSpec(d, family)
		cw = newVirtualMachineCustomizationWaiter(client, vm, d.Get("clone.0.customize.0.timeout").(int))
		if err := virtualmachine.Customize(vm, custSpec); err != nil {
			// Roll back the VMs as per the error handling in reconfigure.
			if derr := resourceVSphereVirtualMachineDelete(d, meta); derr != nil {
				return nil, fmt.Errorf(formatVirtualMachinePostCloneRollbackError, vm.InventoryPath, err, derr)
			}
			d.SetId("")
			return nil, fmt.Errorf("error sending customization spec: %s", err)
		}
	}
	*/
	// Finally time to power on the virtual machine!
	if err := virtualmachine.PowerOn(vm); err != nil {
		return nil, fmt.Errorf("error powering on virtual machine: %s", err)
	}
	// If we customized, wait on customization.
	if cw != nil {
		glog.V(4).Infof("[DEBUG] %s: Waiting for VM customization to complete", d.MachineName)
		<-cw.Done()
		if err := cw.Err(); err != nil {
			return nil, fmt.Errorf(formatVirtualMachineCustomizationWaitError, vm.InventoryPath, err)
		}
	}
	// Clone is complete and ready to return
	return vm, nil
}

// expandVirtualMachineResourceAllocation reads the VM resource allocation
// resource data keys for the type supplied by key and returns an appropriate
// types.ResourceAllocationInfo reference.
func expandVirtualMachineResourceAllocation(spec *v1alpha1.VMwareMachineClassSpec, key string) *types.ResourceAllocationInfo {
	shareLevelKey := fmt.Sprintf("%s_share_level", key)
	shareCountKey := fmt.Sprintf("%s_share_count", key)
	limitKey := fmt.Sprintf("%s_limit", key)
	reservationKey := fmt.Sprintf("%s_reservation", key)

	limit := int64(virtualdevice.GetMorePropertiesInt(spec, limitKey))
	reservation := int64(virtualdevice.GetMorePropertiesInt(spec, reservationKey))
	obj := &types.ResourceAllocationInfo{
		Limit:       &limit,
		Reservation: &reservation,
	}
	shares := &types.SharesInfo{
		Level:  types.SharesLevel(virtualdevice.GetMorePropertiesString(spec, shareLevelKey)),
		Shares: int32(virtualdevice.GetMorePropertiesInt(spec, shareCountKey)),
	}
	obj.Shares = shares
	return obj
}

// expandVirtualMachineResourceAllocation reads the VM resource allocation
// resource data keys for the type supplied by key and returns an appropriate
// types.ResourceAllocationInfo reference.
func (d *VMwareDriver) expandVirtualMachineResourceAllocation(key string) *types.ResourceAllocationInfo {
	shareLevelKey := fmt.Sprintf("%s_share_level", key)
	shareCountKey := fmt.Sprintf("%s_share_count", key)
	limitKey := fmt.Sprintf("%s_limit", key)
	reservationKey := fmt.Sprintf("%s_reservation", key)

	limit := int64(d.getMorePropertiesInt(limitKey))
	reservation := int64(d.getMorePropertiesInt(reservationKey))
	obj := &types.ResourceAllocationInfo{
		Limit:       &limit,
		Reservation: &reservation,
	}
	shares := &types.SharesInfo{
		Level:  types.SharesLevel(d.getMorePropertiesString(shareLevelKey)),
		Shares: int32(d.getMorePropertiesInt(shareCountKey)),
	}
	obj.Shares = shares
	return obj
}

// expandLatencySensitivity reads certain ResourceData keys and returns a
// LatencySensitivity.
func (d *VMwareDriver) expandLatencySensitivity() *types.LatencySensitivity {
	sensitityLevel := types.LatencySensitivitySensitivityLevelNormal
	if d.VMwareMachineClass.Spec.LatencySensitivity != "" {
		sensitityLevel = types.LatencySensitivitySensitivityLevel(d.VMwareMachineClass.Spec.LatencySensitivity)
	}
	obj := &types.LatencySensitivity{
		Level: sensitityLevel,
	}
	return obj
}

func (d *VMwareDriver) expandExtraConfig() []types.BaseOptionValue {
	// currently not supported
	return nil
}

// expandVAppConfig reads in all the vapp key/value pairs and returns
// the appropriate VmConfigSpec.
//
// We track changes to keys to determine if any have been removed from
// configuration - if they have, we add them with an empty value to ensure
// they are removed from vAppConfig on the update.
func (d *VMwareDriver) expandVAppConfig(client *govmomi.Client) (*types.VmConfigSpec, error) {
	newVApps := d.VMwareMachineClass.Spec.VApp
	if newVApps == nil {
		return nil, nil
	}

	// Many vApp config values, such as IP address, will require a
	// restart of the machine to properly apply. We don't necessarily
	// know which ones they are, so we will restart for every change.
	d.VMwareMachineClass.Spec.RebootRequired = true

	var props []types.VAppPropertySpec

	newMap := newVApps.Properties
	uuid := d.MachineID
	if uuid == "" {
		// No virtual machine has been created, this usually means that this is a
		// brand new virtual machine. vApp properties are not supported on this
		// workflow, so if there are any defined, return an error indicating such.
		// Return with a no-op otherwise.
		if len(newMap) > 0 {
			return nil, fmt.Errorf("vApp properties can only be set on cloned virtual machines")
		}
		return nil, nil
	}
	vm, _ := virtualmachine.FromUUID(client, d.MachineID)
	vmProps, _ := virtualmachine.Properties(vm)
	if vmProps.Config.VAppConfig == nil {
		return nil, fmt.Errorf("this VM lacks a vApp configuration and cannot have vApp properties set on it")
	}
	allProperties := vmProps.Config.VAppConfig.GetVmConfigInfo().Property

	for _, p := range allProperties {
		if *p.UserConfigurable == true {
			defaultValue := " "
			if p.DefaultValue != "" {
				defaultValue = p.DefaultValue
			}
			prop := types.VAppPropertySpec{
				ArrayUpdateSpec: types.ArrayUpdateSpec{
					Operation: types.ArrayUpdateOperationEdit,
				},
				Info: &types.VAppPropertyInfo{
					Key:              p.Key,
					Id:               p.Id,
					Value:            defaultValue,
					UserConfigurable: p.UserConfigurable,
				},
			}

			newValue, ok := newMap[p.Id]
			if ok {
				prop.Info.Value = newValue
				delete(newMap, p.Id)
			}
			props = append(props, prop)
		} else {
			_, ok := newMap[p.Id]
			if ok {
				return nil, fmt.Errorf("vApp property with userConfigurable=false specified in vapp.properties: %+v", reflect.ValueOf(newMap).MapKeys())
			}
		}
	}

	if len(newMap) > 0 {
		return nil, fmt.Errorf("unsupported vApp properties in vapp.properties: %+v", reflect.ValueOf(newMap).MapKeys())
	}

	return &types.VmConfigSpec{
		Property: props,
	}, nil
}

func (d *VMwareDriver) expandCPUCountConfig() int32 {
	return int32(d.VMwareMachineClass.Spec.NumCpus)
}

func (d *VMwareDriver) expandMemorySizeConfig() int64 {
	return int64(d.VMwareMachineClass.Spec.Memory)
}

// expandVirtualMachineBootOptions reads certain ResourceData keys and
// returns a VirtualMachineBootOptions.
func (d *VMwareDriver) expandVirtualMachineBootOptions(client *govmomi.Client) *types.VirtualMachineBootOptions {
	obj := &types.VirtualMachineBootOptions{
		BootDelay:        int64(d.getMorePropertiesInt("boot_delay")),
		BootRetryEnabled: d.getMorePropertiesBool("boot_retry_enabled"),
		BootRetryDelay:   int64(d.getMorePropertiesInt("boot_retry_delay")),
	}
	// Only set EFI secure boot if we are on vSphere 6.5 and higher
	version := viapi.ParseVersionFromClient(client)
	if version.Newer(viapi.VSphereVersion{Product: version.Product, Major: 6, Minor: 5}) {
		obj.EfiSecureBootEnabled = d.getMorePropertiesBoolWithRestart("efi_secure_boot_enabled")
	}
	return obj
}

// expandVirtualMachineFlagInfo reads certain ResourceData keys and
// returns a VirtualMachineFlagInfo.
func (d *VMwareDriver) expandVirtualMachineFlagInfo() *types.VirtualMachineFlagInfo {
	obj := &types.VirtualMachineFlagInfo{
		DiskUuidEnabled:  d.getMorePropertiesBoolWithRestart("enable_disk_uuid"),
		VirtualExecUsage: d.getMorePropertiesStringWithRestart("hv_mode"),
		VirtualMmuUsage:  d.getMorePropertiesStringWithRestart("ept_rvi_mode"),
		EnableLogging:    d.getMorePropertiesBoolWithRestart("enable_logging"),
	}
	return obj
}

// expandToolsConfigInfo reads certain ResourceData keys and
// returns a ToolsConfigInfo.
func (d *VMwareDriver) expandToolsConfigInfo() *types.ToolsConfigInfo {
	obj := &types.ToolsConfigInfo{
		SyncTimeWithHost:    d.getMorePropertiesBool("sync_time_with_host"),
		AfterPowerOn:        d.getMorePropertiesBoolWithRestart("run_tools_scripts_after_power_on"),
		AfterResume:         d.getMorePropertiesBoolWithRestart("run_tools_scripts_after_resume"),
		BeforeGuestStandby:  d.getMorePropertiesBoolWithRestart("run_tools_scripts_before_guest_standby"),
		BeforeGuestShutdown: d.getMorePropertiesBoolWithRestart("run_tools_scripts_before_guest_shutdown"),
		BeforeGuestReboot:   d.getMorePropertiesBoolWithRestart("run_tools_scripts_before_guest_reboot"),
	}
	return obj
}

// expandVirtualMachineConfigSpec reads certain ResourceData keys and
// returns a VirtualMachineConfigSpec.
func (d *VMwareDriver) expandVirtualMachineConfigSpec(client *govmomi.Client) (types.VirtualMachineConfigSpec, error) {
	glog.V(4).Infof("[DEBUG] %s: Building config spec", d.MachineName)
	vappConfig, err := d.expandVAppConfig(client)
	if err != nil {
		return types.VirtualMachineConfigSpec{}, err
	}

	guestId := "other-64"
	if d.VMwareMachineClass.Spec.GuestId != "" {
		guestId = d.VMwareMachineClass.Spec.GuestId
	}
	obj := types.VirtualMachineConfigSpec{
		Name:                         d.MachineName,
		GuestId:                      guestId,
		AlternateGuestName:           d.getMorePropertiesStringWithRestart("alternate_guest_name"),
		Annotation:                   strings.Join(d.VMwareMachineClass.Spec.Tags, ","),
		Tools:                        d.expandToolsConfigInfo(),
		Flags:                        d.expandVirtualMachineFlagInfo(),
		NumCPUs:                      d.expandCPUCountConfig(),
		NumCoresPerSocket:            int32(d.getMorePropertiesIntWithRestart("num_cores_per_socket")),
		MemoryMB:                     d.expandMemorySizeConfig(),
		MemoryHotAddEnabled:          d.getMorePropertiesBoolWithRestart("memory_hot_add_enabled"),
		CpuHotAddEnabled:             d.getMorePropertiesBoolWithRestart("cpu_hot_add_enabled"),
		CpuHotRemoveEnabled:          d.getMorePropertiesBoolWithRestart("cpu_hot_remove_enabled"),
		CpuAllocation:                d.expandVirtualMachineResourceAllocation("cpu"),
		MemoryAllocation:             d.expandVirtualMachineResourceAllocation("memory"),
		MemoryReservationLockedToMax: d.getMemoryReservationLockedToMax(),
		ExtraConfig:                  d.expandExtraConfig(),
		SwapPlacement:                d.getMorePropertiesStringWithRestart("swap_placement_policy"),
		BootOptions:                  d.expandVirtualMachineBootOptions(client),
		VAppConfig:                   vappConfig,
		Firmware:                     d.getMorePropertiesStringWithRestart("firmware"),
		NestedHVEnabled:              d.getMorePropertiesBoolWithRestart("nested_hv_enabled"),
		VPMCEnabled:                  d.getMorePropertiesBoolWithRestart("cpu_performance_counters_enabled"),
		LatencySensitivity:           d.expandLatencySensitivity(),
	}

	return obj, nil
}

func (d *VMwareDriver) getMorePropertiesBoolWithRestart(key string) *bool {
	// restart is ignore as VM is never changed
	return d.getMorePropertiesBool(key)
}

func (d *VMwareDriver) getMorePropertiesBool(key string) *bool {
	return virtualdevice.GetMorePropertiesBool(&d.VMwareMachineClass.Spec, key)
}

func (d *VMwareDriver) getMorePropertiesStringWithRestart(key string) string {
	// restart is ignore as VM is never changed
	return d.getMorePropertiesString(key)
}

func (d *VMwareDriver) getMorePropertiesString(key string) string {
	return virtualdevice.GetMorePropertiesString(&d.VMwareMachineClass.Spec, key)
}

func (d *VMwareDriver) getMorePropertiesIntWithRestart(key string) int {
	// restart is ignore as VM is never changed
	return d.getMorePropertiesInt(key)
}

func (d *VMwareDriver) getMorePropertiesInt(key string) int {
	return virtualdevice.GetMorePropertiesInt(&d.VMwareMachineClass.Spec, key)
}

// getMemoryReservationLockedToMax determines if the memory_reservation is not
// set to be equal to memory. If they are not equal, then the memory
// reservation needs to be unlocked from the maximum. Rather than supporting
// the locking reservation to max option, we can set memory_reservation to
// memory in the configuration. Not supporting the option causes problems when
// cloning from a template that has it enabled. The solution is to set it to
// false when needed, but leave it alone when the change is not necessary.
func (d *VMwareDriver) getMemoryReservationLockedToMax() *bool {
	if d.getMorePropertiesInt("memory_reservation") != d.VMwareMachineClass.Spec.Memory {
		b := false
		return &b
	}
	return nil
}

// resourceVSphereVirtualMachineRollbackCreate attempts to "roll back" a
// resource due to an error that happened post-create that will put the VM in a
// state where it cannot be worked with. This should only be done early on in
// the process, namely on clone operations between when the clone actually
// happens, and no later than after the initial post-clone update is complete.
//
// If the rollback fails, an error is displayed prompting the user to manually
// delete the virtual machine before trying again.
func (d *VMwareDriver) resourceVSphereVirtualMachineRollbackCreate(
	client *govmomi.Client,
	vm *object.VirtualMachine,
	origErr error,
) error {
	defer func() {
		d.MachineID = ""
	}()

	// Updates are largely atomic, so more than likely no disks with
	// keep_on_remove were attached, but just in case, we run this through delete
	// to make sure to safely remove any disk that may have been attached as part
	// of this process if it was flagged as such.
	if err := d.resourceVSphereVirtualMachineDelete(client); err != nil {
		return fmt.Errorf(formatVirtualMachinePostCloneRollbackError, vm.InventoryPath, origErr, err)
	}
	return fmt.Errorf("error reconfiguring virtual machine: %s", origErr)
}

func (d *VMwareDriver) resourceVSphereVirtualMachineDelete(client *govmomi.Client) error {
	glog.V(4).Infof("[DEBUG] %s: Performing delete", d.MachineName)
	id := d.MachineID
	vm, err := virtualmachine.FromUUID(client, id)
	if err != nil {
		return fmt.Errorf("cannot locate virtual machine with UUID %q: %s", id, err)
	}
	vprops, err := virtualmachine.Properties(vm)
	if err != nil {
		return fmt.Errorf("error fetching VM properties: %s", err)
	}
	// Shutdown the VM first. We do attempt a graceful shutdown for the purpose
	// of catching any edge data issues with associated virtual disks that we may
	// need to retain on delete. However, we ignore the user-set force shutdown
	// flag.
	if vprops.Runtime.PowerState != types.VirtualMachinePowerStatePoweredOff {
		timeout := 3 // minutes
		if d.VMwareMachineClass.Spec.ShutdownWaitTimeout != nil {
			timeout = *d.VMwareMachineClass.Spec.ShutdownWaitTimeout
		}
		// d.Get("shutdown_wait_timeout").(int)
		if err := virtualmachine.GracefulPowerOff(client, vm, timeout, true); err != nil {
			return fmt.Errorf("error shutting down virtual machine: %s", err)
		}
	}
	// Now attempt to detach any virtual disks that may need to be preserved.
	devices := object.VirtualDeviceList(vprops.Config.Hardware.Device)
	spec := types.VirtualMachineConfigSpec{}
	if spec.DeviceChange, err = virtualdevice.DiskDestroyOperation(&d.VMwareMachineClass.Spec, client, devices); err != nil {
		return err
	}
	// Only run the reconfigure operation if there's actually disks in the spec.
	if len(spec.DeviceChange) > 0 {
		if err := virtualmachine.Reconfigure(vm, spec); err != nil {
			return fmt.Errorf("error detaching virtual disks: %s", err)
		}
	}

	// The final operation here is to destroy the VM.
	if err := virtualmachine.Destroy(vm); err != nil {
		return fmt.Errorf("error destroying virtual machine: %s", err)
	}
	d.MachineID = ""
	glog.V(4).Infof("[DEBUG] %s: Delete complete", d.MachineName)
	return nil
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
