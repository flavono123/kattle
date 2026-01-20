package kube

import (
	"reflect"
	"strconv"
	"strings"
	"sync"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// FieldStore stores extracted field values for resources to reduce memory usage.
// Instead of storing full Kubernetes objects, only essential metadata,
// structure info (array lengths, map keys), and selected fields are stored.
type FieldStore struct {
	mu sync.RWMutex

	// data stores field values per resource key
	// key: "namespace/name" or "name" for cluster-scoped
	// value: map[fieldPath]interface{}
	data map[string]map[string]interface{}

	// selectedFields are the field paths selected by the user for display
	selectedFields []string

	// essentialFields are always extracted (metadata)
	essentialFields []string
}

// Essential metadata fields that are always stored
var defaultEssentialFields = []string{
	"metadata.name",
	"metadata.namespace",
	"metadata.uid",
	"metadata.resourceVersion",
	"metadata.creationTimestamp",
	"metadata.labels",
	"metadata.annotations",
	"metadata.ownerReferences",
	"metadata.deletionTimestamp",
	"metadata.finalizers",
}

// NewFieldStore creates a new FieldStore instance
func NewFieldStore() *FieldStore {
	return &FieldStore{
		data:            make(map[string]map[string]interface{}),
		selectedFields:  make([]string, 0),
		essentialFields: defaultEssentialFields,
	}
}

// Get returns all stored field values for a resource
func (fs *FieldStore) Get(key string) map[string]interface{} {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	if fields, ok := fs.data[key]; ok {
		// Return a copy to prevent external modification
		result := make(map[string]interface{}, len(fields))
		for k, v := range fields {
			result[k] = v
		}
		return result
	}
	return nil
}

// GetField returns a specific field value for a resource
func (fs *FieldStore) GetField(key, fieldPath string) (interface{}, bool) {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	if fields, ok := fs.data[key]; ok {
		val, exists := fields[fieldPath]
		return val, exists
	}
	return nil, false
}

// List returns all resource keys in the store
func (fs *FieldStore) List() []string {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	keys := make([]string, 0, len(fs.data))
	for k := range fs.data {
		keys = append(keys, k)
	}
	return keys
}

// Update extracts and stores field values from a full Kubernetes object.
// The original object can be GC'd after this call.
// Extracts: all leaf values (primitives), structure metadata (array lengths, map keys).
func (fs *FieldStore) Update(key string, obj *unstructured.Unstructured) {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	fields := make(map[string]interface{})

	// Extract ALL values (structure metadata + leaf values) with deep copy
	extractAllValues(obj.Object, "", fields)

	fs.data[key] = fields
}

// Delete removes a resource from the store
func (fs *FieldStore) Delete(key string) {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	delete(fs.data, key)
}

// Clear removes all resources from the store
func (fs *FieldStore) Clear() {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	fs.data = make(map[string]map[string]interface{})
}

// SetSelectedFields updates the list of fields to extract.
// Existing data is NOT re-extracted; new updates will use the new field list.
func (fs *FieldStore) SetSelectedFields(fields []string) {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	fs.selectedFields = fields
}

// GetSelectedFields returns the current selected fields
func (fs *FieldStore) GetSelectedFields() []string {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	result := make([]string, len(fs.selectedFields))
	copy(result, fs.selectedFields)
	return result
}

// Count returns the number of resources in the store
func (fs *FieldStore) Count() int {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	return len(fs.data)
}

// GetMaxArrayLength returns the maximum array length at a given path across all resources.
// Used for tree building to determine how many index nodes to create.
// path is a dot-separated path like "spec.containers" or "spec.containers.0.ports".
func (fs *FieldStore) GetMaxArrayLength(path string) int {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	structKey := "_struct:" + path + ".len"
	maxLen := 0

	for _, fields := range fs.data {
		if val, ok := fields[structKey]; ok {
			if length, ok := val.(int); ok && length > maxLen {
				maxLen = length
			}
		}
	}

	return maxLen
}

// GetDistinctMapKeys returns all unique map keys at a given path across all resources.
// Used for tree building to create map key nodes.
// path is a dot-separated path like "metadata.labels" or "metadata.annotations".
func (fs *FieldStore) GetDistinctMapKeys(path string) []string {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	structKey := "_struct:" + path + ".keys"
	exists := make(map[string]struct{})
	var keys []string

	for _, fields := range fs.data {
		if val, ok := fields[structKey]; ok {
			if keyList, ok := val.([]string); ok {
				for _, k := range keyList {
					if _, found := exists[k]; !found {
						exists[k] = struct{}{}
						keys = append(keys, k)
					}
				}
			}
		}
	}

	return keys
}

// DetectStructureChange compares structure metadata between two field maps.
// Returns true if structure has changed (array lengths or map keys differ).
func (fs *FieldStore) DetectStructureChange(oldFields, newFields map[string]interface{}) bool {
	// Check structure metadata prefixed with "_struct:"
	for k, newVal := range newFields {
		if !strings.HasPrefix(k, "_struct:") {
			continue
		}

		oldVal, exists := oldFields[k]
		if !exists {
			return true // new structure field
		}

		if !reflect.DeepEqual(oldVal, newVal) {
			return true // structure changed
		}
	}

	// Check if any structure fields were removed
	for k := range oldFields {
		if !strings.HasPrefix(k, "_struct:") {
			continue
		}
		if _, exists := newFields[k]; !exists {
			return true // structure field removed
		}
	}

	return false
}

// ReconstructObject rebuilds a partial object from stored fields.
// Used for GetResourcesByKeys to return data to frontend.
// Note: This returns only stored fields, not the full original object.
func (fs *FieldStore) ReconstructObject(key string) map[string]interface{} {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	fields, ok := fs.data[key]
	if !ok {
		return nil
	}

	// Build a nested object from flat field paths
	obj := make(map[string]interface{})

	for path, value := range fields {
		// Skip structure metadata (not part of the actual object)
		if strings.HasPrefix(path, "_struct:") {
			continue
		}
		setFieldByPath(obj, path, value)
	}

	return obj
}

// getFieldByPath retrieves a value from a nested map using dot notation.
// e.g., "metadata.name" retrieves obj["metadata"]["name"]
func getFieldByPath(obj map[string]interface{}, path string) (interface{}, bool) {
	parts := strings.Split(path, ".")
	current := interface{}(obj)

	for _, part := range parts {
		switch v := current.(type) {
		case map[string]interface{}:
			val, ok := v[part]
			if !ok {
				return nil, false
			}
			current = val
		default:
			return nil, false
		}
	}

	return current, true
}

// setFieldByPath sets a value in a nested structure using dot notation.
// Handles both maps and arrays - numeric path segments create array indices.
func setFieldByPath(obj map[string]interface{}, path string, value interface{}) {
	parts := strings.Split(path, ".")
	if len(parts) == 0 {
		return
	}
	setNestedValue(obj, parts, value)
}

// setNestedValue recursively sets a value in a nested map/array structure.
func setNestedValue(current map[string]interface{}, parts []string, value interface{}) {
	for i := 0; i < len(parts); i++ {
		part := parts[i]
		isLast := i == len(parts)-1

		if isLast {
			current[part] = value
			return
		}

		// Look ahead to next part to determine structure type
		nextPart := parts[i+1]

		if isNumericIndex(nextPart) {
			// Next part is array index, ensure current[part] is an array
			arr := ensureArray(current, part)
			idx, _ := strconv.Atoi(nextPart)

			// Ensure array has enough elements
			for len(arr) <= idx {
				arr = append(arr, nil)
			}
			current[part] = arr // Update in case append reallocated

			// Check if we're setting a leaf value or nested structure
			if i+2 < len(parts) {
				// More path segments after index - ensure element is a map
				if arr[idx] == nil {
					arr[idx] = make(map[string]interface{})
				}
				elemMap, ok := arr[idx].(map[string]interface{})
				if !ok {
					elemMap = make(map[string]interface{})
					arr[idx] = elemMap
				}
				current = elemMap
			} else {
				// Index is second-to-last, set value directly in array
				arr[idx] = value
				return
			}
			i++ // Skip the index part we just processed
		} else {
			// Next part is a key, ensure current[part] is a map
			if current[part] == nil {
				current[part] = make(map[string]interface{})
			}
			nextMap, ok := current[part].(map[string]interface{})
			if !ok {
				nextMap = make(map[string]interface{})
				current[part] = nextMap
			}
			current = nextMap
		}
	}
}

// ensureArray ensures the value at key is a slice, creating one if needed.
func ensureArray(m map[string]interface{}, key string) []interface{} {
	if arr, ok := m[key].([]interface{}); ok {
		return arr
	}
	arr := make([]interface{}, 0)
	m[key] = arr
	return arr
}

// isNumericIndex returns true if s represents an array index (non-negative integer).
func isNumericIndex(s string) bool {
	_, err := strconv.Atoi(s)
	return err == nil
}

// extractAllValues extracts ALL values from a Kubernetes object:
// - Structure metadata (_struct: prefix) for tree detection
// - All primitive/leaf values (strings, numbers, bools) with deep copy
// - Map keys and array lengths
func extractAllValues(obj interface{}, prefix string, fields map[string]interface{}) {
	switch v := obj.(type) {
	case map[string]interface{}:
		// Store map keys for this level (for tree detection)
		if prefix != "" {
			keys := make([]string, 0, len(v))
			for k := range v {
				keys = append(keys, k)
			}
			fields["_struct:"+prefix+".keys"] = keys
		}

		// Recurse into children
		for k, val := range v {
			childPath := k
			if prefix != "" {
				childPath = prefix + "." + k
			}
			extractAllValues(val, childPath, fields)
		}

	case []interface{}:
		// Store array length
		fields["_struct:"+prefix+".len"] = len(v)

		// Recurse into array elements with index in path
		for i, elem := range v {
			childPath := prefix + "." + strconv.Itoa(i)
			extractAllValues(elem, childPath, fields)
		}

	case string, bool, int, int64, float64, nil:
		// Store primitive values directly (these are already safe copies)
		if prefix != "" {
			fields[prefix] = v
		}
	}
}
