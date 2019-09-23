package schema

import (
	"fmt"
	"reflect"

	"github.com/mitchellh/mapstructure"
)

// Schema is used to describe the structure of a value.
//
// Read the documentation of the struct elements for important details.
type Schema struct {
	// Type is the type of the value and must be one of the ValueType values.
	//
	// This type not only determines what type is expected/valid in configuring
	// this value, but also what type is returned when ResourceData.Get is
	// called. The types returned by Get are:
	//
	//   TypeBool - bool
	//   TypeInt - int
	//   TypeFloat - float64
	//   TypeString - string
	//
	Type ValueType

	// If one of these is set, then this item can come from the configuration.
	// Both cannot be set. If Optional is set, the value is optional. If
	// Required is set, the value is required.
	//
	// One of these must be set if the value is not computed. That is:
	// value either comes from the config, is computed, or is both.
	Optional bool
	Required bool

	// If Computed is true, then the result of this value is computed
	// (unless specified by config) on creation.
	Computed bool

	// If this is non-nil, then this will be a default value that is used
	// when this item is not set in the configuration.
	//
	// DefaultFunc can be specified to compute a dynamic default.
	// Only one of Default or DefaultFunc can be set. If DefaultFunc is
	// used then its return value should be stable to avoid generating
	// confusing/perpetual diffs.
	//
	// Changing either Default or the return value of DefaultFunc can be
	// a breaking change, especially if the attribute in question has
	// ForceNew set. If a default needs to change to align with changing
	// assumptions in an upstream API then it may be necessary to also use
	// the MigrateState function on the resource to change the state to match,
	// or have the Read function adjust the state value to align with the
	// new default.
	//
	// If Required is true above, then Default cannot be set. DefaultFunc
	// can be set with Required. If the DefaultFunc returns nil, then there
	// will be no default and the user will be asked to fill it in.
	//
	// If either of these is set, then the user won't be asked for input
	// for this key if the default is not nil.
	Default interface{}

	// Description is used as the description for docs or asking for user
	// input. It should be relatively short (a few sentences max) and should
	// be formatted to fit a CLI.
	Description string

	// ValidateFunc allows individual fields to define arbitrary validation
	// logic. It is yielded the provided config value as an interface{} that is
	// guaranteed to be of the proper Schema type, and it can yield warnings or
	// errors based on inspection of that value.
	//
	// ValidateFunc is honored only when the schema's Type is set to TypeInt,
	// TypeFloat, TypeString, TypeBool, or TypeMap. It is ignored for all other types.
	ValidateFunc SchemaValidateFunc
}

// SchemaValidateFunc is a function used to validate a single field in the
// schema.
type SchemaValidateFunc func(interface{}, string) ([]string, []error)

func (s *Schema) GoString() string {
	return fmt.Sprintf("*%#v", *s)
}

// Returns a default value for this schema by either reading Default or
// evaluating DefaultFunc. If neither of these are defined, returns nil.
func (s *Schema) DefaultValue() (interface{}, error) {
	if s.Default != nil {
		return s.Default, nil
	}

	return nil, nil
}

// Returns a zero value for the schema.
func (s *Schema) ZeroValue() interface{} {
	return s.Type.Zero()
}

func (schema *Schema) Validate(
	k string,
	raw interface{},
) ([]string, []error) {

	// a nil value shouldn't happen in the old protocol, and in the new
	// protocol the types have already been validated. Either way, we can't
	// reflect on nil, so don't panic.
	if raw == nil {
		return nil, nil
	}

	// Catch if the user gave a complex type where a primitive was
	// expected, so we can return a friendly error message that
	// doesn't contain Go type system terminology.
	switch reflect.ValueOf(raw).Type().Kind() {
	case reflect.Slice:
		return nil, []error{
			fmt.Errorf("%s must be a single value, not a list", k),
		}
	case reflect.Map:
		return nil, []error{
			fmt.Errorf("%s must be a single value, not a map", k),
		}
	default: // ok
	}

	var decoded interface{}
	switch schema.Type {
	case TypeBool:
		// Verify that we can parse this as the correct type
		var n bool
		if err := mapstructure.WeakDecode(raw, &n); err != nil {
			return nil, []error{fmt.Errorf("%s: %s", k, err)}
		}
		decoded = n
	case TypeInt:
		// Verify that we can parse this as an int
		var n int
		if err := mapstructure.WeakDecode(raw, &n); err != nil {
			return nil, []error{fmt.Errorf("%s: %s", k, err)}
		}
		decoded = n
	case TypeFloat:
		// Verify that we can parse this as an int
		var n float64
		if err := mapstructure.WeakDecode(raw, &n); err != nil {
			return nil, []error{fmt.Errorf("%s: %s", k, err)}
		}
		decoded = n
	case TypeString:
		// Verify that we can parse this as a string
		var n string
		if err := mapstructure.WeakDecode(raw, &n); err != nil {
			return nil, []error{fmt.Errorf("%s: %s", k, err)}
		}
		decoded = n
	default:
		panic(fmt.Sprintf("Unknown validation type: %#v", schema.Type))
	}

	if schema.ValidateFunc != nil {
		return schema.ValidateFunc(decoded, k)
	}

	return nil, nil
}

// Zero returns the zero value for a type.
func (t ValueType) Zero() interface{} {
	switch t {
	case TypeInvalid:
		return nil
	case TypeBool:
		return false
	case TypeInt:
		return 0
	case TypeFloat:
		return 0.0
	case TypeString:
		return ""
	default:
		panic(fmt.Sprintf("unknown type %s", t))
	}
}
