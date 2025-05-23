package test

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
)

// MockClient is a mock implementation of client.Client
type MockClient struct {
	scheme  *runtime.Scheme
	objects map[client.ObjectKey]client.Object
	lists   map[schema.GroupVersionKind][]client.Object
	mutex   sync.RWMutex
}

// NewMockClient creates a new mock client
func NewMockClient(scheme *runtime.Scheme) *MockClient {
	if scheme == nil {
		scheme = runtime.NewScheme()
		_ = scheme.SetVersionPriority(schema.GroupVersion{Group: "", Version: "v1"})
		_ = scheme.SetVersionPriority(schema.GroupVersion{Group: "networking.k8s.aws", Version: "v1alpha1"})
	}
	return &MockClient{
		scheme:  scheme,
		objects: make(map[client.ObjectKey]client.Object),
		lists:   make(map[schema.GroupVersionKind][]client.Object),
	}
}

// Get retrieves an object
func (m *MockClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	storedObj, found := m.objects[key]
	if !found {
		return fmt.Errorf("object %s not found", key)
	}

	// Copy the object
	objVal := reflect.ValueOf(obj)
	if objVal.Kind() != reflect.Ptr || objVal.IsNil() {
		return fmt.Errorf("object is not a pointer or is nil")
	}

	// Get the GVK of the object
	gvk, err := apiutil.GVKForObject(storedObj, m.scheme)
	if err != nil {
		return err
	}

	// Create a new object of the same type
	newObj, err := m.scheme.New(gvk)
	if err != nil {
		return err
	}

	// Deep copy the stored object to the new object
	if err := m.scheme.Convert(storedObj, newObj, nil); err != nil {
		return err
	}

	// Convert the new object to the requested object
	if err := m.scheme.Convert(newObj, obj, nil); err != nil {
		return err
	}

	return nil
}

// List retrieves a list of objects
func (m *MockClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	// Get the GVK of the list
	gvk, err := apiutil.GVKForObject(list, m.scheme)
	if err != nil {
		return err
	}

	// Get the GVK of the items in the list
	itemsGVK := schema.GroupVersionKind{
		Group:   gvk.Group,
		Version: gvk.Version,
		Kind:    strings.TrimSuffix(gvk.Kind, "List"),
	}

	// Get the items
	items, found := m.lists[itemsGVK]
	if !found {
		// Return empty list
		return nil
	}

	// Apply list options
	filteredItems := filterItems(items, opts)

	// Set the items in the list
	listVal := reflect.ValueOf(list).Elem()
	itemsField := listVal.FieldByName("Items")
	if !itemsField.IsValid() {
		return fmt.Errorf("list does not have Items field")
	}

	// Create a new slice for the items
	itemsType := itemsField.Type()
	newItems := reflect.MakeSlice(itemsType, len(filteredItems), len(filteredItems))

	// Copy the items to the new slice
	for i, item := range filteredItems {
		// Create a new object of the same type
		newObj, err := m.scheme.New(itemsGVK)
		if err != nil {
			return err
		}

		// Deep copy the item to the new object
		if err := m.scheme.Convert(item, newObj, nil); err != nil {
			return err
		}

		// Set the item in the new slice
		newItems.Index(i).Set(reflect.ValueOf(newObj).Elem())
	}

	// Set the new slice in the list
	itemsField.Set(newItems)

	return nil
}

// Create creates an object
func (m *MockClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	// Get the key for the object
	key := client.ObjectKey{
		Namespace: obj.GetNamespace(),
		Name:      obj.GetName(),
	}

	// Check if the object already exists
	if _, found := m.objects[key]; found {
		return fmt.Errorf("object %s already exists", key)
	}

	// Set creation timestamp if not set
	timestamp := obj.GetCreationTimestamp()
	if timestamp.IsZero() {
		obj.SetCreationTimestamp(metav1.Now())
	}

	// Create a deep copy of the object
	gvk, err := apiutil.GVKForObject(obj, m.scheme)
	if err != nil {
		return err
	}

	newObj, err := m.scheme.New(gvk)
	if err != nil {
		return err
	}

	if err := m.scheme.Convert(obj, newObj, nil); err != nil {
		return err
	}

	// Store the object
	m.objects[key] = newObj.(client.Object)

	// Add to list
	m.lists[gvk] = append(m.lists[gvk], newObj.(client.Object))

	return nil
}

// Delete deletes an object
func (m *MockClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	// Get the key for the object
	key := client.ObjectKey{
		Namespace: obj.GetNamespace(),
		Name:      obj.GetName(),
	}

	// Check if the object exists
	if _, found := m.objects[key]; !found {
		return fmt.Errorf("object %s not found", key)
	}

	// Get the GVK of the object
	gvk, err := apiutil.GVKForObject(obj, m.scheme)
	if err != nil {
		return err
	}

	// Remove from list
	items := m.lists[gvk]
	for i, item := range items {
		if item.GetName() == obj.GetName() && item.GetNamespace() == obj.GetNamespace() {
			m.lists[gvk] = append(items[:i], items[i+1:]...)
			break
		}
	}

	// Delete the object
	delete(m.objects, key)

	return nil
}

// Update updates an object
func (m *MockClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	// Get the key for the object
	key := client.ObjectKey{
		Namespace: obj.GetNamespace(),
		Name:      obj.GetName(),
	}

	// Check if the object exists
	if _, found := m.objects[key]; !found {
		return fmt.Errorf("object %s not found", key)
	}

	// Create a deep copy of the object
	gvk, err := apiutil.GVKForObject(obj, m.scheme)
	if err != nil {
		return err
	}

	newObj, err := m.scheme.New(gvk)
	if err != nil {
		return err
	}

	if err := m.scheme.Convert(obj, newObj, nil); err != nil {
		return err
	}

	// Store the object
	m.objects[key] = newObj.(client.Object)

	// Update in list
	items := m.lists[gvk]
	for i, item := range items {
		if item.GetName() == obj.GetName() && item.GetNamespace() == obj.GetNamespace() {
			m.lists[gvk][i] = newObj.(client.Object)
			break
		}
	}

	return nil
}

// Patch patches an object
func (m *MockClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	// For simplicity, just call Update with no options
	return m.Update(ctx, obj)
}

// DeleteAllOf deletes all objects of a type
func (m *MockClient) DeleteAllOf(ctx context.Context, obj client.Object, opts ...client.DeleteAllOfOption) error {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	// Get the GVK of the object
	gvk, err := apiutil.GVKForObject(obj, m.scheme)
	if err != nil {
		return err
	}

	// Remove all objects of this type
	m.lists[gvk] = nil

	// Remove from objects map
	for key, obj := range m.objects {
		objGVK, err := apiutil.GVKForObject(obj, m.scheme)
		if err != nil {
			continue
		}
		if objGVK == gvk {
			delete(m.objects, key)
		}
	}

	return nil
}

// Status returns a StatusWriter for updating status
func (m *MockClient) Status() client.StatusWriter {
	return &MockStatusWriter{client: m}
}

// Scheme returns the scheme
func (m *MockClient) Scheme() *runtime.Scheme {
	return m.scheme
}

// RESTMapper returns a RESTMapper
func (m *MockClient) RESTMapper() meta.RESTMapper {
	return nil
}

// SubResource returns a SubResourceClient for accessing subresources
func (m *MockClient) SubResource(subResource string) client.SubResourceClient {
	return &MockSubResourceClient{client: m, subResource: subResource}
}

// MockSubResourceClient is a mock implementation of client.SubResourceClient
type MockSubResourceClient struct {
	client      *MockClient
	subResource string
}

// Get gets a subresource
func (m *MockSubResourceClient) Get(ctx context.Context, obj client.Object, subResource client.Object, opts ...client.SubResourceGetOption) error {
	// For simplicity, just return nil
	return nil
}

// Create creates a subresource
func (m *MockSubResourceClient) Create(ctx context.Context, obj client.Object, subResource client.Object, opts ...client.SubResourceCreateOption) error {
	// For simplicity, just return nil
	return nil
}

// Update updates a subresource
func (m *MockSubResourceClient) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	// For simplicity, just call Update on the parent client
	return m.client.Update(ctx, obj)
}

// Patch patches a subresource
func (m *MockSubResourceClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	// For simplicity, just call Patch on the parent client
	return m.client.Patch(ctx, obj, patch)
}

// MockStatusWriter is a mock implementation of client.StatusWriter
type MockStatusWriter struct {
	client *MockClient
}

// Update updates the status of an object
func (m *MockStatusWriter) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	return m.client.Update(ctx, obj)
}

// Patch patches the status of an object
func (m *MockStatusWriter) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	return m.client.Patch(ctx, obj, patch)
}

// Create creates the status of an object
func (m *MockStatusWriter) Create(ctx context.Context, obj client.Object, subResource client.Object, opts ...client.SubResourceCreateOption) error {
	// For simplicity, just call Update
	return m.client.Update(ctx, obj)
}

// MockEventRecorder is a mock implementation of record.EventRecorder
type MockEventRecorder struct {
	Events []string
	mutex  sync.RWMutex
}

// NewMockEventRecorder creates a new mock event recorder
func NewMockEventRecorder() *MockEventRecorder {
	return &MockEventRecorder{
		Events: make([]string, 0),
	}
}

// Event records an event
func (m *MockEventRecorder) Event(object runtime.Object, eventtype, reason, message string) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.Events = append(m.Events, fmt.Sprintf("%s %s %s", eventtype, reason, message))
}

// Eventf records an event with formatting
func (m *MockEventRecorder) Eventf(object runtime.Object, eventtype, reason, messageFmt string, args ...interface{}) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.Events = append(m.Events, fmt.Sprintf("%s %s %s", eventtype, reason, fmt.Sprintf(messageFmt, args...)))
}

// AnnotatedEventf records an event with annotations
func (m *MockEventRecorder) AnnotatedEventf(object runtime.Object, annotations map[string]string, eventtype, reason, messageFmt string, args ...interface{}) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.Events = append(m.Events, fmt.Sprintf("%s %s %s", eventtype, reason, fmt.Sprintf(messageFmt, args...)))
}

// filterItems applies list options to filter items
func filterItems(items []client.Object, opts []client.ListOption) []client.Object {
	// If no options, return all items
	if len(opts) == 0 {
		return items
	}

	// Apply each option
	filteredItems := make([]client.Object, 0, len(items))
	for _, item := range items {
		include := true
		for _, opt := range opts {
			// Handle different option types
			switch o := opt.(type) {
			case client.MatchingLabels:
				// Check if item has all the required labels
				itemLabels := item.GetLabels()
				for k, v := range o {
					if itemLabels[k] != v {
						include = false
						break
					}
				}
			case client.InNamespace:
				// Check if item is in the namespace
				if string(o) != item.GetNamespace() && string(o) != "" {
					include = false
				}
			}
			if !include {
				break
			}
		}
		if include {
			filteredItems = append(filteredItems, item)
		}
	}
	return filteredItems
}
