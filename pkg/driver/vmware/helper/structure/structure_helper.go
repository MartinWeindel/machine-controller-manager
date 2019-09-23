package structure

import (
	"fmt"
	"math"
	"reflect"

	"github.com/gardener/machine-controller-manager/pkg/driver/vmware/schema"
	"github.com/vmware/govmomi/vim25/types"
)

// ResourceIDStringer is a small interface that can be used to supply
// ResourceData and ResourceDiff to functions that need to print the ID of a
// resource, namely used by logging.
type ResourceIDStringer interface {
	Id() string
}

// ResourceIDString prints a friendly string for a resource, supplied by name.
func ResourceIDString(d ResourceIDStringer, name string) string {
	id := d.Id()
	if id == "" {
		id = "<new resource>"
	}
	return fmt.Sprintf("%s (ID = %s)", name, id)
}

// SliceInterfacesToStrings converts an interface slice to a string slice. The
// function does not attempt to do any sanity checking and will panic if one of
// the items in the slice is not a string.
func SliceInterfacesToStrings(s []interface{}) []string {
	var d []string
	for _, v := range s {
		if o, ok := v.(string); ok {
			d = append(d, o)
		}
	}
	return d
}

// SliceStringsToInterfaces converts a string slice to an interface slice.
func SliceStringsToInterfaces(s []string) []interface{} {
	var d []interface{}
	for _, v := range s {
		d = append(d, v)
	}
	return d
}

// SliceInterfacesToManagedObjectReferences converts an interface slice into a
// slice of ManagedObjectReferences with the type of t.
func SliceInterfacesToManagedObjectReferences(s []interface{}, t string) []types.ManagedObjectReference {
	var d []types.ManagedObjectReference
	for _, v := range s {
		d = append(d, types.ManagedObjectReference{
			Type:  t,
			Value: v.(string),
		})
	}
	return d
}

// SliceStringsToManagedObjectReferences converts a string slice into a slice
// of ManagedObjectReferences with the type of t.
func SliceStringsToManagedObjectReferences(s []string, t string) []types.ManagedObjectReference {
	var d []types.ManagedObjectReference
	for _, v := range s {
		d = append(d, types.ManagedObjectReference{
			Type:  t,
			Value: v,
		})
	}
	return d
}

// MergeSchema merges the map[string]*schema.Schema from src into dst. Safety
// against conflicts is enforced by panicing.
func MergeSchema(dst, src map[string]*schema.Schema) {
	for k, v := range src {
		if _, ok := dst[k]; ok {
			panic(fmt.Errorf("conflicting schema key: %s", k))
		}
		dst[k] = v
	}
}

// StringPtr makes a *string out of the value passed in through v.
//
// vSphere uses nil values in strings to omit values in the SOAP XML request,
// and helps denote inheritance in certain cases.
func StringPtr(v string) *string {
	return &v
}

// BoolPtr makes a *bool out of the value passed in through v.
//
// vSphere uses nil values in bools to omit values in the SOAP XML request, and
// helps denote inheritance in certain cases.
func BoolPtr(v bool) *bool {
	return &v
}

// Int64Ptr makes an *int64 out of the value passed in through v.
func Int64Ptr(v int64) *int64 {
	return &v
}

// Int32Ptr makes an *int32 out of the value passed in through v.
func Int32Ptr(v int32) *int32 {
	return &v
}

// ByteToMB returns n/1000000. The input must be an integer that can be divisible
// by 1000000.
func ByteToMB(n interface{}) interface{} {
	switch v := n.(type) {
	case int:
		return v / 1000000
	case int32:
		return v / 1000000
	case int64:
		return v / 1000000
	}
	panic(fmt.Errorf("non-integer type %T for value", n))
}

// ByteToGB returns n/1000000000. The input must be an integer that can be
// divisible by 1000000000.
//
// Remember that int32 overflows at 2GB, so any values higher than that will
// produce an inaccurate result.
func ByteToGB(n interface{}) interface{} {
	switch v := n.(type) {
	case int:
		return v / 1000000000
	case int32:
		return v / 1000000000
	case int64:
		return v / 1000000000
	}
	panic(fmt.Errorf("non-integer type %T for value", n))
}

// ByteToGiB returns n/1024^3. The input must be an integer that can be
// appropriately divisible.
//
// Remember that int32 overflows at approximately 2GiB, so any values higher
// than that will produce an inaccurate result.
func ByteToGiB(n interface{}) interface{} {
	switch v := n.(type) {
	case int:
		return v / int(math.Pow(1024, 3))
	case int32:
		return v / int32(math.Pow(1024, 3))
	case int64:
		return v / int64(math.Pow(1024, 3))
	}
	panic(fmt.Errorf("non-integer type %T for value", n))
}

// GiBToByte returns n*1024^3.
//
// The output is returned as int64 - if another type is needed, it needs to be
// cast. Remember that int32 overflows at around 2GiB and uint32 will overflow at 4GiB.
func GiBToByte(n interface{}) int64 {
	switch v := n.(type) {
	case int:
		return int64(v * int(math.Pow(1024, 3)))
	case int32:
		return int64(v * int32(math.Pow(1024, 3)))
	case int64:
		return v * int64(math.Pow(1024, 3))
	}
	panic(fmt.Errorf("non-integer type %T for value", n))
}

// GBToByte returns n*1000000000.
//
// The output is returned as int64 - if another type is needed, it needs to be
// cast. Remember that int32 overflows at 2GB and uint32 will overflow at 4GB.
func GBToByte(n interface{}) int64 {
	switch v := n.(type) {
	case int:
		return int64(v * 1000000000)
	case int32:
		return int64(v * 1000000000)
	case int64:
		return v * 1000000000
	}
	panic(fmt.Errorf("non-integer type %T for value", n))
}

// BoolPolicy converts a bool into a VMware BoolPolicy value.
func BoolPolicy(b bool) *types.BoolPolicy {
	bp := &types.BoolPolicy{
		Value: BoolPtr(b),
	}
	return bp
}

// StringPolicy converts a string into a VMware StringPolicy value.
func StringPolicy(s string) *types.StringPolicy {
	sp := &types.StringPolicy{
		Value: s,
	}
	return sp
}

// LongPolicy converts a supported number into a VMware LongPolicy value. This
// will panic if there is no implicit conversion of the value into an int64.
func LongPolicy(n interface{}) *types.LongPolicy {
	lp := &types.LongPolicy{}
	switch v := n.(type) {
	case int:
		lp.Value = int64(v)
	case int8:
		lp.Value = int64(v)
	case int16:
		lp.Value = int64(v)
	case int32:
		lp.Value = int64(v)
	case uint:
		lp.Value = int64(v)
	case uint8:
		lp.Value = int64(v)
	case uint16:
		lp.Value = int64(v)
	case uint32:
		lp.Value = int64(v)
	case int64:
		lp.Value = v
	default:
		panic(fmt.Errorf("non-convertible type %T for value", n))
	}
	return lp
}

// DeRef returns the value pointed to by the interface if the interface is a
// pointer and is not nil, otherwise returns nil, or the direct value if it's
// not a pointer.
func DeRef(v interface{}) interface{} {
	if v == nil {
		return nil
	}
	k := reflect.TypeOf(v).Kind()
	if k != reflect.Ptr {
		return v
	}
	if reflect.ValueOf(v) == reflect.Zero(reflect.TypeOf(v)) {
		// All zero-value pointers are nil
		return nil
	}
	return reflect.ValueOf(v).Elem().Interface()
}
