package kube

import (
	"reflect"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var _ = Describe("FieldStore", func() {
	var (
		fs      *FieldStore
		testObj *unstructured.Unstructured
	)

	BeforeEach(func() {
		fs = NewFieldStore()

		testObj = &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "v1",
				"kind":       "Pod",
				"metadata": map[string]interface{}{
					"name":              "test-pod",
					"namespace":         "default",
					"uid":               "abc-123",
					"resourceVersion":   "12345",
					"creationTimestamp": "2024-01-01T00:00:00Z",
					"labels": map[string]interface{}{
						"app": "nginx",
						"env": "prod",
					},
					"annotations": map[string]interface{}{
						"note": "test annotation",
					},
					"ownerReferences": []interface{}{
						map[string]interface{}{
							"kind": "ReplicaSet",
							"name": "test-rs",
						},
					},
				},
				"spec": map[string]interface{}{
					"containers": []interface{}{
						map[string]interface{}{
							"name":  "nginx",
							"image": "nginx:1.19",
							"ports": []interface{}{
								map[string]interface{}{
									"containerPort": float64(80),
								},
							},
						},
						map[string]interface{}{
							"name":  "sidecar",
							"image": "busybox",
						},
					},
					"nodeName": "node-1",
				},
				"status": map[string]interface{}{
					"phase": "Running",
					"conditions": []interface{}{
						map[string]interface{}{
							"type":   "Ready",
							"status": "True",
						},
					},
				},
			},
		}
	})

	Describe("Basic operations", func() {
		It("should store and retrieve essential fields", func() {
			key := "default/test-pod"
			fs.Update(key, testObj)

			fields := fs.Get(key)
			Expect(fields).NotTo(BeNil())
			Expect(fields["metadata.name"]).To(Equal("test-pod"))
			Expect(fields["metadata.namespace"]).To(Equal("default"))
			Expect(fields["metadata.uid"]).To(Equal("abc-123"))
		})

		It("should return nil for non-existent key", func() {
			fields := fs.Get("nonexistent/key")
			Expect(fields).To(BeNil())
		})

		It("should delete resources", func() {
			key := "default/test-pod"
			fs.Update(key, testObj)
			Expect(fs.Get(key)).NotTo(BeNil())

			fs.Delete(key)
			Expect(fs.Get(key)).To(BeNil())
		})

		It("should list all keys", func() {
			fs.Update("ns1/pod1", testObj)
			fs.Update("ns2/pod2", testObj)

			keys := fs.List()
			Expect(keys).To(HaveLen(2))
			Expect(keys).To(ContainElements("ns1/pod1", "ns2/pod2"))
		})

		It("should clear all data", func() {
			fs.Update("ns1/pod1", testObj)
			fs.Update("ns2/pod2", testObj)
			Expect(fs.Count()).To(Equal(2))

			fs.Clear()
			Expect(fs.Count()).To(Equal(0))
		})
	})

	Describe("GetField", func() {
		It("should retrieve specific field values", func() {
			key := "default/test-pod"
			fs.Update(key, testObj)

			val, found := fs.GetField(key, "metadata.name")
			Expect(found).To(BeTrue())
			Expect(val).To(Equal("test-pod"))
		})

		It("should return false for non-existent field", func() {
			key := "default/test-pod"
			fs.Update(key, testObj)

			_, found := fs.GetField(key, "nonexistent.field")
			Expect(found).To(BeFalse())
		})
	})

	Describe("Field extraction", func() {
		It("should extract selected fields at full paths", func() {
			key := "default/test-pod"
			// Set selected fields before Update to extract non-essential fields
			fs.SetSelectedFields([]string{"spec.nodeName", "status.phase"})
			fs.Update(key, testObj)

			fields := fs.Get(key)
			// Selected leaf values are stored at their full path
			Expect(fields["spec.nodeName"]).To(Equal("node-1"))
			Expect(fields["status.phase"]).To(Equal("Running"))
		})

		It("should extract nested map values at full paths", func() {
			key := "default/test-pod"
			fs.Update(key, testObj)

			fields := fs.Get(key)
			// Labels are stored as individual keys, not as a nested map
			Expect(fields["metadata.labels.app"]).To(Equal("nginx"))
			Expect(fields["metadata.labels.env"]).To(Equal("prod"))
		})
	})

	Describe("Structure metadata extraction", func() {
		It("should extract array lengths for selected fields", func() {
			key := "default/test-pod"
			// Set selected fields to include spec.containers so structure metadata is extracted
			fs.SetSelectedFields([]string{"spec.containers"})
			fs.Update(key, testObj)

			fields := fs.Get(key)
			// spec.containers is an array of 2 elements
			Expect(fields["_struct:spec.containers.len"]).To(Equal(2))
		})

		It("should extract map keys for selected fields", func() {
			key := "default/test-pod"
			// Set selected fields to include spec.* so structure metadata for spec is extracted
			fs.SetSelectedFields([]string{"spec.containers", "spec.nodeName"})
			fs.Update(key, testObj)

			fields := fs.Get(key)
			// spec level should have keys since it's an ancestor of selected fields
			specKeys, ok := fields["_struct:spec.keys"].([]string)
			Expect(ok).To(BeTrue())
			Expect(specKeys).To(ContainElements("containers", "nodeName"))
		})

		It("should NOT extract structure metadata for non-selected paths", func() {
			key := "default/test-pod"
			// Only essential fields are selected (metadata.*)
			// spec.* is not selected, so its structure metadata should NOT be stored
			fs.Update(key, testObj)

			fields := fs.Get(key)
			// spec.containers structure metadata should NOT exist
			_, hasSpecContainersLen := fields["_struct:spec.containers.len"]
			Expect(hasSpecContainersLen).To(BeFalse())
			// But metadata structure should exist (essential fields)
			_, hasMetadataKeys := fields["_struct:metadata.keys"]
			Expect(hasMetadataKeys).To(BeTrue())
		})
	})

	Describe("DetectStructureChange", func() {
		It("should detect array length changes", func() {
			oldFields := map[string]interface{}{
				"_struct:spec.containers.len": 2,
			}
			newFields := map[string]interface{}{
				"_struct:spec.containers.len": 3,
			}

			changed := fs.DetectStructureChange(oldFields, newFields)
			Expect(changed).To(BeTrue())
		})

		It("should detect new structure fields", func() {
			oldFields := map[string]interface{}{}
			newFields := map[string]interface{}{
				"_struct:spec.volumes.len": 1,
			}

			changed := fs.DetectStructureChange(oldFields, newFields)
			Expect(changed).To(BeTrue())
		})

		It("should detect removed structure fields", func() {
			oldFields := map[string]interface{}{
				"_struct:spec.volumes.len": 1,
			}
			newFields := map[string]interface{}{}

			changed := fs.DetectStructureChange(oldFields, newFields)
			Expect(changed).To(BeTrue())
		})

		It("should return false when structure unchanged", func() {
			oldFields := map[string]interface{}{
				"_struct:spec.containers.len": 2,
				"metadata.name":               "test",
			}
			newFields := map[string]interface{}{
				"_struct:spec.containers.len": 2,
				"metadata.name":               "test-changed",
			}

			changed := fs.DetectStructureChange(oldFields, newFields)
			Expect(changed).To(BeFalse())
		})
	})

	Describe("GetMaxArrayLength", func() {
		It("should return max array length across all resources", func() {
			// Set selected fields to include spec.containers so structure metadata is extracted
			fs.SetSelectedFields([]string{"spec.containers"})

			// Create objects with different container counts
			pod1 := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"metadata": map[string]interface{}{
						"name": "pod-1", "namespace": "default",
					},
					"spec": map[string]interface{}{
						"containers": []interface{}{
							map[string]interface{}{"name": "c1"},
						},
					},
				},
			}
			pod2 := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"metadata": map[string]interface{}{
						"name": "pod-2", "namespace": "default",
					},
					"spec": map[string]interface{}{
						"containers": []interface{}{
							map[string]interface{}{"name": "c1"},
							map[string]interface{}{"name": "c2"},
							map[string]interface{}{"name": "c3"},
						},
					},
				},
			}

			fs.Update("default/pod-1", pod1)
			fs.Update("default/pod-2", pod2)

			maxLen := fs.GetMaxArrayLength("spec.containers")
			Expect(maxLen).To(Equal(3))
		})

		It("should return 0 for non-existent path", func() {
			// Set selected fields to include spec.containers to store some structure metadata
			fs.SetSelectedFields([]string{"spec.containers"})
			fs.Update("default/test-pod", testObj)

			maxLen := fs.GetMaxArrayLength("spec.nonexistent")
			Expect(maxLen).To(Equal(0))
		})

		It("should return 0 for empty store", func() {
			maxLen := fs.GetMaxArrayLength("spec.containers")
			Expect(maxLen).To(Equal(0))
		})
	})

	Describe("GetDistinctMapKeys", func() {
		It("should return all unique map keys across resources", func() {
			// Create objects with different labels
			pod1 := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"metadata": map[string]interface{}{
						"name": "pod-1", "namespace": "default",
						"labels": map[string]interface{}{
							"app": "nginx",
							"env": "prod",
						},
					},
				},
			}
			pod2 := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"metadata": map[string]interface{}{
						"name": "pod-2", "namespace": "default",
						"labels": map[string]interface{}{
							"app":     "redis",
							"version": "1.0",
						},
					},
				},
			}

			fs.Update("default/pod-1", pod1)
			fs.Update("default/pod-2", pod2)

			keys := fs.GetDistinctMapKeys("metadata.labels")
			Expect(keys).To(ContainElements("app", "env", "version"))
		})

		It("should return empty slice for non-existent path", func() {
			fs.Update("default/test-pod", testObj)

			keys := fs.GetDistinctMapKeys("spec.nonexistent")
			Expect(keys).To(BeEmpty())
		})

		It("should return empty slice for empty store", func() {
			keys := fs.GetDistinctMapKeys("metadata.labels")
			Expect(keys).To(BeEmpty())
		})
	})

	Describe("ReconstructObject", func() {
		It("should rebuild nested object from fields", func() {
			key := "default/test-pod"
			fs.Update(key, testObj)

			obj := fs.ReconstructObject(key)
			Expect(obj).NotTo(BeNil())

			// Check nested structure is preserved
			metadata, ok := obj["metadata"].(map[string]interface{})
			Expect(ok).To(BeTrue())
			Expect(metadata["name"]).To(Equal("test-pod"))
			Expect(metadata["namespace"]).To(Equal("default"))
		})

		It("should cache reconstructed objects for subsequent calls", func() {
			key := "default/test-pod"
			fs.Update(key, testObj)

			// First call builds and caches
			obj1 := fs.ReconstructObject(key)
			Expect(obj1).NotTo(BeNil())

			// Second call should return the same cached object (same pointer)
			obj2 := fs.ReconstructObject(key)
			Expect(obj2).NotTo(BeNil())

			// Verify it's the same object (pointer equality via reflect)
			// Maps cannot be compared directly in Go, so we use reflect.ValueOf().Pointer()
			ptr1 := reflect.ValueOf(obj1).Pointer()
			ptr2 := reflect.ValueOf(obj2).Pointer()
			Expect(ptr1).To(Equal(ptr2), "Expected same pointer for cached objects")
		})

		It("should invalidate cache on Update", func() {
			key := "default/test-pod"
			fs.Update(key, testObj)

			// Get cached object
			obj1 := fs.ReconstructObject(key)
			Expect(obj1).NotTo(BeNil())

			// Update the resource (invalidates cache)
			updatedObj := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"metadata": map[string]interface{}{
						"name":      "test-pod-updated",
						"namespace": "default",
					},
				},
			}
			fs.Update(key, updatedObj)

			// Get new object (should be rebuilt)
			obj2 := fs.ReconstructObject(key)
			Expect(obj2).NotTo(BeNil())

			// Should be a different object with updated data
			metadata, ok := obj2["metadata"].(map[string]interface{})
			Expect(ok).To(BeTrue())
			Expect(metadata["name"]).To(Equal("test-pod-updated"))

			// Original cached object should still have old data
			oldMetadata, ok := obj1["metadata"].(map[string]interface{})
			Expect(ok).To(BeTrue())
			Expect(oldMetadata["name"]).To(Equal("test-pod"))
		})

		It("should invalidate cache on Delete", func() {
			key := "default/test-pod"
			fs.Update(key, testObj)

			// Get cached object
			obj1 := fs.ReconstructObject(key)
			Expect(obj1).NotTo(BeNil())

			// Delete the resource (invalidates cache)
			fs.Delete(key)

			// Should return nil now
			obj2 := fs.ReconstructObject(key)
			Expect(obj2).To(BeNil())
		})

		It("should invalidate all caches on Clear", func() {
			// Add multiple resources
			fs.Update("default/pod-1", testObj)
			fs.Update("default/pod-2", testObj)

			// Cache both
			obj1 := fs.ReconstructObject("default/pod-1")
			obj2 := fs.ReconstructObject("default/pod-2")
			Expect(obj1).NotTo(BeNil())
			Expect(obj2).NotTo(BeNil())

			// Clear all
			fs.Clear()

			// Both should return nil now
			Expect(fs.ReconstructObject("default/pod-1")).To(BeNil())
			Expect(fs.ReconstructObject("default/pod-2")).To(BeNil())
		})

		It("should reconstruct arrays correctly", func() {
			key := "default/test-pod"
			// Set selected fields to extract container data
			fs.SetSelectedFields([]string{"spec.containers"})
			fs.Update(key, testObj)

			obj := fs.ReconstructObject(key)
			Expect(obj).NotTo(BeNil())

			// Check spec.containers is reconstructed as an array
			spec, ok := obj["spec"].(map[string]interface{})
			Expect(ok).To(BeTrue())
			containers, ok := spec["containers"].([]interface{})
			Expect(ok).To(BeTrue())
			Expect(containers).To(HaveLen(2))

			// Check first container
			c0, ok := containers[0].(map[string]interface{})
			Expect(ok).To(BeTrue())
			Expect(c0["name"]).To(Equal("nginx"))
			Expect(c0["image"]).To(Equal("nginx:1.19"))

			// Check second container
			c1, ok := containers[1].(map[string]interface{})
			Expect(ok).To(BeTrue())
			Expect(c1["name"]).To(Equal("sidecar"))
			Expect(c1["image"]).To(Equal("busybox"))
		})

		It("should reconstruct nested arrays correctly", func() {
			key := "default/test-pod"
			// Set selected fields to extract container data including ports
			fs.SetSelectedFields([]string{"spec.containers"})
			fs.Update(key, testObj)

			obj := fs.ReconstructObject(key)
			spec, ok := obj["spec"].(map[string]interface{})
			Expect(ok).To(BeTrue())
			containers, ok := spec["containers"].([]interface{})
			Expect(ok).To(BeTrue())

			// Check ports array in first container
			c0 := containers[0].(map[string]interface{})
			ports, ok := c0["ports"].([]interface{})
			Expect(ok).To(BeTrue())
			Expect(ports).To(HaveLen(1))

			port0 := ports[0].(map[string]interface{})
			Expect(port0["containerPort"]).To(Equal(float64(80)))
		})

		It("should exclude structure metadata from reconstruction", func() {
			key := "default/test-pod"
			fs.Update(key, testObj)

			obj := fs.ReconstructObject(key)
			// Structure metadata should not appear in reconstructed object
			for k := range obj {
				Expect(k).NotTo(HavePrefix("_struct:"))
			}
		})

		It("should return nil for non-existent key", func() {
			obj := fs.ReconstructObject("nonexistent")
			Expect(obj).To(BeNil())
		})
	})

	Describe("Concurrent access", func() {
		It("should handle concurrent reads and writes safely", func() {
			done := make(chan bool, 10)

			// Writers
			for i := 0; i < 5; i++ {
				go func(id int) {
					defer GinkgoRecover()
					for j := 0; j < 100; j++ {
						key := "default/pod-" + string(rune('a'+id))
						fs.Update(key, testObj)
						fs.SetSelectedFields([]string{"status.phase"})
					}
					done <- true
				}(i)
			}

			// Readers
			for i := 0; i < 5; i++ {
				go func() {
					defer GinkgoRecover()
					for j := 0; j < 100; j++ {
						_ = fs.List()
						_ = fs.Count()
						_ = fs.Get("default/pod-a")
						_, _ = fs.GetField("default/pod-a", "metadata.name")
						_ = fs.ReconstructObject("default/pod-a")
					}
					done <- true
				}()
			}

			// Wait for all goroutines
			for i := 0; i < 10; i++ {
				<-done
			}
		})
	})

	Context("String interning", func() {
		It("should intern identical string values", func() {
			// Create multiple pods with the same status.phase value
			for i := 0; i < 100; i++ {
				pod := &unstructured.Unstructured{
					Object: map[string]interface{}{
						"metadata": map[string]interface{}{
							"name":      "pod-" + string(rune('a'+i%26)),
							"namespace": "default",
						},
						"status": map[string]interface{}{
							"phase": "Running", // Same value across all pods
						},
					},
				}
				fs.SetSelectedFields([]string{"status.phase"})
				fs.Update("default/pod-"+string(rune('a'+i%26)), pod)
			}

			// Verify all pods have the same interned value
			var phaseValue interface{}
			for i := 0; i < 26; i++ {
				fields := fs.Get("default/pod-" + string(rune('a'+i)))
				if fields == nil {
					continue
				}
				if val, ok := fields["status.phase"]; ok {
					if phaseValue == nil {
						phaseValue = val
					} else {
						// All interned strings should be identical (same pointer)
						Expect(val).To(Equal(phaseValue))
					}
				}
			}
		})

		It("should not intern long strings (>64 chars)", func() {
			longValue := "this-is-a-very-long-string-that-should-not-be-interned-because-it-exceeds-64-characters"
			// Use labels instead of annotations (annotations removed from essential fields)
			pod := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"metadata": map[string]interface{}{
						"name":      "test-pod",
						"namespace": "default",
						"labels": map[string]interface{}{
							"long-label": longValue,
						},
					},
				},
			}
			fs.Update("default/test-pod", pod)

			fields := fs.Get("default/test-pod")
			Expect(fields).NotTo(BeNil())
			Expect(fields["metadata.labels.long-label"]).To(Equal(longValue))
		})
	})
})
