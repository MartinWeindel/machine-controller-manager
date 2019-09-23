package vmworkflow

import (
	"fmt"
	"github.com/gardener/machine-controller-manager/pkg/apis/machine/v1alpha1"
	"log"

	"github.com/gardener/machine-controller-manager/pkg/driver/vmware/helper/datastore"
	"github.com/gardener/machine-controller-manager/pkg/driver/vmware/helper/hostsystem"
	"github.com/gardener/machine-controller-manager/pkg/driver/vmware/helper/resourcepool"
	"github.com/gardener/machine-controller-manager/pkg/driver/vmware/helper/virtualmachine"
	"github.com/gardener/machine-controller-manager/pkg/driver/vmware/virtualdevice"
	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"
)

// validateCloneSnapshots checks a VM to make sure it has a single snapshot
// with no children, to make sure there is no ambiguity when selecting a
// snapshot for linked clones.
func validateCloneSnapshots(props *mo.VirtualMachine) error {
	if props.Snapshot == nil {
		return fmt.Errorf("virtual machine or template %s must have a snapshot to be used as a linked clone", props.Config.Uuid)
	}
	// Root snapshot list can only have a singular element
	if len(props.Snapshot.RootSnapshotList) != 1 {
		return fmt.Errorf("virtual machine or template %s must have exactly one root snapshot (has: %d)", props.Config.Uuid, len(props.Snapshot.RootSnapshotList))
	}
	// Check to make sure the root snapshot has no children
	if len(props.Snapshot.RootSnapshotList[0].ChildSnapshotList) > 0 {
		return fmt.Errorf("virtual machine or template %s's root snapshot must not have children", props.Config.Uuid)
	}
	// Current snapshot must match root snapshot (this should be the case anyway)
	if props.Snapshot.CurrentSnapshot.Value != props.Snapshot.RootSnapshotList[0].Snapshot.Value {
		return fmt.Errorf("virtual machine or template %s's current snapshot must match root snapshot", props.Config.Uuid)
	}
	return nil
}

// ExpandVirtualMachineCloneSpec creates a clone spec for an existing virtual machine.
//
// The clone spec built by this function for the clone contains the target
// datastore, the source snapshot in the event of linked clones, and a relocate
// spec that contains the new locations and configuration details of the new
// virtual disks.
func ExpandVirtualMachineCloneSpec(classSpec *v1alpha1.VMwareMachineClassSpec, c *govmomi.Client) (types.VirtualMachineCloneSpec, *object.VirtualMachine, error) {
	var spec types.VirtualMachineCloneSpec
	log.Printf("[DEBUG] ExpandVirtualMachineCloneSpec: Preparing clone spec for VM")

	ds, err := datastore.FromID(c, classSpec.DatastoreId)
	if err != nil {
		return spec, nil, fmt.Errorf("error locating datastore for VM: %s", err)
	}
	spec.Location.Datastore = types.NewReference(ds.Reference())

	tUUID := classSpec.Clone.TemplateUuid
	log.Printf("[DEBUG] ExpandVirtualMachineCloneSpec: Cloning from UUID: %s", tUUID)
	vm, err := virtualmachine.FromUUID(c, tUUID)
	if err != nil {
		return spec, nil, fmt.Errorf("cannot locate virtual machine or template with UUID %q: %s", tUUID, err)
	}
	vprops, err := virtualmachine.Properties(vm)
	if err != nil {
		return spec, nil, fmt.Errorf("error fetching virtual machine or template properties: %s", err)
	}
	// If we are creating a linked clone, grab the current snapshot of the
	// source, and populate the appropriate field. This should have already been
	// validated, but just in case, validate it again here.
	if classSpec.Clone.LinkedClone {
		log.Printf("[DEBUG] ExpandVirtualMachineCloneSpec: Clone type is a linked clone")
		log.Printf("[DEBUG] ExpandVirtualMachineCloneSpec: Fetching snapshot for VM/template UUID %s", tUUID)
		if err := validateCloneSnapshots(vprops); err != nil {
			return spec, nil, err
		}
		spec.Snapshot = vprops.Snapshot.CurrentSnapshot
		spec.Location.DiskMoveType = string(types.VirtualMachineRelocateDiskMoveOptionsCreateNewChildDiskBacking)
		log.Printf("[DEBUG] ExpandVirtualMachineCloneSpec: Snapshot for clone: %s", vprops.Snapshot.CurrentSnapshot.Value)
	}

	// Set the target host system and resource pool.
	poolID := classSpec.ResourcePoolId
	pool, err := resourcepool.FromID(c, poolID)
	if err != nil {
		return spec, nil, fmt.Errorf("could not find resource pool ID %q: %s", poolID, err)
	}
	var hs *object.HostSystem
	if classSpec.HostSystemId != "" {
		hsID := classSpec.HostSystemId
		var err error
		if hs, err = hostsystem.FromID(c, hsID); err != nil {
			return spec, nil, fmt.Errorf("error locating host system at ID %q: %s", hsID, err)
		}
	}
	// Validate that the host is part of the resource pool before proceeding
	if err := resourcepool.ValidateHost(c, pool, hs); err != nil {
		return spec, nil, err
	}
	poolRef := pool.Reference()
	spec.Location.Pool = &poolRef
	if hs != nil {
		hsRef := hs.Reference()
		spec.Location.Host = &hsRef
	}

	// Grab the relocate spec for the disks.
	l := object.VirtualDeviceList(vprops.Config.Hardware.Device)
	relocators, err := virtualdevice.DiskCloneRelocateOperation(classSpec, c, l)
	if err != nil {
		return spec, nil, err
	}
	spec.Location.Disk = relocators
	log.Printf("[DEBUG] ExpandVirtualMachineCloneSpec: Clone spec prep complete")
	return spec, vm, nil
}
