package kube

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewSQLStore_InMemory(t *testing.T) {
	store, err := NewSQLStore(":memory:")
	require.NoError(t, err)
	defer store.Close()

	assert.NotNil(t, store.db)
	assert.NotNil(t, store.stmtUpsert)
	assert.NotNil(t, store.stmtDelete)
	assert.NotNil(t, store.stmtGet)
}

func TestNewSQLStore_File(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewSQLStore(dbPath)
	require.NoError(t, err)
	defer store.Close()

	// Verify file was created
	_, err = os.Stat(dbPath)
	assert.NoError(t, err)
}

func TestSQLStore_Upsert_Insert(t *testing.T) {
	store, err := NewSQLStore(":memory:")
	require.NoError(t, err)
	defer store.Close()

	data := map[string]any{
		"metadata": map[string]any{
			"name":      "test-pod",
			"namespace": "default",
		},
		"status": map[string]any{
			"phase": "Running",
		},
	}
	jsonData, _ := json.Marshal(data)

	err = store.Upsert("ctx1/default/test-pod", "ctx1", "default", "test-pod", jsonData)
	require.NoError(t, err)

	// Verify data was inserted
	result, err := store.Get("ctx1/default/test-pod")
	require.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, "Running", result["status"].(map[string]any)["phase"])
}

func TestSQLStore_Upsert_Update(t *testing.T) {
	store, err := NewSQLStore(":memory:")
	require.NoError(t, err)
	defer store.Close()

	key := "ctx1/default/test-pod"

	// Insert initial data
	data1 := map[string]any{"status": map[string]any{"phase": "Pending"}}
	jsonData1, _ := json.Marshal(data1)
	err = store.Upsert(key, "ctx1", "default", "test-pod", jsonData1)
	require.NoError(t, err)

	// Update with new data
	data2 := map[string]any{"status": map[string]any{"phase": "Running"}}
	jsonData2, _ := json.Marshal(data2)
	err = store.Upsert(key, "ctx1", "default", "test-pod", jsonData2)
	require.NoError(t, err)

	// Verify data was updated
	result, err := store.Get(key)
	require.NoError(t, err)
	assert.Equal(t, "Running", result["status"].(map[string]any)["phase"])

	// Verify only one entry exists
	count, err := store.Count()
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestSQLStore_Delete(t *testing.T) {
	store, err := NewSQLStore(":memory:")
	require.NoError(t, err)
	defer store.Close()

	key := "ctx1/default/test-pod"
	data := map[string]any{"status": "Running"}
	jsonData, _ := json.Marshal(data)

	err = store.Upsert(key, "ctx1", "default", "test-pod", jsonData)
	require.NoError(t, err)

	err = store.Delete(key)
	require.NoError(t, err)

	// Verify data was deleted
	result, err := store.Get(key)
	require.NoError(t, err)
	assert.Nil(t, result)
}

func TestSQLStore_GetByKeys(t *testing.T) {
	store, err := NewSQLStore(":memory:")
	require.NoError(t, err)
	defer store.Close()

	// Insert multiple resources
	resources := []struct {
		key       string
		context   string
		namespace string
		name      string
		phase     string
	}{
		{"ctx1/default/pod1", "ctx1", "default", "pod1", "Running"},
		{"ctx1/default/pod2", "ctx1", "default", "pod2", "Pending"},
		{"ctx2/kube-system/pod3", "ctx2", "kube-system", "pod3", "Running"},
	}

	for _, r := range resources {
		data := map[string]any{"status": map[string]any{"phase": r.phase}}
		jsonData, _ := json.Marshal(data)
		err = store.Upsert(r.key, r.context, r.namespace, r.name, jsonData)
		require.NoError(t, err)
	}

	// Get subset of keys
	keys := []string{"ctx1/default/pod1", "ctx2/kube-system/pod3"}
	results, err := store.GetByKeys(keys)
	require.NoError(t, err)
	assert.Len(t, results, 2)

	// Verify _context is added
	contextFound := make(map[string]bool)
	for _, r := range results {
		ctx, ok := r["_context"].(string)
		assert.True(t, ok, "_context should be a string")
		contextFound[ctx] = true
	}
	assert.True(t, contextFound["ctx1"])
	assert.True(t, contextFound["ctx2"])
}

func TestSQLStore_GetByKeys_Empty(t *testing.T) {
	store, err := NewSQLStore(":memory:")
	require.NoError(t, err)
	defer store.Close()

	results, err := store.GetByKeys([]string{})
	require.NoError(t, err)
	assert.Nil(t, results)
}

func TestSQLStore_GetByKeys_NonExistent(t *testing.T) {
	store, err := NewSQLStore(":memory:")
	require.NoError(t, err)
	defer store.Close()

	results, err := store.GetByKeys([]string{"nonexistent/key/here"})
	require.NoError(t, err)
	assert.Empty(t, results)
}

func TestSQLStore_DeleteByContext(t *testing.T) {
	store, err := NewSQLStore(":memory:")
	require.NoError(t, err)
	defer store.Close()

	// Insert resources from multiple contexts
	resources := []struct {
		key     string
		context string
	}{
		{"ctx1/default/pod1", "ctx1"},
		{"ctx1/default/pod2", "ctx1"},
		{"ctx2/default/pod3", "ctx2"},
	}

	for _, r := range resources {
		data := map[string]any{"name": r.key}
		jsonData, _ := json.Marshal(data)
		err = store.Upsert(r.key, r.context, "default", "pod", jsonData)
		require.NoError(t, err)
	}

	// Delete all ctx1 resources
	err = store.DeleteByContext("ctx1")
	require.NoError(t, err)

	// Verify only ctx2 resources remain
	count, err := store.Count()
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	keys, err := store.List()
	require.NoError(t, err)
	assert.Len(t, keys, 1)
	assert.Equal(t, "ctx2/default/pod3", keys[0])
}

func TestSQLStore_List(t *testing.T) {
	store, err := NewSQLStore(":memory:")
	require.NoError(t, err)
	defer store.Close()

	// Insert resources
	for i := 0; i < 3; i++ {
		key := fmt.Sprintf("ctx/ns/pod%d", i)
		data := map[string]any{"index": i}
		jsonData, _ := json.Marshal(data)
		err = store.Upsert(key, "ctx", "ns", "pod", jsonData)
		require.NoError(t, err)
	}

	keys, err := store.List()
	require.NoError(t, err)
	assert.Len(t, keys, 3)
}

func TestSQLStore_Count(t *testing.T) {
	store, err := NewSQLStore(":memory:")
	require.NoError(t, err)
	defer store.Close()

	count, err := store.Count()
	require.NoError(t, err)
	assert.Equal(t, 0, count)

	// Insert resources
	for i := 0; i < 5; i++ {
		key := fmt.Sprintf("ctx/ns/pod%d", i)
		data := map[string]any{"index": i}
		jsonData, _ := json.Marshal(data)
		err = store.Upsert(key, "ctx", "ns", "pod", jsonData)
		require.NoError(t, err)
	}

	count, err = store.Count()
	require.NoError(t, err)
	assert.Equal(t, 5, count)
}

func TestSQLStore_Clear(t *testing.T) {
	store, err := NewSQLStore(":memory:")
	require.NoError(t, err)
	defer store.Close()

	// Insert resources
	for i := 0; i < 3; i++ {
		key := fmt.Sprintf("ctx/ns/pod%d", i)
		data := map[string]any{"index": i}
		jsonData, _ := json.Marshal(data)
		err = store.Upsert(key, "ctx", "ns", "pod", jsonData)
		require.NoError(t, err)
	}

	count, err := store.Count()
	require.NoError(t, err)
	assert.Equal(t, 3, count)

	err = store.Clear()
	require.NoError(t, err)

	count, err = store.Count()
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

func TestSQLStore_Close(t *testing.T) {
	store, err := NewSQLStore(":memory:")
	require.NoError(t, err)

	err = store.Close()
	require.NoError(t, err)

	// Operations after close should fail
	_, err = store.Count()
	assert.Error(t, err)
}

func TestSQLStore_ConcurrentAccess(t *testing.T) {
	store, err := NewSQLStore(":memory:")
	require.NoError(t, err)
	defer store.Close()

	var wg sync.WaitGroup
	errCh := make(chan error, 20)

	// Concurrent writes with unique keys using index
	for i := range 10 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			key := fmt.Sprintf("ctx/ns/pod-%d", idx)
			data := map[string]any{"index": idx}
			jsonData, _ := json.Marshal(data)
			if err := store.Upsert(key, "ctx", "ns", "pod", jsonData); err != nil {
				errCh <- fmt.Errorf("upsert error for index %d: %w", idx, err)
			}
		}(i)
	}

	// Wait for all writes to complete
	wg.Wait()

	// Check for any write errors
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}

	// Concurrent reads (after writes complete)
	var readWg sync.WaitGroup
	for range 10 {
		readWg.Add(1)
		go func() {
			defer readWg.Done()
			_, _ = store.Count()
			_, _ = store.List()
		}()
	}

	// Wait for all reads
	readWg.Wait()

	count, err := store.Count()
	require.NoError(t, err)
	assert.Equal(t, 10, count)
}

func TestSQLStore_GetRange(t *testing.T) {
	store, err := NewSQLStore(":memory:")
	require.NoError(t, err)
	defer store.Close()

	// Insert 10 resources with different creationTimestamps
	for i := range 10 {
		key := fmt.Sprintf("ctx/ns/pod-%02d", i)
		data := map[string]any{
			"metadata": map[string]any{
				"name":              fmt.Sprintf("pod-%02d", i),
				"namespace":         "ns",
				"creationTimestamp": fmt.Sprintf("2024-01-%02dT00:00:00Z", i+1),
			},
		}
		jsonData, _ := json.Marshal(data)
		err = store.Upsert(key, "ctx", "ns", fmt.Sprintf("pod-%02d", i), jsonData)
		require.NoError(t, err)
	}

	t.Run("basic range query", func(t *testing.T) {
		result, err := store.GetRange(0, 5, "", false)
		require.NoError(t, err)
		assert.Len(t, result, 5)
	})

	t.Run("range with offset", func(t *testing.T) {
		result, err := store.GetRange(5, 10, "", false)
		require.NoError(t, err)
		assert.Len(t, result, 5)
	})

	t.Run("range beyond data", func(t *testing.T) {
		result, err := store.GetRange(8, 15, "", false)
		require.NoError(t, err)
		assert.Len(t, result, 2) // only 2 rows left (8, 9)
	})

	t.Run("sorted by creationTimestamp desc", func(t *testing.T) {
		result, err := store.GetRange(0, 3, "metadata.creationTimestamp", true)
		require.NoError(t, err)
		assert.Len(t, result, 3)

		// Should be in descending order (pod-09, pod-08, pod-07)
		meta0 := result[0]["metadata"].(map[string]any)
		meta1 := result[1]["metadata"].(map[string]any)
		assert.True(t, meta0["creationTimestamp"].(string) > meta1["creationTimestamp"].(string))
	})

	t.Run("sorted by name asc", func(t *testing.T) {
		result, err := store.GetRange(0, 3, "metadata.name", false)
		require.NoError(t, err)
		assert.Len(t, result, 3)

		// Should be in ascending order (pod-00, pod-01, pod-02)
		meta0 := result[0]["metadata"].(map[string]any)
		meta1 := result[1]["metadata"].(map[string]any)
		assert.True(t, meta0["name"].(string) < meta1["name"].(string))
	})

	t.Run("includes _context and _key", func(t *testing.T) {
		result, err := store.GetRange(0, 1, "", false)
		require.NoError(t, err)
		assert.Len(t, result, 1)
		assert.Equal(t, "ctx", result[0]["_context"])
		assert.NotEmpty(t, result[0]["_key"])
	})
}

func TestSQLStore_GetRangeWithFilters(t *testing.T) {
	store, err := NewSQLStore(":memory:")
	require.NoError(t, err)
	defer store.Close()

	// Setup: insert test data with various namespaces and status
	testData := []struct {
		key       string
		namespace string
		name      string
		status    string
	}{
		{"ctx/ns-a/pod-1", "ns-a", "pod-1", "Running"},
		{"ctx/ns-a/pod-2", "ns-a", "pod-2", "Pending"},
		{"ctx/ns-b/pod-3", "ns-b", "pod-3", "Running"},
		{"ctx/ns-b/pod-4", "ns-b", "pod-4", "Failed"},
		{"ctx/ns-c/pod-5", "ns-c", "pod-5", "Running"},
	}

	for _, td := range testData {
		data, _ := json.Marshal(map[string]any{
			"metadata": map[string]any{
				"name":      td.name,
				"namespace": td.namespace,
			},
			"status": map[string]any{
				"phase": td.status,
			},
		})
		err := store.Upsert(td.key, "ctx", td.namespace, td.name, data)
		require.NoError(t, err)
	}

	t.Run("no filters returns all", func(t *testing.T) {
		params := QueryParams{Start: 0, End: 10}
		result, err := store.GetRangeWithFilters(params)
		require.NoError(t, err)
		assert.Len(t, result, 5)
	})

	t.Run("global search by name", func(t *testing.T) {
		params := QueryParams{Start: 0, End: 10, Search: "pod-1"}
		result, err := store.GetRangeWithFilters(params)
		require.NoError(t, err)
		assert.Len(t, result, 1)
		meta := result[0]["metadata"].(map[string]any)
		assert.Equal(t, "pod-1", meta["name"])
	})

	t.Run("global search by namespace", func(t *testing.T) {
		params := QueryParams{Start: 0, End: 10, Search: "ns-a"}
		result, err := store.GetRangeWithFilters(params)
		require.NoError(t, err)
		assert.Len(t, result, 2)
	})

	t.Run("global search by status.phase", func(t *testing.T) {
		params := QueryParams{Start: 0, End: 10, Search: "Running"}
		result, err := store.GetRangeWithFilters(params)
		require.NoError(t, err)
		assert.Len(t, result, 3) // pod-1, pod-3, pod-5
	})

	t.Run("filter by equals", func(t *testing.T) {
		params := QueryParams{
			Start: 0, End: 10,
			Filters: []Filter{
				{Field: "status.phase", Op: FilterOpEquals, Value: "Failed"},
			},
		}
		result, err := store.GetRangeWithFilters(params)
		require.NoError(t, err)
		assert.Len(t, result, 1)
		status := result[0]["status"].(map[string]any)
		assert.Equal(t, "Failed", status["phase"])
	})

	t.Run("filter by contains", func(t *testing.T) {
		params := QueryParams{
			Start: 0, End: 10,
			Filters: []Filter{
				{Field: "metadata.namespace", Op: FilterOpContains, Value: "-a"},
			},
		}
		result, err := store.GetRangeWithFilters(params)
		require.NoError(t, err)
		assert.Len(t, result, 2) // ns-a
	})

	t.Run("filter by startsWith", func(t *testing.T) {
		params := QueryParams{
			Start: 0, End: 10,
			Filters: []Filter{
				{Field: "metadata.name", Op: FilterOpStartsWith, Value: "pod-"},
			},
		}
		result, err := store.GetRangeWithFilters(params)
		require.NoError(t, err)
		assert.Len(t, result, 5) // all pods
	})

	t.Run("filter by in", func(t *testing.T) {
		params := QueryParams{
			Start: 0, End: 10,
			Filters: []Filter{
				{Field: "metadata.namespace", Op: FilterOpIn, Value: []string{"ns-a", "ns-c"}},
			},
		}
		result, err := store.GetRangeWithFilters(params)
		require.NoError(t, err)
		assert.Len(t, result, 3) // ns-a (2) + ns-c (1)
	})

	t.Run("multiple filters (AND)", func(t *testing.T) {
		params := QueryParams{
			Start: 0, End: 10,
			Filters: []Filter{
				{Field: "metadata.namespace", Op: FilterOpEquals, Value: "ns-a"},
				{Field: "status.phase", Op: FilterOpEquals, Value: "Running"},
			},
		}
		result, err := store.GetRangeWithFilters(params)
		require.NoError(t, err)
		assert.Len(t, result, 1) // only pod-1
	})

	t.Run("search with filter combined", func(t *testing.T) {
		params := QueryParams{
			Start:  0,
			End:    10,
			Search: "Running",
			Filters: []Filter{
				{Field: "metadata.namespace", Op: FilterOpEquals, Value: "ns-b"},
			},
		}
		result, err := store.GetRangeWithFilters(params)
		require.NoError(t, err)
		assert.Len(t, result, 1) // pod-3 (ns-b + Running)
	})

	t.Run("pagination with filters", func(t *testing.T) {
		params := QueryParams{
			Start:  0,
			End:    2,
			Search: "Running",
		}
		result, err := store.GetRangeWithFilters(params)
		require.NoError(t, err)
		assert.Len(t, result, 2) // first 2 of 3 Running pods
	})

	t.Run("sorting with filters", func(t *testing.T) {
		params := QueryParams{
			Start:     0,
			End:       10,
			Search:    "Running",
			SortField: "metadata.name",
			SortDesc:  true,
		}
		result, err := store.GetRangeWithFilters(params)
		require.NoError(t, err)
		assert.Len(t, result, 3)
		// Descending: pod-5, pod-3, pod-1
		meta0 := result[0]["metadata"].(map[string]any)
		meta2 := result[2]["metadata"].(map[string]any)
		assert.Equal(t, "pod-5", meta0["name"])
		assert.Equal(t, "pod-1", meta2["name"])
	})
}

func TestSQLStore_CountWithFilters(t *testing.T) {
	store, err := NewSQLStore(":memory:")
	require.NoError(t, err)
	defer store.Close()

	// Setup: insert test data
	for i := 0; i < 10; i++ {
		ns := "ns-a"
		if i >= 6 {
			ns = "ns-b"
		}
		data, _ := json.Marshal(map[string]any{
			"metadata": map[string]any{
				"name":      fmt.Sprintf("pod-%d", i),
				"namespace": ns,
			},
		})
		key := fmt.Sprintf("ctx/%s/pod-%d", ns, i)
		err := store.Upsert(key, "ctx", ns, fmt.Sprintf("pod-%d", i), data)
		require.NoError(t, err)
	}

	t.Run("no filters", func(t *testing.T) {
		params := QueryParams{}
		count, err := store.CountWithFilters(params)
		require.NoError(t, err)
		assert.Equal(t, 10, count)
	})

	t.Run("with search", func(t *testing.T) {
		params := QueryParams{Search: "ns-a"}
		count, err := store.CountWithFilters(params)
		require.NoError(t, err)
		assert.Equal(t, 6, count) // pod-0 to pod-5
	})

	t.Run("with filter", func(t *testing.T) {
		params := QueryParams{
			Filters: []Filter{
				{Field: "metadata.namespace", Op: FilterOpEquals, Value: "ns-b"},
			},
		}
		count, err := store.CountWithFilters(params)
		require.NoError(t, err)
		assert.Equal(t, 4, count) // pod-6 to pod-9
	})
}

func TestEscapeLike(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"normal", "normal"},
		{"with%percent", `with\%percent`},
		{"with_underscore", `with\_underscore`},
		{`with\backslash`, `with\\backslash`},
		{"all%_\\special", `all\%\_\\special`},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := escapeLike(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}
