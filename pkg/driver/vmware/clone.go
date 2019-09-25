package vmware

import (
	"context"
	"flag"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/gardener/machine-controller-manager/pkg/apis/machine/v1alpha1"
	"github.com/gardener/machine-controller-manager/pkg/driver/vmware/flags"
	"github.com/golang/glog"
	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/property"
	"github.com/vmware/govmomi/vim25"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"
)

const defaultAPITimeout = time.Minute * 5

type Clone struct {
	name string

	NetworkFlag *flags.NetworkFlag

	Client         *vim25.Client
	Cluster        *object.ClusterComputeResource
	Datacenter     *object.Datacenter
	Datastore      *object.Datastore
	StoragePod     *object.StoragePod
	ResourcePool   *object.ResourcePool
	HostSystem     *object.HostSystem
	Folder         *object.Folder
	VirtualMachine *object.VirtualMachine

	Clone *object.VirtualMachine
}

func NewClone(machineName string) (Clone, error) {
	cmd := Clone{name: machineName}

	return cmd, nil
}

func (cmd *Clone) Run(ctx context.Context, client *govmomi.Client, spec *v1alpha1.VMwareMachineClassSpec) error {
	var err error

	ctx = flags.ContextWithPseudoFlagset(ctx, client, spec)

	clientFlag, ctx := flags.NewClientFlag(ctx)
	cmd.Client, err = clientFlag.Client()
	if err != nil {
		return err
	}

	clusterFlag, ctx := flags.NewClusterFlag(ctx)
	cmd.Cluster, err = clusterFlag.ClusterIfSpecified()
	if err != nil {
		return err
	}

	datacenterFlag, ctx := flags.NewDatacenterFlag(ctx)
	cmd.Datacenter, err = datacenterFlag.Datacenter()
	if err != nil {
		return err
	}

	storagePodFlag, ctx := flags.NewStoragePodFlag(ctx)
	if storagePodFlag.Isset() {
		cmd.StoragePod, err = storagePodFlag.StoragePod()
		if err != nil {
			return err
		}
	} else if cmd.Cluster == nil {
		datastoreFlag, ctx2 := flags.NewDatastoreFlag(ctx)
		ctx = ctx2
		cmd.Datastore, err = datastoreFlag.Datastore()
		if err != nil {
			return err
		}
	}

	hostSystemFlag, ctx := flags.NewHostSystemFlag(ctx)
	cmd.HostSystem, err = hostSystemFlag.HostSystemIfSpecified()
	if err != nil {
		return err
	}

	if cmd.HostSystem != nil {
		if cmd.ResourcePool, err = cmd.HostSystem.ResourcePool(ctx); err != nil {
			return err
		}
	} else {
		if cmd.Cluster == nil {
			// -host is optional
			resourcePoolFlag, ctx2 := flags.NewResourcePoolFlag(ctx)
			ctx = ctx2
			if cmd.ResourcePool, err = resourcePoolFlag.ResourcePool(); err != nil {
				return err
			}
		} else {
			if cmd.ResourcePool, err = cmd.Cluster.ResourcePool(ctx); err != nil {
				return err
			}
		}
	}

	folderFlag, ctx := flags.NewFolderFlag(ctx)
	if cmd.Folder, err = folderFlag.Folder(); err != nil {
		return err
	}

	cmd.NetworkFlag, ctx = flags.NewNetworkFlag(ctx)

	virtualMachineFlag, ctx := flags.NewVirtualMachineFlag(ctx)
	if cmd.VirtualMachine, err = virtualMachineFlag.VirtualMachine(); err != nil {
		return err
	}

	if cmd.VirtualMachine == nil {
		return flag.ErrHelp
	}

	vm, err := cmd.cloneVM(ctx)
	if err != nil {
		return err
	}
	cmd.Clone = vm

	vappConfig, err := cmd.expandVAppConfig(ctx)
	if err != nil {
		return err
	}

	cpus := spec.NumCpus
	memory := spec.Memory
	annotation := fromTags(spec.Tags)
	if cpus > 0 || memory > 0 || annotation != "" || vappConfig != nil {
		vmConfigSpec := types.VirtualMachineConfigSpec{}
		if cpus > 0 {
			vmConfigSpec.NumCPUs = int32(cpus)
		}
		if memory > 0 {
			vmConfigSpec.MemoryMB = int64(memory)
		}
		vmConfigSpec.Annotation = annotation
		vmConfigSpec.VAppConfig = vappConfig

		task, err := vm.Reconfigure(ctx, vmConfigSpec)
		if err != nil {
			return err
		}
		_, err = task.WaitForResult(ctx, nil)
		if err != nil {
			return err
		}
	}

	return cmd.powerOn(ctx)
}

// expandVAppConfig reads in all the vapp key/value pairs and returns
// the appropriate VmConfigSpec.
//
// We track changes to keys to determine if any have been removed from
// configuration - if they have, we add them with an empty value to ensure
// they are removed from vAppConfig on the update.
func (cmd *Clone) expandVAppConfig(ctx context.Context) (*types.VmConfigSpec, error) {
	vm := cmd.Clone
	newVApps := flags.GetSpecFromPseudoFlagset(ctx).VApp
	if newVApps == nil {
		return nil, nil
	}

	var props []types.VAppPropertySpec

	newMap := newVApps.Properties
	vmProps, _ := moProperties(vm)
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

// Properties is a convenience method that wraps fetching the
// VirtualMachine MO from its higher-level object.
func moProperties(vm *object.VirtualMachine) (*mo.VirtualMachine, error) {
	glog.V(4).Infof("[DEBUG] Fetching properties for VM %q", vm.InventoryPath)
	ctx, cancel := context.WithTimeout(context.Background(), defaultAPITimeout)
	defer cancel()
	var props mo.VirtualMachine
	if err := vm.Properties(ctx, vm.Reference(), nil, &props); err != nil {
		return nil, err
	}
	return &props, nil
}

func (cmd *Clone) powerOn(ctx context.Context) error {
	vm := cmd.Clone
	task, err := vm.PowerOn(ctx)
	if err != nil {
		return err
	}

	_, err = task.WaitForResult(ctx, nil)
	if err != nil {
		return err
	}

	waitForIP := flags.GetSpecFromPseudoFlagset(ctx).WaitForIP
	if waitForIP {
		_, err = vm.WaitForIP(ctx)
		if err != nil {
			return err
		}
	}

	return nil
}

func fromTags(tags map[string]string) string {
	if len(tags) == 0 {
		return ""
	}
	arr := make([]string, 0, len(tags))
	for k, v := range tags {
		arr = append(arr, k+"="+v)
	}
	return strings.Join(arr, ", ")
}

func (cmd *Clone) cloneVM(ctx context.Context) (*object.VirtualMachine, error) {
	devices, err := cmd.VirtualMachine.Device(ctx)
	if err != nil {
		return nil, err
	}

	// prepare virtual device config spec for network card
	configSpecs := []types.BaseVirtualDeviceConfigSpec{}

	if cmd.NetworkFlag.IsSet() {
		op := types.VirtualDeviceConfigSpecOperationAdd
		card, derr := cmd.NetworkFlag.Device()
		if derr != nil {
			return nil, derr
		}
		// search for the first network card of the source
		for _, device := range devices {
			if _, ok := device.(types.BaseVirtualEthernetCard); ok {
				op = types.VirtualDeviceConfigSpecOperationEdit
				// set new backing info
				cmd.NetworkFlag.Change(device, card)
				card = device
				break
			}
		}

		configSpecs = append(configSpecs, &types.VirtualDeviceConfigSpec{
			Operation: op,
			Device:    card,
		})
	}

	folderref := cmd.Folder.Reference()
	poolref := cmd.ResourcePool.Reference()

	relocateSpec := types.VirtualMachineRelocateSpec{
		DeviceChange: configSpecs,
		Folder:       &folderref,
		Pool:         &poolref,
	}

	if cmd.HostSystem != nil {
		hostref := cmd.HostSystem.Reference()
		relocateSpec.Host = &hostref
	}

	cloneSpec := &types.VirtualMachineCloneSpec{
		PowerOn:  false,
		Template: false,
	}

	cloneSpec.Location = relocateSpec
	vmref := cmd.VirtualMachine.Reference()

	// clone to storage pod
	datastoreref := types.ManagedObjectReference{}
	if cmd.StoragePod != nil && cmd.Datastore == nil {
		storagePod := cmd.StoragePod.Reference()

		// Build pod selection spec from config spec
		podSelectionSpec := types.StorageDrsPodSelectionSpec{
			StoragePod: &storagePod,
		}

		// Get the virtual machine reference
		vmref := cmd.VirtualMachine.Reference()

		// Build the placement spec
		storagePlacementSpec := types.StoragePlacementSpec{
			Folder:           &folderref,
			Vm:               &vmref,
			CloneName:        cmd.name,
			CloneSpec:        cloneSpec,
			PodSelectionSpec: podSelectionSpec,
			Type:             string(types.StoragePlacementSpecPlacementTypeClone),
		}

		// Get the storage placement result
		storageResourceManager := object.NewStorageResourceManager(cmd.Client)
		result, err := storageResourceManager.RecommendDatastores(ctx, storagePlacementSpec)
		if err != nil {
			return nil, err
		}

		// Get the recommendations
		recommendations := result.Recommendations
		if len(recommendations) == 0 {
			return nil, fmt.Errorf("no datastore-cluster recommendations")
		}

		// Get the first recommendation
		datastoreref = recommendations[0].Action[0].(*types.StoragePlacementAction).Destination
	} else if cmd.StoragePod == nil && cmd.Datastore != nil {
		datastoreref = cmd.Datastore.Reference()
	} else if cmd.Cluster != nil {
		spec := types.PlacementSpec{
			PlacementType: string(types.PlacementSpecPlacementTypeClone),
			CloneName:     cmd.name,
			CloneSpec:     cloneSpec,
			RelocateSpec:  &cloneSpec.Location,
			Vm:            &vmref,
		}
		result, err := cmd.Cluster.PlaceVm(ctx, spec)
		if err != nil {
			return nil, err
		}

		recs := result.Recommendations
		if len(recs) == 0 {
			return nil, fmt.Errorf("no cluster recommendations")
		}

		rspec := *recs[0].Action[0].(*types.PlacementAction).RelocateSpec
		cloneSpec.Location.Host = rspec.Host
		cloneSpec.Location.Datastore = rspec.Datastore
		datastoreref = *rspec.Datastore
	} else {
		return nil, fmt.Errorf("please provide either a cluster, datastore or datastore-cluster")
	}

	// Set the destination datastore
	cloneSpec.Location.Datastore = &datastoreref

	// Check if vmx already exists
	force := flags.GetSpecFromPseudoFlagset(ctx).Force
	if !force {
		vmxPath := fmt.Sprintf("%s/%s.vmx", cmd.name, cmd.name)

		var mds mo.Datastore
		err = property.DefaultCollector(cmd.Client).RetrieveOne(ctx, datastoreref, []string{"name"}, &mds)
		if err != nil {
			return nil, err
		}

		datastore := object.NewDatastore(cmd.Client, datastoreref)
		datastore.InventoryPath = mds.Name

		_, err := datastore.Stat(ctx, vmxPath)
		if err == nil {
			dsPath := cmd.Datastore.Path(vmxPath)
			return nil, fmt.Errorf("file %s already exists", dsPath)
		}
	}

	// check if customization specification requested
	customization := flags.GetSpecFromPseudoFlagset(ctx).Customization
	if len(customization) > 0 {
		// get the customization spec manager
		customizationSpecManager := object.NewCustomizationSpecManager(cmd.Client)
		// check if customization specification exists
		exists, err := customizationSpecManager.DoesCustomizationSpecExist(ctx, customization)
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, fmt.Errorf("customization specification %s does not exists", customization)
		}
		// get the customization specification
		customSpecItem, err := customizationSpecManager.GetCustomizationSpec(ctx, customization)
		if err != nil {
			return nil, err
		}
		customSpec := customSpecItem.Spec
		// set the customization
		cloneSpec.Customization = &customSpec
	}

	task, err := cmd.VirtualMachine.Clone(ctx, cmd.Folder, cmd.name, *cloneSpec)
	if err != nil {
		return nil, err
	}

	glog.Infof("Cloning %s to %s...", cmd.VirtualMachine.InventoryPath, cmd.name)

	info, err := task.WaitForResult(ctx)
	if err != nil {
		return nil, err
	}

	return object.NewVirtualMachine(cmd.Client, info.Result.(types.ManagedObjectReference)), nil
}
