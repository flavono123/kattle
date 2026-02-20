package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestExtractFieldsForSQL_EssentialFields(t *testing.T) {
	obj := &unstructured.Unstructured{
		Object: map[string]any{
			"metadata": map[string]any{
				"name":              "test-pod",
				"namespace":        "default",
				"uid":              "abc-123",
				"resourceVersion":  "999",
				"creationTimestamp": "2025-01-01T00:00:00Z",
				"labels": map[string]any{
					"app": "web",
				},
			},
		},
	}

	result := extractFieldsForSQL(obj, nil)

	// Essential fields should always be extracted
	meta, ok := result["metadata"].(map[string]any)
	assert.True(t, ok)
	assert.Equal(t, "test-pod", meta["name"])
	assert.Equal(t, "default", meta["namespace"])
	assert.Equal(t, "abc-123", meta["uid"])
	assert.Equal(t, "999", meta["resourceVersion"])
	assert.Equal(t, "2025-01-01T00:00:00Z", meta["creationTimestamp"])

	labels, ok := meta["labels"].(map[string]any)
	assert.True(t, ok)
	assert.Equal(t, "web", labels["app"])
}

func TestExtractFieldsForSQL_SelectedFields(t *testing.T) {
	obj := &unstructured.Unstructured{
		Object: map[string]any{
			"metadata": map[string]any{
				"name":      "test-pod",
				"namespace": "default",
			},
			"status": map[string]any{
				"phase": "Running",
			},
			"spec": map[string]any{
				"replicas": float64(3),
			},
		},
	}

	result := extractFieldsForSQL(obj, []string{"status.phase", "spec.replicas"})

	status, ok := result["status"].(map[string]any)
	assert.True(t, ok)
	assert.Equal(t, "Running", status["phase"])

	spec, ok := result["spec"].(map[string]any)
	assert.True(t, ok)
	assert.Equal(t, float64(3), spec["replicas"])
}

func TestExtractFieldsForSQL_MissingFieldStoredAsNull(t *testing.T) {
	obj := &unstructured.Unstructured{
		Object: map[string]any{
			"metadata": map[string]any{
				"name":      "test-pod",
				"namespace": "default",
			},
		},
	}

	result := extractFieldsForSQL(obj, []string{"status.phase"})

	// status.phase doesn't exist → explicit null stored
	status, ok := result["status"].(map[string]any)
	assert.True(t, ok)

	val, keyExists := status["phase"]
	assert.True(t, keyExists, "key should exist for explicit null convention")
	assert.Nil(t, val, "missing field should be stored as nil (JSON null)")
}

func TestExtractFieldsForSQL_WildcardPath(t *testing.T) {
	obj := &unstructured.Unstructured{
		Object: map[string]any{
			"metadata": map[string]any{
				"name":      "test-pod",
				"namespace": "default",
				"labels": map[string]any{
					"app":  "web",
					"tier": "frontend",
				},
			},
		},
	}

	result := extractFieldsForSQL(obj, []string{"metadata.labels.*"})

	meta := result["metadata"].(map[string]any)
	labels, ok := meta["labels"].(map[string]any)
	assert.True(t, ok, "wildcard should extract the parent map")
	assert.Equal(t, "web", labels["app"])
	assert.Equal(t, "frontend", labels["tier"])
}

func TestExtractFieldsForSQL_WildcardMissingParent(t *testing.T) {
	obj := &unstructured.Unstructured{
		Object: map[string]any{
			"metadata": map[string]any{
				"name":      "test-pod",
				"namespace": "default",
			},
			// no "spec" at all
		},
	}

	result := extractFieldsForSQL(obj, []string{"spec.containers.*"})

	spec, ok := result["spec"].(map[string]any)
	assert.True(t, ok)
	val, keyExists := spec["containers"]
	assert.True(t, keyExists, "wildcard parent should exist as explicit null")
	assert.Nil(t, val)
}

func TestExtractFieldsForSQL_DuplicateWildcardParent(t *testing.T) {
	obj := &unstructured.Unstructured{
		Object: map[string]any{
			"metadata": map[string]any{
				"name":      "test-pod",
				"namespace": "default",
				"labels": map[string]any{
					"app": "web",
				},
			},
		},
	}

	// Two wildcard paths with same parent — should not cause duplicate extraction
	result := extractFieldsForSQL(obj, []string{
		"metadata.labels.*.app",
		"metadata.labels.*.tier",
	})

	meta := result["metadata"].(map[string]any)
	labels, ok := meta["labels"].(map[string]any)
	assert.True(t, ok)
	assert.Equal(t, "web", labels["app"])
}

func TestSetNestedField(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		value    any
		expected map[string]any
	}{
		{
			name:     "single level",
			path:     "name",
			value:    "test",
			expected: map[string]any{"name": "test"},
		},
		{
			name:  "nested path",
			path:  "metadata.name",
			value: "test-pod",
			expected: map[string]any{
				"metadata": map[string]any{"name": "test-pod"},
			},
		},
		{
			name:  "deep nested path",
			path:  "spec.containers.resources",
			value: "128Mi",
			expected: map[string]any{
				"spec": map[string]any{
					"containers": map[string]any{
						"resources": "128Mi",
					},
				},
			},
		},
		{
			name:  "nil value (explicit null)",
			path:  "status.phase",
			value: nil,
			expected: map[string]any{
				"status": map[string]any{"phase": nil},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj := make(map[string]any)
			setNestedField(obj, tt.path, tt.value)
			assert.Equal(t, tt.expected, obj)
		})
	}
}

func TestSplitPath(t *testing.T) {
	assert.Equal(t, []string{"metadata", "name"}, splitPath("metadata.name"))
	assert.Equal(t, []string{"name"}, splitPath("name"))
	assert.Equal(t, []string{"spec", "containers", "*", "image"}, splitPath("spec.containers.*.image"))
}
