// Code generated by client-gen. DO NOT EDIT.

package fake

import (
	v1alpha1 "github.com/gardener/machine-controller-manager/pkg/apis/machine/v1alpha1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	labels "k8s.io/apimachinery/pkg/labels"
	schema "k8s.io/apimachinery/pkg/runtime/schema"
	types "k8s.io/apimachinery/pkg/types"
	watch "k8s.io/apimachinery/pkg/watch"
	testing "k8s.io/client-go/testing"
)

// FakeVsphereMachineClasses implements VsphereMachineClassInterface
type FakeVsphereMachineClasses struct {
	Fake *FakeMachineV1alpha1
	ns   string
}

var vspheremachineclassesResource = schema.GroupVersionResource{Group: "machine.sapcloud.io", Version: "v1alpha1", Resource: "vspheremachineclasses"}

var vspheremachineclassesKind = schema.GroupVersionKind{Group: "machine.sapcloud.io", Version: "v1alpha1", Kind: "VsphereMachineClass"}

// Get takes name of the vsphereMachineClass, and returns the corresponding vsphereMachineClass object, and an error if there is any.
func (c *FakeVsphereMachineClasses) Get(name string, options v1.GetOptions) (result *v1alpha1.VsphereMachineClass, err error) {
	obj, err := c.Fake.
		Invokes(testing.NewGetAction(vspheremachineclassesResource, c.ns, name), &v1alpha1.VsphereMachineClass{})

	if obj == nil {
		return nil, err
	}
	return obj.(*v1alpha1.VsphereMachineClass), err
}

// List takes label and field selectors, and returns the list of VsphereMachineClasses that match those selectors.
func (c *FakeVsphereMachineClasses) List(opts v1.ListOptions) (result *v1alpha1.VsphereMachineClassList, err error) {
	obj, err := c.Fake.
		Invokes(testing.NewListAction(vspheremachineclassesResource, vspheremachineclassesKind, c.ns, opts), &v1alpha1.VsphereMachineClassList{})

	if obj == nil {
		return nil, err
	}

	label, _, _ := testing.ExtractFromListOptions(opts)
	if label == nil {
		label = labels.Everything()
	}
	list := &v1alpha1.VsphereMachineClassList{ListMeta: obj.(*v1alpha1.VsphereMachineClassList).ListMeta}
	for _, item := range obj.(*v1alpha1.VsphereMachineClassList).Items {
		if label.Matches(labels.Set(item.Labels)) {
			list.Items = append(list.Items, item)
		}
	}
	return list, err
}

// Watch returns a watch.Interface that watches the requested vsphereMachineClasses.
func (c *FakeVsphereMachineClasses) Watch(opts v1.ListOptions) (watch.Interface, error) {
	return c.Fake.
		InvokesWatch(testing.NewWatchAction(vspheremachineclassesResource, c.ns, opts))

}

// Create takes the representation of a vsphereMachineClass and creates it.  Returns the server's representation of the vsphereMachineClass, and an error, if there is any.
func (c *FakeVsphereMachineClasses) Create(vsphereMachineClass *v1alpha1.VsphereMachineClass) (result *v1alpha1.VsphereMachineClass, err error) {
	obj, err := c.Fake.
		Invokes(testing.NewCreateAction(vspheremachineclassesResource, c.ns, vsphereMachineClass), &v1alpha1.VsphereMachineClass{})

	if obj == nil {
		return nil, err
	}
	return obj.(*v1alpha1.VsphereMachineClass), err
}

// Update takes the representation of a vsphereMachineClass and updates it. Returns the server's representation of the vsphereMachineClass, and an error, if there is any.
func (c *FakeVsphereMachineClasses) Update(vsphereMachineClass *v1alpha1.VsphereMachineClass) (result *v1alpha1.VsphereMachineClass, err error) {
	obj, err := c.Fake.
		Invokes(testing.NewUpdateAction(vspheremachineclassesResource, c.ns, vsphereMachineClass), &v1alpha1.VsphereMachineClass{})

	if obj == nil {
		return nil, err
	}
	return obj.(*v1alpha1.VsphereMachineClass), err
}

// Delete takes name of the vsphereMachineClass and deletes it. Returns an error if one occurs.
func (c *FakeVsphereMachineClasses) Delete(name string, options *v1.DeleteOptions) error {
	_, err := c.Fake.
		Invokes(testing.NewDeleteAction(vspheremachineclassesResource, c.ns, name), &v1alpha1.VsphereMachineClass{})

	return err
}

// DeleteCollection deletes a collection of objects.
func (c *FakeVsphereMachineClasses) DeleteCollection(options *v1.DeleteOptions, listOptions v1.ListOptions) error {
	action := testing.NewDeleteCollectionAction(vspheremachineclassesResource, c.ns, listOptions)

	_, err := c.Fake.Invokes(action, &v1alpha1.VsphereMachineClassList{})
	return err
}

// Patch applies the patch and returns the patched vsphereMachineClass.
func (c *FakeVsphereMachineClasses) Patch(name string, pt types.PatchType, data []byte, subresources ...string) (result *v1alpha1.VsphereMachineClass, err error) {
	obj, err := c.Fake.
		Invokes(testing.NewPatchSubresourceAction(vspheremachineclassesResource, c.ns, name, data, subresources...), &v1alpha1.VsphereMachineClass{})

	if obj == nil {
		return nil, err
	}
	return obj.(*v1alpha1.VsphereMachineClass), err
}
