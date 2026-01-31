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

	// stringPool interns frequently repeated string values to reduce memory.
	// e.g., status.phase = "Running" is stored once, referenced by all pods.
	stringPool sync.Map // map[string]string

	// reconstructedCache caches reconstructed objects to avoid repeated allocations.
	// Key: resource key, Value: reconstructed map[string]interface{}
	// Invalidated on Update/Delete. Thread-safe via sync.Map.
	// NOTE: Cached objects are shared across callers - do not modify returned values.
	reconstructedCache sync.Map // map[string]map[string]interface{}
}

// Essential metadata fields that are always stored
// NOTE: metadata.annotations is EXCLUDED to reduce memory usage.
// Annotations like "kubectl.kubernetes.io/last-applied-configuration" can be
// very large (entire pod spec as JSON). If specific annotations are needed,
// users can select them in DFT - they'll be added to selectedFields.
var defaultEssentialFields = []string{
	"metadata.name",
	"metadata.namespace",
	"metadata.uid",
	"metadata.resourceVersion",
	"metadata.creationTimestamp",
	"metadata.labels",
	// "metadata.annotations", // REMOVED: causes WebView memory explosion (2GB+ for 6k pods)
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
// Extracts: structure metadata (always), essential fields, and selected fields.
// If no selected fields are set, extracts all values (backward compatible).
// Invalidates any cached reconstruction for this key.
func (fs *FieldStore) Update(key string, obj *unstructured.Unstructured) {
	// Invalidate cache first (lock-free via sync.Map)
	fs.reconstructedCache.Delete(key)

	fs.mu.Lock()
	defer fs.mu.Unlock()

	fields := make(map[string]interface{})

	// Build extraction set: essential + selected fields
	extractSet := make(map[string]struct{})
	for _, f := range fs.essentialFields {
		extractSet[f] = struct{}{}
	}
	for _, f := range fs.selectedFields {
		extractSet[f] = struct{}{}
	}

	// If no fields specified, fall back to extracting all (backward compatible)
	if len(extractSet) == 0 {
		extractAllValues(obj.Object, "", fields, fs.internString)
	} else {
		// Extract structure metadata (always) + specified fields only
		extractSelectiveValues(obj.Object, "", fields, extractSet, fs.internString)
	}

	fs.data[key] = fields
}

// Delete removes a resource from the store.
// Invalidates any cached reconstruction for this key.
func (fs *FieldStore) Delete(key string) {
	// Invalidate cache first (lock-free via sync.Map)
	fs.reconstructedCache.Delete(key)

	fs.mu.Lock()
	defer fs.mu.Unlock()

	delete(fs.data, key)
}

// Clear removes all resources from the store.
// Also clears the reconstruction cache.
func (fs *FieldStore) Clear() {
	// Clear cache first (replace with new empty sync.Map)
	fs.reconstructedCache = sync.Map{}

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

// internString returns a canonical string from the pool.
// If the string is not in the pool, it's added and returned.
// This reduces memory by sharing identical strings across resources.
// Only interns short strings (<=64 chars) to avoid pool bloat from unique long values.
func (fs *FieldStore) internString(s string) string {
	// Don't intern very long strings (likely unique values like UIDs, timestamps)
	if len(s) > 64 {
		return s
	}

	if interned, ok := fs.stringPool.Load(s); ok {
		return interned.(string)
	}
	fs.stringPool.Store(s, s)
	return s
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
//
// Performance: Results are cached and reused until Update/Delete is called.
// WARNING: The returned map is shared across callers - do not modify it.
func (fs *FieldStore) ReconstructObject(key string) map[string]interface{} {
	// Fast path: check cache first (lock-free via sync.Map)
	if cached, ok := fs.reconstructedCache.Load(key); ok {
		return cached.(map[string]interface{})
	}

	fs.mu.RLock()
	fields, ok := fs.data[key]
	if !ok {
		fs.mu.RUnlock()
		return nil
	}

	// Count non-structure fields to estimate map capacity
	fieldCount := 0
	for path := range fields {
		if !strings.HasPrefix(path, "_struct:") {
			fieldCount++
		}
	}

	// Build a nested object from flat field paths
	// Pre-allocate with estimated capacity (top-level keys ~10-20% of total fields)
	obj := make(map[string]interface{}, fieldCount/5+4)

	for path, value := range fields {
		// Skip structure metadata (not part of the actual object)
		if strings.HasPrefix(path, "_struct:") {
			continue
		}
		setFieldByPath(obj, path, value)
	}
	fs.mu.RUnlock()

	// Cache the result for subsequent calls
	// Use LoadOrStore to handle concurrent reconstruction attempts
	actual, _ := fs.reconstructedCache.LoadOrStore(key, obj)
	return actual.(map[string]interface{})
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

// pathPartsPool reduces allocations for path splitting in setFieldByPath.
// Each []string slice is reused for parsing dot-separated paths.
var pathPartsPool = sync.Pool{
	New: func() interface{} {
		// Pre-allocate for typical path depth (e.g., "spec.containers.0.ports.0.containerPort")
		parts := make([]string, 0, 8)
		return &parts
	},
}

// setFieldByPath sets a value in a nested structure using dot notation.
// Handles both maps and arrays - numeric path segments create array indices.
func setFieldByPath(obj map[string]interface{}, path string, value interface{}) {
	if path == "" {
		return
	}

	// Get a slice from the pool and split path into it
	partsPtr := pathPartsPool.Get().(*[]string)
	parts := (*partsPtr)[:0] // Reset length, keep capacity

	// Manual split to avoid allocation from strings.Split
	start := 0
	for i := 0; i <= len(path); i++ {
		if i == len(path) || path[i] == '.' {
			if i > start {
				parts = append(parts, path[start:i])
			}
			start = i + 1
		}
	}

	if len(parts) == 0 {
		*partsPtr = parts
		pathPartsPool.Put(partsPtr)
		return
	}

	setNestedValue(obj, parts, value)

	// Return to pool
	*partsPtr = parts
	pathPartsPool.Put(partsPtr)
}

// setNestedValue recursively sets a value in a nested map/array structure.
// Optimized to minimize allocations by pre-sizing maps and arrays.
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
			idx, _ := strconv.Atoi(nextPart)

			// Get or create array with sufficient capacity
			arr := ensureArrayWithCapacity(current, part, idx+1)

			// Check if we're setting a leaf value or nested structure
			if i+2 < len(parts) {
				// More path segments after index - ensure element is a map
				if arr[idx] == nil {
					// Pre-allocate with small capacity for nested object
					arr[idx] = make(map[string]interface{}, 4)
				}
				elemMap, ok := arr[idx].(map[string]interface{})
				if !ok {
					elemMap = make(map[string]interface{}, 4)
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
				// Pre-allocate with small capacity for nested object
				current[part] = make(map[string]interface{}, 4)
			}
			nextMap, ok := current[part].(map[string]interface{})
			if !ok {
				nextMap = make(map[string]interface{}, 4)
				current[part] = nextMap
			}
			current = nextMap
		}
	}
}

// ensureArrayWithCapacity ensures the value at key is a slice with at least minLen elements.
// Creates or extends the array as needed, minimizing reallocations.
func ensureArrayWithCapacity(m map[string]interface{}, key string, minLen int) []interface{} {
	if arr, ok := m[key].([]interface{}); ok {
		// Extend if needed
		if len(arr) < minLen {
			// Grow to exact size needed (we know the final size)
			for len(arr) < minLen {
				arr = append(arr, nil)
			}
			m[key] = arr
		}
		return arr
	}
	// Create new array with exact capacity needed
	arr := make([]interface{}, minLen)
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
// intern is an optional function to intern repeated strings for memory efficiency.
func extractAllValues(obj interface{}, prefix string, fields map[string]interface{}, intern func(string) string) {
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
			extractAllValues(val, childPath, fields, intern)
		}

	case []interface{}:
		// Store array length
		fields["_struct:"+prefix+".len"] = len(v)

		// Recurse into array elements with index in path
		for i, elem := range v {
			childPath := prefix + "." + strconv.Itoa(i)
			extractAllValues(elem, childPath, fields, intern)
		}

	case string:
		// Intern string values to reduce memory for repeated values
		if prefix != "" {
			fields[prefix] = intern(v)
		}

	case bool, int, int64, float64, nil:
		// Store primitive values directly (these are already safe copies)
		if prefix != "" {
			fields[prefix] = v
		}
	}
}

// extractSelectiveValues extracts structure metadata and selected field values only.
// This significantly reduces memory usage by not storing unneeded leaf values.
// Structure metadata (_struct:*) is only extracted for paths that are on the way to
// or under selected fields, preventing memory explosion from deeply nested structures.
// Leaf values are only extracted if the path matches or is under a selected field.
// intern is an optional function to intern repeated strings for memory efficiency.
func extractSelectiveValues(obj interface{}, prefix string, fields map[string]interface{}, extractSet map[string]struct{}, intern func(string) string) {
	switch v := obj.(type) {
	case map[string]interface{}:
		// Store map keys only if this path is relevant to extractSet
		if prefix != "" && isUnderExtractPath(prefix, extractSet) {
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
			extractSelectiveValues(val, childPath, fields, extractSet, intern)
		}

	case []interface{}:
		// Store array length only if this path is relevant to extractSet
		if isUnderExtractPath(prefix, extractSet) {
			fields["_struct:"+prefix+".len"] = len(v)
		}

		// Recurse into array elements with index in path
		for i, elem := range v {
			childPath := prefix + "." + strconv.Itoa(i)
			extractSelectiveValues(elem, childPath, fields, extractSet, intern)
		}

	case string:
		// Store string values ONLY if path should be extracted, intern for memory efficiency
		if prefix != "" && shouldExtractPath(prefix, extractSet) {
			fields[prefix] = intern(v)
		}

	case bool, int, int64, float64, nil:
		// Store primitive values ONLY if path should be extracted
		if prefix != "" && shouldExtractPath(prefix, extractSet) {
			fields[prefix] = v
		}
	}
}

// shouldExtractPath checks if a path should have its value extracted.
// Returns true if:
// 1. Exact match: path is in extractSet
// 2. Child of selected: a prefix of path is in extractSet (e.g., "metadata.labels.app" when "metadata.labels" is selected)
func shouldExtractPath(path string, extractSet map[string]struct{}) bool {
	// Exact match
	if _, ok := extractSet[path]; ok {
		return true
	}

	// Check if any selected field is a prefix of this path
	// e.g., if "metadata.labels" is selected, extract "metadata.labels.app"
	for selected := range extractSet {
		if strings.HasPrefix(path, selected+".") {
			return true
		}
	}

	return false
}

// isUnderExtractPath checks if a path is relevant for structure metadata storage.
// Returns true if:
// 1. Exact match: path is in extractSet
// 2. Ancestor: path is a prefix of any extractSet entry (need to traverse through this path)
// 3. Descendant: any extractSet entry is a prefix of path (we're under a selected field)
//
// This limits structure metadata (_struct:*.keys, _struct:*.len) to only paths
// that are on the way to or under selected fields, preventing memory explosion
// from deeply nested structures like spec.containers[].env[].
func isUnderExtractPath(path string, extractSet map[string]struct{}) bool {
	// Exact match
	if _, ok := extractSet[path]; ok {
		return true
	}

	pathWithDot := path + "."

	for selected := range extractSet {
		// Check if path is an ancestor of selected (e.g., path="spec" and selected="spec.nodeName")
		if strings.HasPrefix(selected, pathWithDot) {
			return true
		}
		// Check if path is a descendant of selected (e.g., path="metadata.labels.app" and selected="metadata.labels")
		if strings.HasPrefix(path, selected+".") {
			return true
		}
	}

	return false
}
