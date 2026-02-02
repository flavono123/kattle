package kube

import (
	"sync"

	"k8s.io/client-go/tools/cache"
)

// KeyOnlyStore implements cache.Store but only stores keys, not full objects.
// This drastically reduces memory usage by letting full Kubernetes objects be GC'd
// after their field values are extracted to FieldStore.
//
// The informer's event handlers (AddFunc, UpdateFunc, DeleteFunc) still receive
// full objects - they're passed directly from the API server response, not from the store.
// This store is only used for:
// - Tracking which keys exist (for ListKeys)
// - Supporting HasSynced detection via Replace
type KeyOnlyStore struct {
	mu      sync.RWMutex
	keys    map[string]struct{}
	keyFunc cache.KeyFunc
}

// NewKeyOnlyStore creates a new KeyOnlyStore with the given key function.
// Typically use cache.MetaNamespaceKeyFunc for Kubernetes objects.
func NewKeyOnlyStore(keyFunc cache.KeyFunc) *KeyOnlyStore {
	return &KeyOnlyStore{
		keys:    make(map[string]struct{}),
		keyFunc: keyFunc,
	}
}

// Add implements cache.Store - only stores the key, discards the object
func (s *KeyOnlyStore) Add(obj interface{}) error {
	key, err := s.keyFunc(obj)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.keys[key] = struct{}{}
	s.mu.Unlock()
	return nil
}

// Update implements cache.Store - only stores the key, discards the object
func (s *KeyOnlyStore) Update(obj interface{}) error {
	return s.Add(obj) // Same behavior for key-only storage
}

// Delete implements cache.Store - removes the key
func (s *KeyOnlyStore) Delete(obj interface{}) error {
	key, err := s.keyFunc(obj)
	if err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.keys, key)
	s.mu.Unlock()
	return nil
}

// List implements cache.Store - returns nil since we don't store objects
// Use FieldStore.ReconstructObject for actual data retrieval
func (s *KeyOnlyStore) List() []interface{} {
	return nil
}

// ListKeys implements cache.Store - returns all stored keys
func (s *KeyOnlyStore) ListKeys() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	keys := make([]string, 0, len(s.keys))
	for k := range s.keys {
		keys = append(keys, k)
	}
	return keys
}

// Get implements cache.Store - returns nil since we don't store objects
func (s *KeyOnlyStore) Get(obj interface{}) (item interface{}, exists bool, err error) {
	key, err := s.keyFunc(obj)
	if err != nil {
		return nil, false, err
	}
	return s.GetByKey(key)
}

// GetByKey implements cache.Store - returns nil but indicates if key exists
func (s *KeyOnlyStore) GetByKey(key string) (item interface{}, exists bool, err error) {
	s.mu.RLock()
	_, exists = s.keys[key]
	s.mu.RUnlock()
	return nil, exists, nil
}

// Replace implements cache.Store - replaces all keys (called during initial List sync)
func (s *KeyOnlyStore) Replace(list []interface{}, resourceVersion string) error {
	newKeys := make(map[string]struct{}, len(list))
	for _, obj := range list {
		key, err := s.keyFunc(obj)
		if err != nil {
			continue
		}
		newKeys[key] = struct{}{}
	}
	s.mu.Lock()
	s.keys = newKeys
	s.mu.Unlock()
	return nil
}

// Resync implements cache.Store - no-op for key-only store
func (s *KeyOnlyStore) Resync() error {
	return nil
}

// Count returns the number of keys in the store
func (s *KeyOnlyStore) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.keys)
}
