package virtualdevice

import (
	"github.com/gardener/machine-controller-manager/pkg/apis/machine/v1alpha1"
	"github.com/gardener/machine-controller-manager/pkg/driver/vmware/schema"
	"github.com/gardener/machine-controller-manager/pkg/driver/vmware/validation"
	"github.com/golang/glog"
	"github.com/vmware/govmomi/vim25/types"
	"strconv"
)

var virtualMachineVirtualExecUsageAllowedValues = []string{
	string(types.VirtualMachineFlagInfoVirtualExecUsageHvAuto),
	string(types.VirtualMachineFlagInfoVirtualExecUsageHvOn),
	string(types.VirtualMachineFlagInfoVirtualExecUsageHvOff),
}

var virtualMachineVirtualMmuUsageAllowedValues = []string{
	string(types.VirtualMachineFlagInfoVirtualMmuUsageAutomatic),
	string(types.VirtualMachineFlagInfoVirtualMmuUsageOn),
	string(types.VirtualMachineFlagInfoVirtualMmuUsageOff),
}

var virtualMachineSwapPlacementAllowedValues = []string{
	string(types.VirtualMachineConfigInfoSwapPlacementTypeInherit),
	string(types.VirtualMachineConfigInfoSwapPlacementTypeVmDirectory),
	string(types.VirtualMachineConfigInfoSwapPlacementTypeHostLocal),
}

var virtualMachineFirmwareAllowedValues = []string{
	string(types.GuestOsDescriptorFirmwareTypeBios),
	string(types.GuestOsDescriptorFirmwareTypeEfi),
}

var virtualMachineLatencySensitivityAllowedValues = []string{
	string(types.LatencySensitivitySensitivityLevelLow),
	string(types.LatencySensitivitySensitivityLevelNormal),
	string(types.LatencySensitivitySensitivityLevelMedium),
	string(types.LatencySensitivitySensitivityLevelHigh),
}

var classMorePropertiesSchema = map[string]schema.Schema{
	"scsi_controller_count": {
		Type:         schema.TypeInt,
		Optional:     true,
		Default:      1,
		Description:  "The number of SCSI controllers that Terraform manages on this virtual machine. This directly affects the amount of disks you can add to the virtual machine and the maximum disk unit number. Note that lowering this value does not remove controllers.",
		ValidateFunc: validation.IntBetween(1, 4),
	},
	"scsi_type": {
		Type:         schema.TypeString,
		Optional:     true,
		Default:      SubresourceControllerTypeParaVirtual,
		Description:  "The type of SCSI bus this virtual machine will have. Can be one of lsilogic, lsilogic-sas or pvscsi.",
		ValidateFunc: validation.StringInSlice(SCSIBusTypeAllowedValues, false),
	},
	"scsi_bus_sharing": {
		Type:         schema.TypeString,
		Optional:     true,
		Default:      string(types.VirtualSCSISharingNoSharing),
		Description:  "Mode for sharing the SCSI bus. The modes are physicalSharing, virtualSharing, and noSharing.",
		ValidateFunc: validation.StringInSlice(SCSIBusSharingAllowedValues, false),
	},
	"alternate_guest_name": {
		Type:        schema.TypeString,
		Optional:    true,
		Description: "The guest name for the operating system when guest_id is other or other-64.",
	},
	"firmware": {
		Type:         schema.TypeString,
		Optional:     true,
		Default:      string(types.GuestOsDescriptorFirmwareTypeBios),
		Description:  "The firmware interface to use on the virtual machine. Can be one of bios or EFI.",
		ValidateFunc: validation.StringInSlice(virtualMachineFirmwareAllowedValues, false),
	},
	"num_cores_per_socket": {
		Type:        schema.TypeInt,
		Optional:    true,
		Default:     1,
		Description: "The number of cores to distribute amongst the CPUs in this virtual machine. If specified, the value supplied to num_cpus must be evenly divisible by this value.",
	},
	"cpu_hot_add_enabled": {
		Type:        schema.TypeBool,
		Optional:    true,
		Description: "Allow CPUs to be added to this virtual machine while it is running.",
	},
	"cpu_hot_remove_enabled": {
		Type:        schema.TypeBool,
		Optional:    true,
		Description: "Allow CPUs to be added to this virtual machine while it is running.",
	},
	"nested_hv_enabled": {
		Type:        schema.TypeBool,
		Optional:    true,
		Description: "Enable nested hardware virtualization on this virtual machine, facilitating nested virtualization in the guest.",
	},
	"cpu_performance_counters_enabled": {
		Type:        schema.TypeBool,
		Optional:    true,
		Description: "Enable CPU performance counters on this virtual machine.",
	},
	"swap_placement_policy": {
		Type:         schema.TypeString,
		Optional:     true,
		Default:      string(types.VirtualMachineConfigInfoSwapPlacementTypeInherit),
		Description:  "The swap file placement policy for this virtual machine. Can be one of inherit, hostLocal, or vmDirectory.",
		ValidateFunc: validation.StringInSlice(virtualMachineSwapPlacementAllowedValues, false),
	},
	"memory_hot_add_enabled": {
		Type:        schema.TypeBool,
		Optional:    true,
		Description: "Allow memory to be added to this virtual machine while it is running.",
	},

	// VirtualMachineBootOptions
	"boot_delay": {
		Type:        schema.TypeInt,
		Optional:    true,
		Description: "The number of milliseconds to wait before starting the boot sequence.",
	},
	"efi_secure_boot_enabled": {
		Type:        schema.TypeBool,
		Optional:    true,
		Description: "When the boot type set in firmware is efi, this enables EFI secure boot.",
	},
	"boot_retry_delay": {
		Type:        schema.TypeInt,
		Optional:    true,
		Default:     10000,
		Description: "The number of milliseconds to wait before retrying the boot sequence. This only valid if boot_retry_enabled is true.",
	},
	"boot_retry_enabled": {
		Type:        schema.TypeBool,
		Optional:    true,
		Description: "If set to true, a virtual machine that fails to boot will try again after the delay defined in boot_retry_delay.",
	},

	// VirtualMachineFlagInfo
	"enable_disk_uuid": {
		Type:        schema.TypeBool,
		Optional:    true,
		Description: "Expose the UUIDs of attached virtual disks to the virtual machine, allowing access to them in the guest.",
	},
	"hv_mode": {
		Type:         schema.TypeString,
		Optional:     true,
		Default:      string(types.VirtualMachineFlagInfoVirtualExecUsageHvAuto),
		Description:  "The (non-nested) hardware virtualization setting for this virtual machine. Can be one of hvAuto, hvOn, or hvOff.",
		ValidateFunc: validation.StringInSlice(virtualMachineVirtualExecUsageAllowedValues, false),
	},
	"ept_rvi_mode": {
		Type:         schema.TypeString,
		Optional:     true,
		Default:      string(types.VirtualMachineFlagInfoVirtualMmuUsageAutomatic),
		Description:  "The EPT/RVI (hardware memory virtualization) setting for this virtual machine. Can be one of automatic, on, or off.",
		ValidateFunc: validation.StringInSlice(virtualMachineVirtualMmuUsageAllowedValues, false),
	},
	"enable_logging": {
		Type:        schema.TypeBool,
		Optional:    true,
		Description: "Enable logging on this virtual machine.",
	},

	// ToolsConfigInfo
	"sync_time_with_host": {
		Type:        schema.TypeBool,
		Optional:    true,
		Description: "Enable guest clock synchronization with the host. Requires VMware tools to be installed.",
	},
	"run_tools_scripts_after_power_on": {
		Type:        schema.TypeBool,
		Optional:    true,
		Default:     true,
		Description: "Enable the execution of post-power-on scripts when VMware tools is installed.",
	},
	"run_tools_scripts_after_resume": {
		Type:        schema.TypeBool,
		Optional:    true,
		Default:     true,
		Description: "Enable the execution of post-resume scripts when VMware tools is installed.",
	},
	"run_tools_scripts_before_guest_reboot": {
		Type:        schema.TypeBool,
		Optional:    true,
		Description: "Enable the execution of pre-reboot scripts when VMware tools is installed.",
	},
	"run_tools_scripts_before_guest_shutdown": {
		Type:        schema.TypeBool,
		Optional:    true,
		Default:     true,
		Description: "Enable the execution of pre-shutdown scripts when VMware tools is installed.",
	},
	"run_tools_scripts_before_guest_standby": {
		Type:        schema.TypeBool,
		Optional:    true,
		Default:     true,
		Description: "Enable the execution of pre-standby scripts when VMware tools is installed.",
	},

	"cpu_share_level": {
		Type:         schema.TypeString,
		Optional:     true,
		Default:      string(types.SharesLevelNormal),
		Description:  "The allocation level for cpu resources. Can be one of high, low, normal, or custom.",
		ValidateFunc: validation.StringInSlice(sharesLevelAllowedValues, false),
	},
	"cpu_share_count": {
		Type:         schema.TypeInt,
		Optional:     true,
		Description:  "The amount of shares to allocate to cpu for a custom share level.",
		ValidateFunc: validation.IntAtLeast(0),
	},
	"cpu_limit": {
		Type:         schema.TypeInt,
		Optional:     true,
		Default:      -1,
		Description:  "The maximum amount of CPU (in MHz) that this virtual machine can consume, regardless of available resources.",
		ValidateFunc: validation.IntAtLeast(-1),
	},
	"cpu_reservation": {
		Type:         schema.TypeInt,
		Optional:     true,
		Description:  "The amount of CPU (in MHz) that this virtual machine is guaranteed.",
		ValidateFunc: validation.IntAtLeast(0),
	},
	"memory_share_level": {
		Type:         schema.TypeString,
		Optional:     true,
		Default:      string(types.SharesLevelNormal),
		Description:  "The allocation level for memory resources. Can be one of high, low, normal, or custom.",
		ValidateFunc: validation.StringInSlice(sharesLevelAllowedValues, false),
	},
	"memory_share_count": {
		Type:         schema.TypeInt,
		Optional:     true,
		Description:  "The amount of shares to allocate to memory for a custom share level.",
		ValidateFunc: validation.IntAtLeast(0),
	},
	"memory_limit": {
		Type:         schema.TypeInt,
		Optional:     true,
		Default:      -1,
		Description:  "The maximum amount of memory (in MB) that this virtual machine can consume, regardless of available resources.",
		ValidateFunc: validation.IntAtLeast(-1),
	},
	"memory_reservation": {
		Type:         schema.TypeInt,
		Optional:     true,
		Description:  "The amount of memory (in MB) that this virtual machine is guaranteed.",
		ValidateFunc: validation.IntAtLeast(0),
	},
}

func GetMorePropertiesBool(spec *v1alpha1.VMwareMachineClassSpec, key string) *bool {
	_, schemaFound := classMorePropertiesSchema[key]
	if !schemaFound {
		panic("Undefined More Properties key " + key)
	}
	value, ok := spec.MoreProperties[key]
	if !ok {
		// default value ignored, rely on backend service
		return nil
	}
	b := value == "true"
	return &b
}

func GetMorePropertiesString(spec *v1alpha1.VMwareMachineClassSpec, key string) string {
	schema, schemaFound := classMorePropertiesSchema[key]
	if !schemaFound {
		panic("Undefined More Properties key " + key)
	}
	value, ok := spec.MoreProperties[key]
	if !ok {
		if schema.Default != nil {
			return schema.Default.(string)
		}
		return ""
	}
	return value
}

func GetMorePropertiesInt(spec *v1alpha1.VMwareMachineClassSpec, key string) int {
	schema, schemaFound := classMorePropertiesSchema[key]
	if !schemaFound {
		panic("Undefined MoreProperties key " + key)
	}

	value, ok := spec.MoreProperties[key]
	if !ok {
		if schema.Default != nil {
			return schema.Default.(int)
		}
		return 0
	}
	i, err := strconv.Atoi(value)
	if err != nil {
		glog.Errorf("Invalid value %s for MoreProperties[%s]: %s", value, key, err)
		return 0
	}
	return i
}
