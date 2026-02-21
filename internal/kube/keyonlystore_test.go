package kube

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/tools/cache"
)

// helper to create a minimal unstructured object with namespace/name
func makeUnstructured(namespace, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]any{
			"metadata": map[string]any{
				"name":      name,
				"namespace": namespace,
			},
		},
	}
}

func TestKeyOnlyStore_InterfaceCompliance(t *testing.T) {
	var _ cache.Store = (*KeyOnlyStore)(nil)
}

func TestKeyOnlyStore_AddAndListKeys(t *testing.T) {
	store := NewKeyOnlyStore(cache.MetaNamespaceKeyFunc)

	obj := makeUnstructured("default", "pod-1")
	require.NoError(t, store.Add(obj))

	keys := store.ListKeys()
	assert.Equal(t, []string{"default/pod-1"}, keys)
	assert.Equal(t, 1, store.Count())
}

func TestKeyOnlyStore_UpdateIsSameAsAdd(t *testing.T) {
	store := NewKeyOnlyStore(cache.MetaNamespaceKeyFunc)

	obj := makeUnstructured("default", "pod-1")
	require.NoError(t, store.Add(obj))
	require.NoError(t, store.Update(obj))

	assert.Equal(t, 1, store.Count())
}

func TestKeyOnlyStore_Delete(t *testing.T) {
	store := NewKeyOnlyStore(cache.MetaNamespaceKeyFunc)

	obj := makeUnstructured("default", "pod-1")
	require.NoError(t, store.Add(obj))
	assert.Equal(t, 1, store.Count())

	require.NoError(t, store.Delete(obj))
	assert.Equal(t, 0, store.Count())

	keys := store.ListKeys()
	assert.Empty(t, keys)
}

func TestKeyOnlyStore_GetByKey(t *testing.T) {
	store := NewKeyOnlyStore(cache.MetaNamespaceKeyFunc)

	obj := makeUnstructured("default", "pod-1")
	require.NoError(t, store.Add(obj))

	// Existing key: returns nil object but exists=true
	item, exists, err := store.GetByKey("default/pod-1")
	require.NoError(t, err)
	assert.True(t, exists)
	assert.Nil(t, item, "KeyOnlyStore never returns objects")

	// Non-existing key: returns nil object and exists=false
	item, exists, err = store.GetByKey("default/pod-2")
	require.NoError(t, err)
	assert.False(t, exists)
	assert.Nil(t, item)
}

func TestKeyOnlyStore_Get(t *testing.T) {
	store := NewKeyOnlyStore(cache.MetaNamespaceKeyFunc)

	obj := makeUnstructured("default", "pod-1")
	require.NoError(t, store.Add(obj))

	item, exists, err := store.Get(obj)
	require.NoError(t, err)
	assert.True(t, exists)
	assert.Nil(t, item)
}

func TestKeyOnlyStore_ListReturnsNil(t *testing.T) {
	store := NewKeyOnlyStore(cache.MetaNamespaceKeyFunc)

	obj := makeUnstructured("default", "pod-1")
	require.NoError(t, store.Add(obj))

	// List always returns nil — objects are not stored
	assert.Nil(t, store.List())
}

func TestKeyOnlyStore_Replace(t *testing.T) {
	store := NewKeyOnlyStore(cache.MetaNamespaceKeyFunc)

	// Pre-populate with some keys
	require.NoError(t, store.Add(makeUnstructured("default", "old-pod")))
	assert.Equal(t, 1, store.Count())

	// Replace atomically swaps all keys
	newObjs := []any{
		makeUnstructured("default", "pod-a"),
		makeUnstructured("kube-system", "pod-b"),
		makeUnstructured("default", "pod-c"),
	}
	require.NoError(t, store.Replace(newObjs, "12345"))

	assert.Equal(t, 3, store.Count())

	// Old key is gone
	_, exists, _ := store.GetByKey("default/old-pod")
	assert.False(t, exists)

	// New keys are present
	_, exists, _ = store.GetByKey("default/pod-a")
	assert.True(t, exists)
	_, exists, _ = store.GetByKey("kube-system/pod-b")
	assert.True(t, exists)
	_, exists, _ = store.GetByKey("default/pod-c")
	assert.True(t, exists)
}

func TestKeyOnlyStore_Resync(t *testing.T) {
	store := NewKeyOnlyStore(cache.MetaNamespaceKeyFunc)
	assert.NoError(t, store.Resync(), "Resync should be a no-op")
}

func TestKeyOnlyStore_ConcurrentAccess(t *testing.T) {
	store := NewKeyOnlyStore(cache.MetaNamespaceKeyFunc)

	const numWriters = 10
	const numOpsPerWriter = 100

	var wg sync.WaitGroup

	// Concurrent writers: Add
	for w := range numWriters {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range numOpsPerWriter {
				obj := makeUnstructured("ns", fmt.Sprintf("pod-%d-%d", w, i))
				_ = store.Add(obj)
			}
		}()
	}

	// Concurrent readers: ListKeys + Count
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range numOpsPerWriter {
				_ = store.ListKeys()
				_ = store.Count()
			}
		}()
	}

	// Concurrent deletes
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := range numOpsPerWriter {
			obj := makeUnstructured("ns", fmt.Sprintf("pod-0-%d", i))
			_ = store.Delete(obj)
		}
	}()

	wg.Wait()

	// Just verify no panics occurred and state is consistent
	count := store.Count()
	keys := store.ListKeys()
	assert.Equal(t, count, len(keys))
}
