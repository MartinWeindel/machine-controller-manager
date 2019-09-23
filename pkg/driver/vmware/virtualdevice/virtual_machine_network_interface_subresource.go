package virtualdevice

import (
	"context"
	"fmt"
	"github.com/golang/glog"
	"reflect"
	"strconv"
	"strings"

	"github.com/gardener/machine-controller-manager/pkg/apis/machine/v1alpha1"
	"github.com/gardener/machine-controller-manager/pkg/driver/vmware/helper/dvportgroup"
	"github.com/gardener/machine-controller-manager/pkg/driver/vmware/helper/network"
	"github.com/gardener/machine-controller-manager/pkg/driver/vmware/helper/nsx"
	"github.com/gardener/machine-controller-manager/pkg/driver/vmware/helper/provider"
	"github.com/gardener/machine-controller-manager/pkg/driver/vmware/helper/structure"
	"github.com/gardener/machine-controller-manager/pkg/driver/vmware/helper/viapi"
	"github.com/gardener/machine-controller-manager/pkg/driver/vmware/schema"
	"github.com/gardener/machine-controller-manager/pkg/driver/vmware/validation"
	"github.com/mitchellh/copystructure"
	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25/types"
)

// networkInterfacePciDeviceOffset defines the PCI offset for virtual NICs on a vSphere PCI bus.
const networkInterfacePciDeviceOffset = 7

const (
	networkInterfaceSubresourceTypeE1000   = "e1000"
	networkInterfaceSubresourceTypeE1000e  = "e1000e"
	networkInterfaceSubresourceTypePCNet32 = "pcnet32"
	networkInterfaceSubresourceTypeSriov   = "sriov"
	networkInterfaceSubresourceTypeVmxnet2 = "vmxnet2"
	networkInterfaceSubresourceTypeVmxnet3 = "vmxnet3"
	networkInterfaceSubresourceTypeUnknown = "unknown"
)

var networkInterfaceSubresourceTypeAllowedValues = []string{
	networkInterfaceSubresourceTypeE1000,
	networkInterfaceSubresourceTypeE1000e,
	networkInterfaceSubresourceTypeVmxnet3,
}

var networkInterfaceSubresourceMACAddressTypeAllowedValues = []string{
	string(types.VirtualEthernetCardMacTypeManual),
}

// NetworkInterfaceSubresourceSchema returns the schema for the disk
// sub-resource.
func NetworkInterfaceSubresourceSchema() map[string]*schema.Schema {
	s := map[string]*schema.Schema{
		// VirtualEthernetCardResourceAllocation
		"bandwidth_limit": {
			Type:         schema.TypeInt,
			Optional:     true,
			Default:      -1,
			Description:  "The upper bandwidth limit of this network interface, in Mbits/sec.",
			ValidateFunc: validation.IntAtLeast(-1),
		},
		"bandwidth_reservation": {
			Type:         schema.TypeInt,
			Optional:     true,
			Default:      0,
			Description:  "The bandwidth reservation of this network interface, in Mbits/sec.",
			ValidateFunc: validation.IntAtLeast(0),
		},
		"bandwidth_share_level": {
			Type:         schema.TypeString,
			Optional:     true,
			Default:      string(types.SharesLevelNormal),
			Description:  "The bandwidth share allocation level for this interface. Can be one of low, normal, high, or custom.",
			ValidateFunc: validation.StringInSlice(sharesLevelAllowedValues, false),
		},
		"bandwidth_share_count": {
			Type:         schema.TypeInt,
			Optional:     true,
			Computed:     true,
			Description:  "The share count for this network interface when the share level is custom.",
			ValidateFunc: validation.IntAtLeast(0),
		},

		// VirtualEthernetCard and friends
		"adapter_type": {
			Type:         schema.TypeString,
			Optional:     true,
			Default:      networkInterfaceSubresourceTypeVmxnet3,
			Description:  "The controller type. Can be one of e1000, e1000e, or vmxnet3.",
			ValidateFunc: validation.StringInSlice(networkInterfaceSubresourceTypeAllowedValues, false),
		},
		"use_static_mac": {
			Type:        schema.TypeBool,
			Optional:    true,
			Description: "If true, the mac_address field is treated as a static MAC address and set accordingly.",
		},
		"mac_address": {
			Type:        schema.TypeString,
			Optional:    true,
			Computed:    true,
			Description: "The MAC address of this network interface. Can only be manually set if use_static_mac is true.",
		},
	}
	structure.MergeSchema(s, subresourceSchema())
	return s
}

// NetworkInterfaceSubresource represents a vsphere_virtual_machine
// network_interface sub-resource, with a complex device lifecycle.
type NetworkInterfaceSubresource struct {
	*Subresource
	NetworkId string
}

// NewNetworkInterfaceSubresource returns a network_interface subresource
// populated with all of the necessary fields.
func NewNetworkInterfaceSubresource(client *govmomi.Client, data map[string]string, idx int) *NetworkInterfaceSubresource {
	sr := &NetworkInterfaceSubresource{
		Subresource: &Subresource{
			schema: NetworkInterfaceSubresourceSchema(),
			client: client,
			srtype: subresourceTypeNetworkInterface,
			data:   data,
		},
	}
	sr.Index = idx
	return sr
}

// NetworkInterfacePostCloneOperation normalizes the network interfaces on a
// freshly-cloned virtual machine and outputs any necessary device change
// operations. It also sets the state in advance of the post-create read.
//
// This differs from a regular apply operation in that a configuration is
// already present, but we don't have any existing state, which the standard
// virtual device operations rely pretty heavily on.
func NetworkInterfacePostCloneOperation(interfaces []*v1alpha1.VMwareNetworkInterface, c *govmomi.Client, l object.VirtualDeviceList) (object.VirtualDeviceList, []types.BaseVirtualDeviceConfigSpec, error) {
	glog.V(4).Infof("[DEBUG] NetworkInterfacePostCloneOperation: Looking for post-clone device changes")
	devices := l.Select(func(device types.BaseVirtualDevice) bool {
		if _, ok := device.(types.BaseVirtualEthernetCard); ok {
			return true
		}
		return false
	})
	glog.V(4).Infof("[DEBUG] NetworkInterfacePostCloneOperation: Network devices located: %s", DeviceListString(devices))
	curSet := interfaces
	glog.V(4).Infof("[DEBUG] NetworkInterfacePostCloneOperation: Current resource set from configuration: %s", subresourceNetworkInterfaceListString(curSet))
	urange, err := nicUnitRange(devices)
	if err != nil {
		return nil, nil, fmt.Errorf("error calculating network device range: %s", err)
	}
	srcSet := make([]map[string]string, urange)
	glog.V(4).Infof("[DEBUG] NetworkInterfacePostCloneOperation: Layout from source: %d devices over a %d unit range", len(devices), urange)

	// Populate the source set as if the devices were orphaned. This give us a
	// base to diff off of.
	glog.V(4).Infof("[DEBUG] NetworkInterfacePostCloneOperation: Reading existing devices")
	for n, device := range devices {
		m := make(map[string]string)
		vd := device.GetVirtualDevice()
		ctlr := l.FindByKey(vd.ControllerKey)
		if ctlr == nil {
			return nil, nil, fmt.Errorf("could not find controller with key %d", vd.Key)
		}
		m["key"] = fmt.Sprintf("%d", int(vd.Key))
		var err error
		m["device_address"], err = computeDevAddr(vd, ctlr.(types.BaseVirtualController))
		if err != nil {
			return nil, nil, fmt.Errorf("error computing device address: %s", err)
		}
		r := NewNetworkInterfaceSubresource(c, m, n)
		if err := r.Read(l); err != nil {
			return nil, nil, fmt.Errorf("%s: %s", r.Addr(), err)
		}
		_, _, idx, err := splitDevAddr(r.GetString("device_address"))
		if err != nil {
			return nil, nil, fmt.Errorf("%s: error parsing device address: %s", r, err)
		}
		srcSet[idx-networkInterfacePciDeviceOffset] = r.data
	}

	// Now go over our current set, kind of treating it like an apply:
	//
	// * No source data at the index is a create
	// * Source data at the index is an update if it has changed
	// * Data at the source with the same data after patching config data is a
	// no-op, but we still push the device's state
	var spec []types.BaseVirtualDeviceConfigSpec
	var updates []map[string]string
	for i, ci := range curSet {
		if i > len(srcSet)-1 || srcSet[i] == nil {
			// New device
			r := NewNetworkInterfaceSubresource(c, ci.Properties, i)
			cspec, err := r.Create(l)
			if err != nil {
				return nil, nil, fmt.Errorf("%s: %s", r.Addr(), err)
			}
			l = applyDeviceChange(l, cspec)
			spec = append(spec, cspec...)
			updates = append(updates, r.data)
			continue
		}
		sm := srcSet[i]
		nc, err := copystructure.Copy(sm)
		if err != nil {
			return nil, nil, fmt.Errorf("error copying source network interface state data at index %d: %s", i, err)
		}
		nm := nc.(map[string]string)
		for k, v := range ci.Properties {
			// Skip key and device_address here
			switch k {
			case "key", "device_address":
				continue
			}
			nm[k] = v
		}
		r := NewNetworkInterfaceSubresource(c, nm, i)
		if !reflect.DeepEqual(sm, nm) {
			// Update
			cspec, err := r.Update(l)
			if err != nil {
				return nil, nil, fmt.Errorf("%s: %s", r.Addr(), err)
			}
			l = applyDeviceChange(l, cspec)
			spec = append(spec, cspec...)
		}
		updates = append(updates, r.data)
	}

	// Any other device past the end of the network devices listed in config needs to be removed.
	if len(curSet) < len(srcSet) {
		for i, si := range srcSet[len(curSet):] {
			r := NewNetworkInterfaceSubresource(c, si, i+len(curSet))
			dspec, err := r.Delete(l)
			if err != nil {
				return nil, nil, fmt.Errorf("%s: %s", r.Addr(), err)
			}
			l = applyDeviceChange(l, dspec)
			spec = append(spec, dspec...)
		}
	}

	/* TODO martin: need to store updates?
	glog.V(4).Infof("[DEBUG] NetworkInterfacePostCloneOperation: Post-clone final resource list: %s", subresourceListString(updates))
	// We are now done! Return the updated device list and config spec. Save updates as well.
	if err := d.Set(subresourceTypeNetworkInterface, updates); err != nil {
		return nil, nil, err
	}
	*/
	glog.V(4).Infof("[DEBUG] NetworkInterfacePostCloneOperation: Device list at end of operation: %s", DeviceListString(l))
	glog.V(4).Infof("[DEBUG] NetworkInterfacePostCloneOperation: Device config operations from post-clone: %s", DeviceChangeString(spec))
	glog.V(4).Infof("[DEBUG] NetworkInterfacePostCloneOperation: Operation complete, returning updated spec")
	return l, spec, nil
}

// baseVirtualEthernetCardToBaseVirtualDevice converts a
// BaseVirtualEthernetCard value into a BaseVirtualDevice.
func baseVirtualEthernetCardToBaseVirtualDevice(v types.BaseVirtualEthernetCard) types.BaseVirtualDevice {
	switch t := v.(type) {
	case *types.VirtualE1000:
		return types.BaseVirtualDevice(t)
	case *types.VirtualE1000e:
		return types.BaseVirtualDevice(t)
	case *types.VirtualPCNet32:
		return types.BaseVirtualDevice(t)
	case *types.VirtualSriovEthernetCard:
		return types.BaseVirtualDevice(t)
	case *types.VirtualVmxnet2:
		return types.BaseVirtualDevice(t)
	case *types.VirtualVmxnet3:
		return types.BaseVirtualDevice(t)
	}
	panic(fmt.Errorf("unknown ethernet card type %T", v))
}

// baseVirtualDeviceToBaseVirtualEthernetCard converts a BaseVirtualDevice
// value into a BaseVirtualEthernetCard.
func baseVirtualDeviceToBaseVirtualEthernetCard(v types.BaseVirtualDevice) (types.BaseVirtualEthernetCard, error) {
	if bve, ok := v.(types.BaseVirtualEthernetCard); ok {
		return bve, nil
	}
	return nil, fmt.Errorf("device is not a network device (%T)", v)
}

// virtualEthernetCardString prints a string representation of the ethernet device passed in.
func virtualEthernetCardString(d types.BaseVirtualEthernetCard) string {
	switch d.(type) {
	case *types.VirtualE1000:
		return networkInterfaceSubresourceTypeE1000
	case *types.VirtualE1000e:
		return networkInterfaceSubresourceTypeE1000e
	case *types.VirtualPCNet32:
		return networkInterfaceSubresourceTypePCNet32
	case *types.VirtualSriovEthernetCard:
		return networkInterfaceSubresourceTypeSriov
	case *types.VirtualVmxnet2:
		return networkInterfaceSubresourceTypeVmxnet2
	case *types.VirtualVmxnet3:
		return networkInterfaceSubresourceTypeVmxnet3
	}
	return networkInterfaceSubresourceTypeUnknown
}

// Create creates a vsphere_virtual_machine network_interface sub-resource.
func (r *NetworkInterfaceSubresource) Create(l object.VirtualDeviceList) ([]types.BaseVirtualDeviceConfigSpec, error) {
	glog.V(4).Infof("[DEBUG] %s: Running create", r)
	var spec []types.BaseVirtualDeviceConfigSpec
	ctlr, err := r.ControllerForCreateUpdate(l, SubresourceControllerTypePCI, 0)
	if err != nil {
		return nil, err
	}

	// govmomi has helpers that allow the easy fetching of a network's backing
	// info, once we actually know what that backing is. Set all of that stuff up
	// now.
	net, err := network.FromID(r.client, r.GetString("network_id"))
	if err != nil {
		return nil, err
	}
	bctx, bcancel := context.WithTimeout(context.Background(), provider.DefaultAPITimeout)
	defer bcancel()
	backing, err := net.EthernetCardBackingInfo(bctx)
	if err != nil {
		return nil, err
	}
	device, err := l.CreateEthernetCard(r.GetString("adapter_type"), backing)
	if err != nil {
		return nil, err
	}

	// CreateEthernetCard does not attach stuff, however, assuming that you will
	// let vSphere take care of the attachment and what not, as there is usually
	// only one PCI device per virtual machine and their tools don't really care
	// about state. Terraform does though, so we need to not only set but also
	// track that stuff.
	if err := r.assignEthernetCard(l, device, ctlr); err != nil {
		return nil, err
	}
	// Ensure the device starts connected
	l.Connect(device)

	// Set base-level card bits now
	card := device.(types.BaseVirtualEthernetCard).GetVirtualEthernetCard()
	card.Key = l.NewKey()

	// Set the rest of the settings here.
	if r.GetBool("use_static_mac") {
		card.AddressType = string(types.VirtualEthernetCardMacTypeManual)
		card.MacAddress = r.GetString("mac_address")
	}
	version := viapi.ParseVersionFromClient(r.client)
	if version.Newer(viapi.VSphereVersion{Product: version.Product, Major: 6}) {
		alloc := &types.VirtualEthernetCardResourceAllocation{
			Limit:       structure.Int64Ptr(int64(r.GetInt("bandwidth_limit"))),
			Reservation: structure.Int64Ptr(int64(r.GetInt("bandwidth_reservation"))),
			Share: types.SharesInfo{
				Shares: int32(r.GetInt("bandwidth_share_count")),
				Level:  types.SharesLevel(r.GetString("bandwidth_share_level")),
			},
		}
		card.ResourceAllocation = alloc
	}

	// Done here. Save ID, push the device to the new device list and return.
	if err := r.SaveDevIDs(device, ctlr); err != nil {
		return nil, err
	}
	dspec, err := object.VirtualDeviceList{device}.ConfigSpec(types.VirtualDeviceConfigSpecOperationAdd)
	if err != nil {
		return nil, err
	}
	spec = append(spec, dspec...)
	glog.V(4).Infof("[DEBUG] %s: Device config operations from create: %s", r, DeviceChangeString(spec))
	glog.V(4).Infof("[DEBUG] %s: Create finished", r)
	return spec, nil
}

// Read reads a vsphere_virtual_machine network_interface sub-resource.
func (r *NetworkInterfaceSubresource) Read(l object.VirtualDeviceList) error {
	glog.V(4).Infof("[DEBUG] %s: Reading state", r)
	vd, err := r.FindVirtualDevice(l)
	if err != nil {
		return fmt.Errorf("cannot find network device: %s", err)
	}
	device, err := baseVirtualDeviceToBaseVirtualEthernetCard(vd)
	if err != nil {
		return err
	}

	// Determine the interface type, and set the field appropriately. As a fallback,
	// we actually set adapter_type here to "unknown" if we don't support the NIC
	// type, as we can determine all of the other settings without having to
	// worry about the adapter type, and on update, the adapter type will be
	// rectified by removing the existing NIC and replacing it with a new one.
	r.SetString("adapter_type", virtualEthernetCardString(device))

	// The rest of the information we need to get by reading the attributes off
	// the base card object.
	card := device.GetVirtualEthernetCard()

	// Determine the network
	var netID string
	switch backing := card.Backing.(type) {
	case *types.VirtualEthernetCardNetworkBackingInfo:
		if backing.Network == nil {
			return fmt.Errorf("could not determine network information from NIC backing")
		}
		netID = backing.Network.Value
	case *types.VirtualEthernetCardOpaqueNetworkBackingInfo:
		onet, err := nsx.OpaqueNetworkFromNetworkID(r.client, backing.OpaqueNetworkId)
		if err != nil {
			return err
		}
		netID = onet.Reference().Value
	case *types.VirtualEthernetCardDistributedVirtualPortBackingInfo:
		pg, err := dvportgroup.FromKey(r.client, backing.Port.SwitchUuid, backing.Port.PortgroupKey)
		if err != nil {
			if strings.Contains(err.Error(), "The object or item referred to could not be found") {
				netID = ""
			} else {
				return err
			}
		} else {
			netID = pg.Reference().Value
		}
	default:
		return fmt.Errorf("unknown network interface backing %T", card.Backing)
	}
	r.SetString("network_id", netID)

	r.SetBool("use_static_mac", card.AddressType == string(types.VirtualEthernetCardMacTypeManual))
	r.SetString("mac_address", card.MacAddress)

	version := viapi.ParseVersionFromClient(r.client)
	if version.Newer(viapi.VSphereVersion{Product: version.Product, Major: 6}) {
		if card.ResourceAllocation != nil {
			r.SetInt64Ptr("bandwidth_limit", card.ResourceAllocation.Limit)
			r.SetInt64Ptr("bandwidth_reservation", card.ResourceAllocation.Reservation)
			r.SetInt("bandwidth_share_count", int(card.ResourceAllocation.Share.Shares))
			r.SetString("bandwidth_share_level", string(card.ResourceAllocation.Share.Level))
		}
	}

	// Save the device key and address data
	ctlr, err := findControllerForDevice(l, vd)
	if err != nil {
		return err
	}
	if err := r.SaveDevIDs(vd, ctlr); err != nil {
		return err
	}
	glog.V(4).Infof("[DEBUG] %s: Read finished (key and device address may have changed)", r)
	return nil
}

// Update updates a vsphere_virtual_machine network_interface sub-resource.
func (r *NetworkInterfaceSubresource) Update(l object.VirtualDeviceList) ([]types.BaseVirtualDeviceConfigSpec, error) {
	glog.V(4).Infof("[DEBUG] %s: Beginning update", r)
	vd, err := r.FindVirtualDevice(l)
	if err != nil {
		return nil, fmt.Errorf("cannot find network device: %s", err)
	}
	device, err := baseVirtualDeviceToBaseVirtualEthernetCard(vd)
	if err != nil {
		return nil, err
	}

	// We maintain the final update spec in place, versus just the simple device
	// list, to support deletion of virtual devices so that they can replaced by
	// ones with different device types.
	var spec []types.BaseVirtualDeviceConfigSpec

	card := device.GetVirtualEthernetCard()

	// Has the backing changed?
	//if r.HasChange("network_id") {
	net, err := network.FromID(r.client, r.GetString("network_id"))
	if err != nil {
		return nil, err
	}
	bctx, bcancel := context.WithTimeout(context.Background(), provider.DefaultAPITimeout)
	defer bcancel()
	backing, err := net.EthernetCardBackingInfo(bctx)
	if err != nil {
		return nil, err
	}
	card.Backing = backing

	//if r.HasChange("use_static_mac") {
	if r.GetBool("use_static_mac") {
		card.AddressType = string(types.VirtualEthernetCardMacTypeManual)
		card.MacAddress = r.GetString("mac_address")
	} else {
		// If we've gone from a static MAC address to a auto-generated one, we need
		// to check what address type we need to set things to.
		if r.client.ServiceContent.About.ApiType != "VirtualCenter" {
			// ESXi - type is "generated"
			card.AddressType = string(types.VirtualEthernetCardMacTypeGenerated)
		} else {
			// vCenter - type is "assigned"
			card.AddressType = string(types.VirtualEthernetCardMacTypeAssigned)
		}
		card.MacAddress = ""
	}

	version := viapi.ParseVersionFromClient(r.client)
	if version.Newer(viapi.VSphereVersion{Product: version.Product, Major: 6}) {
		alloc := &types.VirtualEthernetCardResourceAllocation{
			Limit:       structure.Int64Ptr(int64(r.GetInt("bandwidth_limit"))),
			Reservation: structure.Int64Ptr(int64(r.GetInt("bandwidth_reservation"))),
			Share: types.SharesInfo{
				Shares: int32(r.GetInt("bandwidth_share_count")),
				Level:  types.SharesLevel(r.GetString("bandwidth_share_level")),
			},
		}
		card.ResourceAllocation = alloc
	}

	var op types.VirtualDeviceConfigSpecOperation
	if card.Key < 0 {
		// Negative key means that we are re-creating this device
		op = types.VirtualDeviceConfigSpecOperationAdd
	} else {
		op = types.VirtualDeviceConfigSpecOperationEdit
	}

	bvd := baseVirtualEthernetCardToBaseVirtualDevice(device)
	uspec, err := object.VirtualDeviceList{bvd}.ConfigSpec(op)
	if err != nil {
		return nil, err
	}
	spec = append(spec, uspec...)
	glog.V(4).Infof("[DEBUG] %s: Device config operations from update: %s", r, DeviceChangeString(spec))
	glog.V(4).Infof("[DEBUG] %s: Update complete", r)
	return spec, nil
}

// Delete deletes a vsphere_virtual_machine network_interface sub-resource.
func (r *NetworkInterfaceSubresource) Delete(l object.VirtualDeviceList) ([]types.BaseVirtualDeviceConfigSpec, error) {
	glog.V(4).Infof("[DEBUG] %s: Beginning delete", r)
	vd, err := r.FindVirtualDevice(l)
	if err != nil {
		return nil, fmt.Errorf("cannot find network device: %s", err)
	}
	device, err := baseVirtualDeviceToBaseVirtualEthernetCard(vd)
	if err != nil {
		return nil, err
	}

	bvd := baseVirtualEthernetCardToBaseVirtualDevice(device)
	spec, err := object.VirtualDeviceList{bvd}.ConfigSpec(types.VirtualDeviceConfigSpecOperationRemove)
	if err != nil {
		return nil, err
	}
	glog.V(4).Infof("[DEBUG] %s: Device config operations from update: %s", r, DeviceChangeString(spec))
	glog.V(4).Infof("[DEBUG] %s: Delete completed", r)
	return spec, nil
}

// assignEthernetCard is a subset of the logic that goes into AssignController
// right now but with an unit offset of 7. This is based on what we have
// observed on vSphere in terms of reserved PCI unit numbers (the first NIC
// automatically gets re-assigned to unit number 7 if it's not that already.)
func (r *NetworkInterfaceSubresource) assignEthernetCard(l object.VirtualDeviceList, device types.BaseVirtualDevice, c types.BaseVirtualController) error {
	// The PCI device offset. This seems to be where vSphere starts assigning
	// virtual NICs on the PCI controller.
	pciDeviceOffset := int32(networkInterfacePciDeviceOffset)

	// The first part of this is basically the private newUnitNumber function
	// from VirtualDeviceList, with a maximum unit count of 10. This basically
	// means that no more than 10 virtual NICs can be assigned right now, which
	// hopefully should be plenty.
	units := make([]bool, 10)

	ckey := c.GetVirtualController().Key

	for _, device := range l {
		d := device.GetVirtualDevice()
		if d.ControllerKey != ckey || d.UnitNumber == nil || *d.UnitNumber < pciDeviceOffset || *d.UnitNumber >= pciDeviceOffset+10 {
			continue
		}
		units[*d.UnitNumber-pciDeviceOffset] = true
	}

	// Now that we know which units are used, we can pick one
	newUnit := int32(r.Index) + pciDeviceOffset
	if units[newUnit-pciDeviceOffset] {
		return fmt.Errorf("device unit at %d is currently in use on the PCI bus", newUnit)
	}

	d := device.GetVirtualDevice()
	d.ControllerKey = c.GetVirtualController().Key
	d.UnitNumber = &newUnit
	if d.Key == 0 {
		d.Key = -1
	}
	return nil
}

// nicUnitRange calculates a range of units given a certain VirtualDeviceList,
// which should be network interfaces.  It's used in network interface refresh
// logic to determine how many subresources may end up in state.
func nicUnitRange(l object.VirtualDeviceList) (int, error) {
	// No NICs means no range
	if len(l) < 1 {
		return 0, nil
	}

	high := int32(networkInterfacePciDeviceOffset)

	for _, v := range l {
		d := v.GetVirtualDevice()
		if d.UnitNumber == nil {
			return 0, fmt.Errorf("device at key %d has no unit number", d.Key)
		}
		if *d.UnitNumber > high {
			high = *d.UnitNumber
		}
	}
	return int(high - networkInterfacePciDeviceOffset + 1), nil
}

// subresourceDiskListString takes a list of sub-resources and pretty-prints the
// key and device address.
func subresourceNetworkInterfaceListString(data []*v1alpha1.VMwareNetworkInterface) string {
	var strs []string
	for _, ni := range data {
		label := "<nil>"
		if ni != nil {
			label = ni.Properties["network_id"]
		}
		strs = append(strs, label)
	}
	return strings.Join(strs, ",")
}

func (r *NetworkInterfaceSubresource) GetBool(key string) bool {
	_, schemaFound := r.schema[key]
	if !schemaFound {
		panic("Undefined NetworkInterfaceSubresource schema key " + key)
	}
	value, ok := r.data[key]
	if !ok {
		return false
	}
	return value == "true"
}

func (r *NetworkInterfaceSubresource) GetString(key string) string {
	schema, schemaFound := r.schema[key]
	if !schemaFound {
		panic("Undefined NetworkInterfaceSubresource schema key " + key)
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

func (r *NetworkInterfaceSubresource) GetInt(key string) int {
	schema, schemaFound := r.schema[key]
	if !schemaFound {
		panic("Undefined NetworkInterfaceSubresource schema key " + key)
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
		glog.Errorf("Invalid value %s for NetworkInterfaceSubresource schema key %s: %s", value, key, err)
		return 0
	}
	return i
}

func (r *NetworkInterfaceSubresource) SetBool(key string, value bool) {
	_, schemaFound := r.schema[key]
	if !schemaFound {
		panic("Undefined NetworkInterfaceSubresource schema key " + key)
	}
	r.data[key] = fmt.Sprintf("%t", value)
}

func (r *NetworkInterfaceSubresource) SetString(key string, value string) {
	_, schemaFound := r.schema[key]
	if !schemaFound {
		panic("Undefined NetworkInterfaceSubresource schema key " + key)
	}
	r.data[key] = value
}

func (r *NetworkInterfaceSubresource) SetInt(key string, value int) {
	_, schemaFound := r.schema[key]
	if !schemaFound {
		panic("Undefined NetworkInterfaceSubresource schema key " + key)
	}

	r.data[key] = fmt.Sprintf("%d", value)
}

func (r *NetworkInterfaceSubresource) SetInt64Ptr(key string, pvalue *int64) {
	var value int = 0
	if pvalue != nil {
		value = int(*pvalue)
	}
	r.SetInt(key, value)
}
