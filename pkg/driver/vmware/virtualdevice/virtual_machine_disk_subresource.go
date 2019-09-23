package virtualdevice

import (
	"errors"
	"fmt"
	"math"
	"path"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/gardener/machine-controller-manager/pkg/apis/machine/v1alpha1"
	"github.com/gardener/machine-controller-manager/pkg/driver/vmware/helper/datastore"
	"github.com/gardener/machine-controller-manager/pkg/driver/vmware/helper/structure"
	"github.com/gardener/machine-controller-manager/pkg/driver/vmware/helper/viapi"
	"github.com/gardener/machine-controller-manager/pkg/driver/vmware/schema"
	"github.com/gardener/machine-controller-manager/pkg/driver/vmware/validation"
	"github.com/golang/glog"
	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25/types"
)

// diskNameDeprecationNotice is the deprecation warning for the "name"
// attribute, which will removed in 2.0. The notice is verbose, so we format it
// so it looks a little better over CLI.
//
// TODO: Remove this in 2.0.
const diskNameDeprecationNotice = `
The name attribute for virtual disks will be removed in favor of "label" in
future releases. To transition existing disks, rename the "name" attribute to
"label". When doing so, ensure the value of the attribute stays the same.

Note that "label" does not control the name of a VMDK and does not need to bear
the name of one on new disks or virtual machines. For more information, see the
documentation for the label attribute at: 

https://www.terraform.io/docs/providers/vsphere/r/virtual_machine.html#label
`

// diskDatastoreComputedName is a friendly display for disks with datastores
// marked as computed. This happens in datastore cluster workflows.
const diskDatastoreComputedName = "<computed>"

// diskDeletedName is a placeholder name for deleted disks. This is to assist
// with user-friendliness in the diff.
const diskDeletedName = "<deleted>"

// diskDetachedName is a placeholder name for disks that are getting detached,
// either because they have keep_on_remove set or are external disks attached
// with "attach".
const diskDetachedName = "<remove, keep disk>"

// diskOrphanedPrefix is a placeholder name for disks that have been discovered
// as not being tracked by Terraform. These disks are assigned
// "orphaned_disk_0", "orphaned_disk_1", and so on.
const diskOrphanedPrefix = "orphaned_disk_"

var diskSubresourceModeAllowedValues = []string{
	string(types.VirtualDiskModePersistent),
	string(types.VirtualDiskModeNonpersistent),
	string(types.VirtualDiskModeUndoable),
	string(types.VirtualDiskModeIndependent_persistent),
	string(types.VirtualDiskModeIndependent_nonpersistent),
	string(types.VirtualDiskModeAppend),
}

var diskSubresourceSharingAllowedValues = []string{
	string(types.VirtualDiskSharingSharingNone),
	string(types.VirtualDiskSharingSharingMultiWriter),
}

// DiskSubresourceSchema represents the schema for the disk sub-resource.
func DiskSubresourceSchema() map[string]*schema.Schema {
	s := map[string]*schema.Schema{
		// VirtualDiskFlatVer2BackingInfo
		"datastore_id": {
			Type:        schema.TypeString,
			Optional:    true,
			Description: "The datastore ID for this virtual disk, if different than the virtual machine.",
		},
		"path": {
			Type:        schema.TypeString,
			Optional:    true,
			Description: "The full path of the virtual disk. This can only be provided if attach is set to true, otherwise it is a read-only value.",
			ValidateFunc: func(v interface{}, _ string) ([]string, []error) {
				if path.Ext(v.(string)) != ".vmdk" {
					return nil, []error{fmt.Errorf("disk path %s must end in .vmdk", v.(string))}
				}
				return nil, nil
			},
		},
		"disk_mode": {
			Type:         schema.TypeString,
			Optional:     true,
			Default:      string(types.VirtualDiskModePersistent),
			Description:  "The mode of this this virtual disk for purposes of writes and snapshotting. Can be one of append, independent_nonpersistent, independent_persistent, nonpersistent, persistent, or undoable.",
			ValidateFunc: validation.StringInSlice(diskSubresourceModeAllowedValues, false),
		},
		"eagerly_scrub": {
			Type:        schema.TypeBool,
			Optional:    true,
			Default:     false,
			Description: "The virtual disk file zeroing policy when thin_provision is not true. The default is false, which lazily-zeros the disk, speeding up thick-provisioned disk creation time.",
		},
		"disk_sharing": {
			Type:         schema.TypeString,
			Optional:     true,
			Default:      string(types.VirtualDiskSharingSharingNone),
			Description:  "The sharing mode of this virtual disk. Can be one of sharingMultiWriter or sharingNone.",
			ValidateFunc: validation.StringInSlice(diskSubresourceSharingAllowedValues, false),
		},
		"thin_provisioned": {
			Type:        schema.TypeBool,
			Optional:    true,
			Default:     true,
			Description: "If true, this disk is thin provisioned, with space for the file being allocated on an as-needed basis.",
		},
		"write_through": {
			Type:        schema.TypeBool,
			Optional:    true,
			Default:     false,
			Description: "If true, writes for this disk are sent directly to the filesystem immediately instead of being buffered.",
		},

		// StorageIOAllocationInfo
		"io_limit": {
			Type:         schema.TypeInt,
			Optional:     true,
			Default:      -1,
			Description:  "The upper limit of IOPS that this disk can use.",
			ValidateFunc: validation.IntAtLeast(-1),
		},
		"io_reservation": {
			Type:         schema.TypeInt,
			Optional:     true,
			Default:      0,
			Description:  "The I/O guarantee that this disk has, in IOPS.",
			ValidateFunc: validation.IntAtLeast(0),
		},
		"io_share_level": {
			Type:         schema.TypeString,
			Optional:     true,
			Default:      string(types.SharesLevelNormal),
			Description:  "The share allocation level for this disk. Can be one of low, normal, high, or custom.",
			ValidateFunc: validation.StringInSlice(sharesLevelAllowedValues, false),
		},
		"io_share_count": {
			Type:         schema.TypeInt,
			Optional:     true,
			Default:      0,
			Description:  "The share count for this disk when the share level is custom.",
			ValidateFunc: validation.IntAtLeast(0),
		},
	}
	structure.MergeSchema(s, subresourceSchema())
	return s
}

// DiskSubresource represents a vsphere_virtual_machine disk sub-resource, with
// a complex device lifecycle.
type DiskSubresource struct {
	*Subresource

	// The set hash for the device as it exists when NewDiskSubresource is
	// called.
	ID int

	disk *v1alpha1.VMwareDisk
}

// NewDiskSubresource returns a subresource populated with all of the necessary
// fields.
func NewDiskSubresource(client *govmomi.Client, disk *v1alpha1.VMwareDisk, idx int) *DiskSubresource {
	sr := &DiskSubresource{
		Subresource: &Subresource{
			schema: DiskSubresourceSchema(),
			client: client,
			srtype: subresourceTypeDisk,
			data:   disk.MoreProperties,
		},
		disk: disk,
	}
	sr.Index = idx
	return sr
}

// DiskCloneRelocateOperation assembles the
// VirtualMachineRelocateSpecDiskLocator slice for a virtual machine clone
// operation.
//
// This differs from a regular storage vMotion in that we have no existing
// devices in the resource to work off of - the disks in the source virtual
// machine is our source of truth. These disks are assigned to our disk
// sub-resources in config and the relocate specs are generated off of the
// backing data defined in config, taking on these filenames when cloned. After
// the clone is complete, natural re-configuration happens to bring the disk
// configurations fully in sync with what is defined.
func DiskCloneRelocateOperation(classSpec *v1alpha1.VMwareMachineClassSpec, c *govmomi.Client, l object.VirtualDeviceList) ([]types.VirtualMachineRelocateSpecDiskLocator, error) {
	glog.V(4).Infof("[DEBUG] DiskCloneRelocateOperation: Generating full disk relocate spec list")
	devices := SelectDisks(l, GetMorePropertiesInt(classSpec, "scsi_controller_count"))
	glog.V(4).Infof("[DEBUG] DiskCloneRelocateOperation: Disk devices located: %s", DeviceListString(devices))
	// Sort the device list, in case it's not sorted already.
	devSort := virtualDeviceListSorter{
		Sort:       devices,
		DeviceList: l,
	}
	sort.Sort(devSort)
	devices = devSort.Sort
	glog.V(4).Infof("[DEBUG] DiskCloneRelocateOperation: Disk devices order after sort: %s", DeviceListString(devices))
	// Do the same for our listed disks.
	curSet := classSpec.Disks
	glog.V(4).Infof("[DEBUG] DiskCloneRelocateOperation: Current resource set: %s", subresourceDiskListString(curSet))
	sort.Sort(virtualDiskSubresourceSorter(curSet))
	glog.V(4).Infof("[DEBUG] DiskCloneRelocateOperation: Resource set order after sort: %s", subresourceDiskListString(curSet))

	glog.V(4).Infof("[DEBUG] DiskCloneRelocateOperation: Generating relocators for source disks")
	var relocators []types.VirtualMachineRelocateSpecDiskLocator
	for i, device := range devices {
		m := curSet[i]
		if m.MoreProperties == nil {
			m.MoreProperties = map[string]string{}
		}
		vd := device.GetVirtualDevice()
		ctlr := l.FindByKey(vd.ControllerKey)
		if ctlr == nil {
			return nil, fmt.Errorf("could not find controller with key %d", vd.Key)
		}
		m.MoreProperties["key"] = fmt.Sprintf("%d", int(vd.Key))
		var err error
		m.MoreProperties["device_address"], err = computeDevAddr(vd, ctlr.(types.BaseVirtualController))
		if err != nil {
			return nil, fmt.Errorf("error computing device address: %s", err)
		}
		r := NewDiskSubresource(c, m, i)
		// A disk locator is only useful if a target datastore is available. If we
		// don't have a datastore specified (ie: when Storage DRS is in use), then
		// we just need to skip this disk. The disk will be migrated properly
		// through the SDRS API.
		if dsID := r.GetString("datastore_id"); dsID == "" || dsID == diskDatastoreComputedName {
			continue
		}
		// Otherwise, proceed with generating and appending the locator.
		relocator, err := r.Relocate(l, true)
		if err != nil {
			return nil, fmt.Errorf("%s: %s", r.Addr(), err)
		}
		relocators = append(relocators, relocator)
	}

	glog.V(4).Infof("[DEBUG] DiskCloneRelocateOperation: Disk relocator list: %s", diskRelocateListString(relocators))
	glog.V(4).Infof("[DEBUG] DiskCloneRelocateOperation: Disk relocator generation complete")
	return relocators, nil
}

// DiskPostCloneOperation normalizes the virtual disks on a freshly-cloned
// virtual machine and outputs any necessary device change operations. It also
// sets the state in advance of the post-create read.
//
// This differs from a regular apply operation in that a configuration is
// already present, but we don't have any existing state, which the standard
// virtual device operations rely pretty heavily on.
func DiskPostCloneOperation(classSpec *v1alpha1.VMwareMachineClassSpec, c *govmomi.Client, l object.VirtualDeviceList) (object.VirtualDeviceList, []types.BaseVirtualDeviceConfigSpec, error) {
	glog.V(4).Infof("[DEBUG] DiskPostCloneOperation: Looking for disk device changes post-clone")
	devices := SelectDisks(l, GetMorePropertiesInt(classSpec, "scsi_controller_count"))
	glog.V(4).Infof("[DEBUG] DiskPostCloneOperation: Disk devices located: %s", DeviceListString(devices))
	// Sort the device list, in case it's not sorted already.
	devSort := virtualDeviceListSorter{
		Sort:       devices,
		DeviceList: l,
	}
	sort.Sort(devSort)
	devices = devSort.Sort
	glog.V(4).Infof("[DEBUG] DiskPostCloneOperation: Disk devices order after sort: %s", DeviceListString(devices))
	// Do the same for our listed disks.
	curSet := classSpec.Disks
	glog.V(4).Infof("[DEBUG] DiskPostCloneOperation: Current resource set: %s", subresourceDiskListString(curSet))
	sort.Sort(virtualDiskSubresourceSorter(curSet))
	glog.V(4).Infof("[DEBUG] DiskPostCloneOperation: Resource set order after sort: %s", subresourceDiskListString(curSet))

	var spec []types.BaseVirtualDeviceConfigSpec
	var updates []interface{}

	glog.V(4).Infof("[DEBUG] DiskPostCloneOperation: Looking for and applying device changes in source disks")
	for i, device := range devices {
		src := curSet[i]
		if src.MoreProperties == nil {
			src.MoreProperties = map[string]string{}
		}

		vd := device.GetVirtualDevice()
		ctlr := l.FindByKey(vd.ControllerKey)
		if ctlr == nil {
			return nil, nil, fmt.Errorf("could not find controller with key %d", vd.Key)
		}
		src.MoreProperties["key"] = fmt.Sprintf("%d", int(vd.Key))
		var err error
		src.MoreProperties["device_address"], err = computeDevAddr(vd, ctlr.(types.BaseVirtualController))
		if err != nil {
			return nil, nil, fmt.Errorf("error computing device address: %s", err)
		}
		rOld := NewDiskSubresource(c, src, i)

		new := &v1alpha1.VMwareDisk{
			Attach:         src.Attach,
			KeepOnRemove:   src.KeepOnRemove,
			MoreProperties: map[string]string{},
			Size:           src.Size,
			UnitNumber:     src.UnitNumber,
		}
		for k, v := range src.MoreProperties {
			// Skip label, path (path will always be computed here as cloned disks
			// are not being attached externally), name, datastore_id, and uuid. Also
			// skip share_count if we the share level isn't custom.
			//
			// TODO: Remove "name" after 2.0.
			switch k {
			case "path", "datastore_id", "uuid":
				continue
			case "io_share_count":
				if src.MoreProperties["io_share_level"] != string(types.SharesLevelCustom) {
					continue
				}
			}
			new.MoreProperties[k] = v
		}
		rNew := NewDiskSubresource(c, new, i)
		if !reflect.DeepEqual(rNew.data, rOld.data) {
			uspec, err := rNew.Update(l)
			if err != nil {
				return nil, nil, fmt.Errorf("%s: %s", rNew.Addr(), err)
			}
			l = applyDeviceChange(l, uspec)
			spec = append(spec, uspec...)
		}
		updates = append(updates, rNew.data)
	}

	// Any disk past the current device list is a new device. Create those now.
	for _, disk := range curSet[len(devices):] {
		r := NewDiskSubresource(c, disk, len(updates))
		cspec, err := r.Create(l)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %s", r.Addr(), err)
		}
		l = applyDeviceChange(l, cspec)
		spec = append(spec, cspec...)
		updates = append(updates, r.data)
	}

	// TODO martin: implement updating disk specs
	// glog.V(4).Infof("[DEBUG] DiskPostCloneOperation: Post-clone final resource list: %s", subresourceListString(updates))
	// if err := d.Set(subresourceTypeDisk, updates); err != nil {
	//	return nil, nil, err
	//}

	glog.V(4).Infof("[DEBUG] DiskPostCloneOperation: Device list at end of operation: %s", DeviceListString(l))
	glog.V(4).Infof("[DEBUG] DiskPostCloneOperation: Device config operations from post-clone: %s", DeviceChangeString(spec))
	glog.V(4).Infof("[DEBUG] DiskPostCloneOperation: Operation complete, returning updated spec")
	return l, spec, nil
}

// DiskDestroyOperation process the destroy operation for virtual disks.
//
// Disks are the only real operation that require special destroy logic, and
// that's because we want to check to make sure that we detach any disks that
// need to be simply detached (not deleted) before we destroy the entire
// virtual machine, as that would take those disks with it.
func DiskDestroyOperation(classSpec *v1alpha1.VMwareMachineClassSpec, c *govmomi.Client, l object.VirtualDeviceList) ([]types.BaseVirtualDeviceConfigSpec, error) {
	glog.V(4).Infof("[DEBUG] DiskDestroyOperation: Beginning destroy")
	// All we are doing here is getting a config spec for detaching the disks
	// that we need to detach, so we don't need the vast majority of the stateful
	// logic that is in deviceApplyOperation.
	ds := classSpec.Disks

	var spec []types.BaseVirtualDeviceConfigSpec

	glog.V(4).Infof("[DEBUG] DiskDestroyOperation: Detaching devices with keep_on_remove enabled")
	for oi, disk := range ds {
		if !disk.KeepOnRemove && !disk.Attach {
			// We don't care about disks we haven't set to keep
			continue
		}
		r := NewDiskSubresource(c, disk, oi)
		dspec, err := r.Delete(l)
		if err != nil {
			return nil, fmt.Errorf("%s: %s", r.Addr(), err)
		}
		l = applyDeviceChange(l, dspec)
		spec = append(spec, dspec...)
	}

	glog.V(4).Infof("[DEBUG] DiskDestroyOperation: Device config operations from destroy: %s", DeviceChangeString(spec))
	return spec, nil
}

// ReadDiskAttrsForDataSource returns select attributes from the list of disks
// on a virtual machine. This is used in the VM data source to discover
// specific options of all of the disks on the virtual machine sorted by the
// order that they would be added in if a clone were to be done.
func ReadDiskAttrsForDataSource(l object.VirtualDeviceList, count int) ([]map[string]interface{}, error) {
	glog.V(4).Infof("[DEBUG] ReadDiskAttrsForDataSource: Fetching select attributes for disks across %d SCSI controllers", count)
	devices := SelectDisks(l, count)
	glog.V(4).Infof("[DEBUG] ReadDiskAttrsForDataSource: Disk devices located: %s", DeviceListString(devices))
	// Sort the device list, in case it's not sorted already.
	devSort := virtualDeviceListSorter{
		Sort:       devices,
		DeviceList: l,
	}
	sort.Sort(devSort)
	devices = devSort.Sort
	glog.V(4).Infof("[DEBUG] ReadDiskAttrsForDataSource: Disk devices order after sort: %s", DeviceListString(devices))
	var out []map[string]interface{}
	for i, device := range devices {
		disk := device.(*types.VirtualDisk)
		backing, ok := disk.Backing.(*types.VirtualDiskFlatVer2BackingInfo)
		if !ok {
			return nil, fmt.Errorf("disk number %d has an unsupported backing type (expected flat VMDK version 2, got %T)", i, disk.Backing)
		}
		m := make(map[string]interface{})
		var eager, thin bool
		if backing.EagerlyScrub != nil {
			eager = *backing.EagerlyScrub
		}
		if backing.ThinProvisioned != nil {
			thin = *backing.ThinProvisioned
		}
		m["size"] = diskCapacityInGiB(disk)
		m["eagerly_scrub"] = eager
		m["thin_provisioned"] = thin
		out = append(out, m)
	}
	glog.V(4).Infof("[DEBUG] ReadDiskAttrsForDataSource: Attributes returned: %+v", out)
	return out, nil
}

// Create creates a vsphere_virtual_machine disk sub-resource.
func (r *DiskSubresource) Create(l object.VirtualDeviceList) ([]types.BaseVirtualDeviceConfigSpec, error) {
	glog.V(4).Infof("[DEBUG] %s: Creating disk", r)
	var spec []types.BaseVirtualDeviceConfigSpec

	disk, err := r.createDisk(l)
	if err != nil {
		return nil, fmt.Errorf("error creating disk: %s", err)
	}
	// We now have the controller on which we can create our device on.
	// Assign the disk to a controller.
	ctlr, err := r.assignDisk(l, disk)
	if err != nil {
		return nil, fmt.Errorf("cannot assign disk: %s", err)
	}

	if err := r.expandDiskSettings(disk); err != nil {
		return nil, err
	}

	// Done here. Save ID, push the device to the new device list and return.
	if err := r.SaveDevIDs(disk, ctlr); err != nil {
		return nil, err
	}
	dspec, err := object.VirtualDeviceList{disk}.ConfigSpec(types.VirtualDeviceConfigSpecOperationAdd)
	if err != nil {
		return nil, err
	}
	if len(dspec) != 1 {
		return nil, fmt.Errorf("incorrect number of config spec items returned - expected 1, got %d", len(dspec))
	}
	// Clear the file operation if we are attaching.
	if r.disk.Attach {
		dspec[0].GetVirtualDeviceConfigSpec().FileOperation = ""
	}
	spec = append(spec, dspec...)
	glog.V(4).Infof("[DEBUG] %s: Device config operations from create: %s", r, DeviceChangeString(spec))
	glog.V(4).Infof("[DEBUG] %s: Create finished", r)
	return spec, nil
}

/*
// Read reads a vsphere_virtual_machine disk sub-resource and commits the data
// to the newData layer.
func (r *DiskSubresource) Read(l object.VirtualDeviceList) error {
	glog.V(4).Infof("[DEBUG] %s: Reading state", r)
	disk, err := r.findVirtualDisk(l, true)
	if err != nil {
		return fmt.Errorf("cannot find disk device: %s", err)
	}
	unit, ctlr, err := r.findControllerInfo(l, disk)
	if err != nil {
		return err
	}
	r.Set("unit_number", unit)
	if err := r.SaveDevIDs(disk, ctlr); err != nil {
		return err
	}

	// Fetch disk attachment state in config
	var attach bool
	if r.Get("attach") != nil {
		attach = r.Get("attach").(bool)
	}
	// Save disk backing settings
	b, ok := disk.Backing.(*types.VirtualDiskFlatVer2BackingInfo)
	if !ok {
		return fmt.Errorf("disk backing at %s is of an unsupported type (type %T)", r.Get("device_address").(string), disk.Backing)
	}
	r.Set("uuid", b.Uuid)
	r.Set("disk_mode", b.DiskMode)
	r.Set("write_through", b.WriteThrough)

	// Only use disk_sharing if we are on vSphere 6.0 and higher. In addition,
	// skip if the value is unset - this prevents spurious diffs during upgrade
	// situations where the VM hardware version does not actually allow disk
	// sharing. In this situation, the value will be blank, and setting it will
	// actually result in an error.
	version := viapi.ParseVersionFromClient(r.client)
	if version.Newer(viapi.VSphereVersion{Product: version.Product, Major: 6}) && b.Sharing != "" {
		r.Set("disk_sharing", b.Sharing)
	}

	if !attach {
		r.Set("thin_provisioned", b.ThinProvisioned)
		r.Set("eagerly_scrub", b.EagerlyScrub)
	}
	r.Set("datastore_id", b.Datastore.Value)

	// Disk settings
	if !attach {
		dp := &object.DatastorePath{}
		if ok := dp.FromString(b.FileName); !ok {
			return fmt.Errorf("could not parse path from filename: %s", b.FileName)
		}
		r.Set("path", dp.Path)
		r.Set("size", diskCapacityInGiB(disk))
	}

	if allocation := disk.StorageIOAllocation; allocation != nil {
		r.Set("io_limit", allocation.Limit)
		r.Set("io_reservation", allocation.Reservation)
		if shares := allocation.Shares; shares != nil {
			r.Set("io_share_level", string(shares.Level))
			r.Set("io_share_count", shares.Shares)
		}
	}
	glog.V(4).Infof("[DEBUG] %s: Read finished (key and device address may have changed)", r)
	return nil
}
*/
// diskRelocateListString pretty-prints a list of
// VirtualMachineRelocateSpecDiskLocator.
func diskRelocateListString(relocators []types.VirtualMachineRelocateSpecDiskLocator) string {
	var out []string
	for _, relocate := range relocators {
		out = append(out, diskRelocateString(relocate))
	}
	return strings.Join(out, ",")
}

// diskRelocateString prints out information from a
// VirtualMachineRelocateSpecDiskLocator in a friendly way.
//
// The format depends on whether or not a backing has been defined.
func diskRelocateString(relocate types.VirtualMachineRelocateSpecDiskLocator) string {
	key := relocate.DiskId
	var locstring string
	if backing, ok := relocate.DiskBackingInfo.(*types.VirtualDiskFlatVer2BackingInfo); ok && backing != nil {
		locstring = backing.FileName
	} else {
		locstring = relocate.Datastore.Value
	}
	return fmt.Sprintf("(%d => %s)", key, locstring)
}

// virtualDeviceListSorter is an internal type to facilitate sorting of a BaseVirtualDeviceList.
type virtualDeviceListSorter struct {
	Sort       object.VirtualDeviceList
	DeviceList object.VirtualDeviceList
}

// Len implements sort.Interface for virtualDeviceListSorter.
func (l virtualDeviceListSorter) Len() int {
	return len(l.Sort)
}

// Less helps implement sort.Interface for virtualDeviceListSorter. A
// BaseVirtualDevice is "less" than another device if its controller's bus
// number and unit number combination are earlier in the order than the other.
func (l virtualDeviceListSorter) Less(i, j int) bool {
	li := l.Sort[i]
	lj := l.Sort[j]
	liCtlr := l.DeviceList.FindByKey(li.GetVirtualDevice().ControllerKey)
	ljCtlr := l.DeviceList.FindByKey(lj.GetVirtualDevice().ControllerKey)
	if liCtlr == nil || ljCtlr == nil {
		panic(errors.New("virtualDeviceListSorter cannot be used with devices that are not assigned to a controller"))
	}
	if liCtlr.(types.BaseVirtualController).GetVirtualController().BusNumber < liCtlr.(types.BaseVirtualController).GetVirtualController().BusNumber {
		return true
	}
	liUnit := li.GetVirtualDevice().UnitNumber
	ljUnit := lj.GetVirtualDevice().UnitNumber
	if liUnit == nil || ljUnit == nil {
		panic(errors.New("virtualDeviceListSorter cannot be used with devices that do not have unit numbers set"))
	}
	return *liUnit < *ljUnit
}

// Swap helps implement sort.Interface for virtualDeviceListSorter.
func (l virtualDeviceListSorter) Swap(i, j int) {
	l.Sort[i], l.Sort[j] = l.Sort[j], l.Sort[i]
}

// virtualDiskSubresourceSorter sorts a list of disk sub-resources, based on unit number.
type virtualDiskSubresourceSorter []*v1alpha1.VMwareDisk

// Len implements sort.Interface for virtualDiskSubresourceSorter.
func (s virtualDiskSubresourceSorter) Len() int {
	return len(s)
}

// Less helps implement sort.Interface for virtualDiskSubresourceSorter.
func (s virtualDiskSubresourceSorter) Less(i, j int) bool {
	mi := s[i]
	mj := s[j]
	return mi.UnitNumber < mj.UnitNumber
}

// Swap helps implement sort.Interface for virtualDiskSubresourceSorter.
func (s virtualDiskSubresourceSorter) Swap(i, j int) {
	s[i], s[j] = s[j], s[i]
}

// SelectDisks looks for disks that Terraform is supposed to manage. count is
// the number of controllers that Terraform is managing and serves as an upper
// limit (count - 1) of the SCSI bus number for a controller that eligible
// disks need to be attached to.
func SelectDisks(l object.VirtualDeviceList, count int) object.VirtualDeviceList {
	devices := l.Select(func(device types.BaseVirtualDevice) bool {
		if disk, ok := device.(*types.VirtualDisk); ok {
			ctlr, err := findControllerForDevice(l, disk)
			if err != nil {
				glog.V(4).Infof("[DEBUG] DiskRefreshOperation: Error looking for controller for device %q: %s", l.Name(disk), err)
				return false
			}
			if sc, ok := ctlr.(types.BaseVirtualSCSIController); ok && sc.GetVirtualSCSIController().BusNumber < int32(count) {
				cd := sc.(types.BaseVirtualDevice)
				glog.V(4).Infof("[DEBUG] DiskRefreshOperation: Found controller %q for device %q", l.Name(cd), l.Name(disk))
				return true
			}
		}
		return false
	})
	return devices
}

// Update updates a vsphere_virtual_machine disk sub-resource.
func (r *DiskSubresource) Update(l object.VirtualDeviceList) ([]types.BaseVirtualDeviceConfigSpec, error) {
	glog.V(4).Infof("[DEBUG] %s: Beginning update", r)
	disk, err := r.findVirtualDisk(l, false)
	if err != nil {
		return nil, fmt.Errorf("cannot find disk device: %s", err)
	}

	/* TODO martin: can unit number change and if yes, how to track change???
	// Has the unit number changed?
	if r.HasChange("unit_number") {
		ctlr, err := r.assignDisk(l, disk)
		if err != nil {
			return nil, fmt.Errorf("cannot assign disk: %s", err)
		}
		r.SetRestart("unit_number")
		if err := r.SaveDevIDs(disk, ctlr); err != nil {
			return nil, fmt.Errorf("error saving device address: %s", err)
		}
		// A change in disk unit number forces a device key change after the
		// reconfigure. We need to keep the key in the device change spec we send
		// along, but we can reset it here safely. Set it to 0, which will send it
		// though the new device loop, but will distinguish it from newly-created
		// devices.
		r.Set("key", 0)
	}
	*/

	// We can now expand the rest of the settings.
	if err := r.expandDiskSettings(disk); err != nil {
		return nil, err
	}

	dspec, err := object.VirtualDeviceList{disk}.ConfigSpec(types.VirtualDeviceConfigSpecOperationEdit)
	if err != nil {
		return nil, err
	}
	if len(dspec) != 1 {
		return nil, fmt.Errorf("incorrect number of config spec items returned - expected 1, got %d", len(dspec))
	}
	// Clear file operation - VirtualDeviceList currently sets this to replace, which is invalid
	dspec[0].GetVirtualDeviceConfigSpec().FileOperation = ""
	glog.V(4).Infof("[DEBUG] %s: Device config operations from update: %s", r, DeviceChangeString(dspec))
	glog.V(4).Infof("[DEBUG] %s: Update complete", r)
	return dspec, nil
}

// Delete deletes a vsphere_virtual_machine disk sub-resource.
func (r *DiskSubresource) Delete(l object.VirtualDeviceList) ([]types.BaseVirtualDeviceConfigSpec, error) {
	glog.V(4).Infof("[DEBUG] %s: Beginning delete", r)
	disk, err := r.findVirtualDisk(l, false)
	if err != nil {
		return nil, fmt.Errorf("cannot find disk device: %s", err)
	}
	deleteSpec, err := object.VirtualDeviceList{disk}.ConfigSpec(types.VirtualDeviceConfigSpecOperationRemove)
	if err != nil {
		return nil, err
	}
	if len(deleteSpec) != 1 {
		return nil, fmt.Errorf("incorrect number of config spec items returned - expected 1, got %d", len(deleteSpec))
	}
	if r.disk.KeepOnRemove || r.disk.Attach {
		// Clear file operation so that the disk is kept on remove.
		deleteSpec[0].GetVirtualDeviceConfigSpec().FileOperation = ""
	}
	glog.V(4).Infof("[DEBUG] %s: Device config operations from update: %s", r, DeviceChangeString(deleteSpec))
	glog.V(4).Infof("[DEBUG] %s: Delete completed", r)
	return deleteSpec, nil
}

// Relocate produces a VirtualMachineRelocateSpecDiskLocator for this resource
// and is used for both cloning and storage vMotion.
func (r *DiskSubresource) Relocate(l object.VirtualDeviceList, clone bool) (types.VirtualMachineRelocateSpecDiskLocator, error) {
	glog.V(4).Infof("[DEBUG] %s: Starting relocate generation", r)
	disk, err := r.findVirtualDisk(l, clone)
	var relocate types.VirtualMachineRelocateSpecDiskLocator
	if err != nil {
		return relocate, fmt.Errorf("cannot find disk device: %s", err)
	}

	// Expand all of the necessary disk settings first. This ensures all backing
	// data is properly populate and updated.
	if err := r.expandDiskSettings(disk); err != nil {
		return relocate, err
	}

	relocate.DiskId = disk.Key

	// Set the datastore for the relocation
	dsID := r.GetDataString("datastore_id")
	/* TODO martin rdd ???
	if dsID == "" {
		// Default to the default datastore
		dsID = r.rdd.Get("datastore_id").(string)
	}
	*/
	ds, err := datastore.FromID(r.client, dsID)
	if err != nil {
		return relocate, err
	}
	dsref := ds.Reference()
	relocate.Datastore = dsref

	/* TODO martin rdd ???
	// Add additional backing options if we are cloning.
	if r.rdd.Id() == "" {
		glog.V(4).Infof("[DEBUG] %s: Adding additional options to relocator for cloning", r)

		backing := disk.Backing.(*types.VirtualDiskFlatVer2BackingInfo)
		backing.FileName = ds.Path("")
		backing.Datastore = &dsref
		relocate.DiskBackingInfo = backing
	}
	*/

	// Done!
	glog.V(4).Infof("[DEBUG] %s: Generated disk locator: %s", r, diskRelocateString(relocate))
	glog.V(4).Infof("[DEBUG] %s: Relocate generation complete", r)
	return relocate, nil
}

// expandDiskSettings sets appropriate fields on an existing disk - this is
// used during Create and Update to set attributes to those found in
// configuration.
func (r *DiskSubresource) expandDiskSettings(disk *types.VirtualDisk) error {
	// Backing settings
	b := disk.Backing.(*types.VirtualDiskFlatVer2BackingInfo)
	b.DiskMode = r.GetString("disk_mode")
	b.WriteThrough = r.GetBool("write_through")

	// Only use disk_sharing if we are on vSphere 6.0 and higher
	version := viapi.ParseVersionFromClient(r.client)
	if version.Newer(viapi.VSphereVersion{Product: version.Product, Major: 6}) {
		b.Sharing = r.GetString("disk_sharing")
	}

	// This settings are only set for internal disks
	if !r.disk.Attach {
		var err error
		var v interface{}
		if v, err = r.GetDataBoolWithVeto("thin_provisioned"); err != nil {
			return err
		}
		b.ThinProvisioned = structure.BoolPtr(v.(bool))

		if v, err = r.GetDataBoolWithVeto("eagerly_scrub"); err != nil {
			return err
		}
		b.EagerlyScrub = structure.BoolPtr(v.(bool))

		/* martin: change detection for disk size???
		// Disk settings
		os, ns := r.GetChange("size")
		if os.(int) > ns.(int) {
			return fmt.Errorf("virtual disks cannot be shrunk")
		}
		*/
		disk.CapacityInBytes = structure.GiBToByte(r.disk.Size)
		disk.CapacityInKB = disk.CapacityInBytes / 1024
	}

	alloc := &types.StorageIOAllocationInfo{
		Limit:       structure.Int64Ptr(int64(r.GetInt("io_limit"))),
		Reservation: structure.Int32Ptr(int32(r.GetInt("io_reservation"))),
		Shares: &types.SharesInfo{
			Shares: int32(r.GetInt("io_share_count")),
			Level:  types.SharesLevel(r.GetString("io_share_level")),
		},
	}
	disk.StorageIOAllocation = alloc

	return nil
}

// createDisk performs all of the logic for a base virtual disk creation.
func (r *DiskSubresource) createDisk(l object.VirtualDeviceList) (*types.VirtualDisk, error) {
	disk := new(types.VirtualDisk)
	disk.Backing = new(types.VirtualDiskFlatVer2BackingInfo)

	if err := r.assignBackingInfo(disk); err != nil {
		return nil, err
	}

	// Set a new device key for this device
	disk.Key = l.NewKey()
	return disk, nil
}

func (r *DiskSubresource) assignBackingInfo(disk *types.VirtualDisk) error {
	dsID := r.GetString("datastore_id")
	if dsID == "" || dsID == diskDatastoreComputedName {
		/* TODO martin: rdd needed ???
		// Default to the default datastore
		dsID = r.rdd.Get("datastore_id").(string)
		*/
	}

	ds, err := datastore.FromID(r.client, dsID)
	if err != nil {
		return err
	}
	dsref := ds.Reference()

	var diskName string
	if r.disk.Attach {
		// No path interpolation is performed any more for attached disks - the
		// provided path must be the full path to the virtual disk you want to
		// attach.
		diskName = r.GetString("path")
	}

	backing := disk.Backing.(*types.VirtualDiskFlatVer2BackingInfo)
	backing.FileName = ds.Path(diskName)
	backing.Datastore = &dsref

	return nil
}

// assignDisk takes a unit number and assigns it correctly to a controller on
// the SCSI bus. An error is returned if the assigned unit number is taken.
func (r *DiskSubresource) assignDisk(l object.VirtualDeviceList, disk *types.VirtualDisk) (types.BaseVirtualController, error) {
	number := r.disk.UnitNumber
	// Figure out the bus number, and look up the SCSI controller that matches
	// that. You can attach 15 disks to a SCSI controller, and we allow a maximum
	// of 30 devices.
	bus := number / 15
	// Also determine the unit number on that controller.
	unit := int32(math.Mod(float64(number), 15))

	// Find the controller.
	ctlr, err := r.ControllerForCreateUpdate(l, SubresourceControllerTypeSCSI, bus)
	if err != nil {
		return nil, err
	}

	// Build the unit list.
	units := make([]bool, 16)
	// Reserve the SCSI unit number
	scsiUnit := ctlr.(types.BaseVirtualSCSIController).GetVirtualSCSIController().ScsiCtlrUnitNumber
	units[scsiUnit] = true

	ckey := ctlr.GetVirtualController().Key

	for _, device := range l {
		d := device.GetVirtualDevice()
		if d.ControllerKey != ckey || d.UnitNumber == nil {
			continue
		}
		units[*d.UnitNumber] = true
	}

	// We now have a valid list of units. If we need to, shift up the desired
	// unit number so it's not taking the unit of the controller itself.
	if unit >= scsiUnit {
		unit++
	}

	if units[unit] {
		return nil, fmt.Errorf("unit number %d on SCSI bus %d is in use", unit, bus)
	}

	// If we made it this far, we are good to go!
	disk.ControllerKey = ctlr.GetVirtualController().Key
	disk.UnitNumber = &unit
	return ctlr, nil
}

// findControllerInfo determines the normalized unit number for the disk device
// based on the SCSI controller and unit number it's connected to. The
// controller is also returned.
func (r *Subresource) findControllerInfo(l object.VirtualDeviceList, disk *types.VirtualDisk) (int, types.BaseVirtualController, error) {
	ctlr := l.FindByKey(disk.ControllerKey)
	if ctlr == nil {
		return -1, nil, fmt.Errorf("could not find disk controller with key %d for disk key %d", disk.ControllerKey, disk.Key)
	}
	if disk.UnitNumber == nil {
		return -1, nil, fmt.Errorf("unit number on disk key %d is unset", disk.Key)
	}
	sc, ok := ctlr.(types.BaseVirtualSCSIController)
	if !ok {
		return -1, nil, fmt.Errorf("controller at key %d is not a SCSI controller (actual: %T)", ctlr.GetVirtualDevice().Key, ctlr)
	}
	unit := *disk.UnitNumber
	if unit > sc.GetVirtualSCSIController().ScsiCtlrUnitNumber {
		unit--
	}
	unit = unit + 15*sc.GetVirtualSCSIController().BusNumber
	return int(unit), ctlr.(types.BaseVirtualController), nil
}

// findVirtualDisk locates a virtual disk by it UUID, or by its device address
// if UUID is missing.
//
// The device address search is only used if fallback is true - this is so that
// we can distinguish situations where it should be used, such as a read,
// versus situations where it should never be used, such as an update or
// delete.
func (r *DiskSubresource) findVirtualDisk(l object.VirtualDeviceList, fallback bool) (*types.VirtualDisk, error) {
	device, err := r.findVirtualDiskByUUIDOrAddress(l, fallback)
	if err != nil {
		return nil, err
	}
	return device.(*types.VirtualDisk), nil
}

func (r *DiskSubresource) findVirtualDiskByUUIDOrAddress(l object.VirtualDeviceList, fallback bool) (types.BaseVirtualDevice, error) {
	uuid := r.GetString("uuid")
	switch {
	case uuid == "" && fallback:
		return r.FindVirtualDevice(l)
	case uuid == "" && !fallback:
		return nil, errors.New("disk UUID is missing")
	}
	devices := l.Select(func(device types.BaseVirtualDevice) bool {
		return diskUUIDMatch(device, uuid)
	})
	switch {
	case len(devices) < 1:
		return nil, fmt.Errorf("virtual disk with UUID %s not found", uuid)
	case len(devices) > 1:
		// This is an edge/should never happen case
		return nil, fmt.Errorf("multiple virtual disks with UUID %s found", uuid)
	}
	return devices[0], nil
}

func diskUUIDMatch(device types.BaseVirtualDevice, uuid string) bool {
	disk, ok := device.(*types.VirtualDisk)
	if !ok {
		return false
	}
	backing, ok := disk.Backing.(*types.VirtualDiskFlatVer2BackingInfo)
	if !ok {
		return false
	}
	if backing.Uuid != uuid {
		return false
	}
	return true
}

// diskCapacityInGiB reports the supplied disk's capacity, by first checking
// CapacityInBytes, and then falling back to CapacityInKB if that value is
// unavailable. This helps correct some situations where the former value's
// data gets cleared, which seems to happen on upgrades.
func diskCapacityInGiB(disk *types.VirtualDisk) int {
	if disk.CapacityInBytes > 0 {
		return int(structure.ByteToGiB(disk.CapacityInBytes).(int64))
	}
	glog.V(4).Infof("[DEBUG] diskCapacityInGiB: capacityInBytes missing for for %s, falling back to capacityInKB",
		object.VirtualDeviceList{}.Name(disk),
	)
	return int(structure.ByteToGiB(disk.CapacityInKB * 1024).(int64))
}

// subresourceDiskListString takes a list of sub-resources and pretty-prints the
// key and device address.
func subresourceDiskListString(data []*v1alpha1.VMwareDisk) string {
	var strs []string
	for _, disk := range data {
		label := "<nil>"
		if disk != nil {
			if disk.Label == "" {
				label = "<new device>"
			}
			label = disk.Label
		}
		strs = append(strs, label)
	}
	return strings.Join(strs, ",")
}

func (r *DiskSubresource) GetBool(key string) *bool {
	_, schemaFound := r.schema[key]
	if !schemaFound {
		panic("Undefined DiskSubresource key " + key)
	}
	value, ok := r.data[key]
	if !ok {
		// default value ignored, rely on backend service
		return nil
	}
	b := value == "true"
	return &b
}

func (r *DiskSubresource) GetString(key string) string {
	schema, schemaFound := r.schema[key]
	if !schemaFound {
		panic("Undefined DiskSubresource key " + key)
	}
	value, ok := r.data[key]
	if !ok {
		if schema.Default != nil {
			return schema.Default.(string)
		}
		return ""
	}
	return value
}

func (r *DiskSubresource) GetInt(key string) int {
	schema, schemaFound := r.schema[key]
	if !schemaFound {
		panic("Undefined DiskSubresource key " + key)
	}

	value, ok := r.data[key]
	if !ok {
		if schema.Default != nil {
			return schema.Default.(int)
		}
		return 0
	}
	i, err := strconv.Atoi(value)
	if err != nil {
		glog.Errorf("Invalid value %s for DiskSubresource key %s: %s", value, key, err)
		return 0
	}
	return i
}
