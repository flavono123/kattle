package kube

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var _ = Describe("Node", func() {
	Describe("Pickable", func() {
		It("should return false if node has children", func() {
			node := &Node{
				children: map[string]*Node{"child": {}},
			}
			Expect(node.Pickable(nil)).To(BeFalse())
		})

		It("should return true for primitive field with value", func() {
			node := &Node{
				name: "foo",
				field: &Field{
					Type: "string",
				},
			}
			objs := []*unstructured.Unstructured{
				{
					Object: map[string]interface{}{
						"foo": "bar",
					},
				},
			}
			Expect(node.Pickable(objs)).To(BeTrue())
		})

		It("should return false for primitive field without value", func() {
			node := &Node{
				name: "foo",
				field: &Field{
					Type: "string",
				},
			}
			objs := []*unstructured.Unstructured{
				{
					Object: map[string]interface{}{},
				},
			}
			Expect(node.Pickable(objs)).To(BeFalse())
		})

		It("should return false for non-primitive field", func() {
			node := &Node{
				name: "foo",
				field: &Field{
					Type: "map[string]string",
				},
			}
			objs := []*unstructured.Unstructured{
				{
					Object: map[string]interface{}{
						"foo": map[string]interface{}{"a": "b"},
					},
				},
			}
			Expect(node.Pickable(objs)).To(BeFalse())
		})
	})

	Describe("CreateNodeTreeFromStore", func() {
		var fs *FieldStore

		BeforeEach(func() {
			fs = NewFieldStore()
		})

		It("should create nodes for array fields using FieldStore", func() {
			// Set selected fields to include spec.containers so structure metadata is extracted
			fs.SetSelectedFields([]string{"spec.containers"})

			// Create a pod with containers array
			pod := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"metadata": map[string]interface{}{
						"name": "test-pod", "namespace": "default",
					},
					"spec": map[string]interface{}{
						"containers": []interface{}{
							map[string]interface{}{"name": "c1"},
							map[string]interface{}{"name": "c2"},
						},
					},
				},
			}
			fs.Update("default/test-pod", pod)

			// Create field tree with array field
			fieldTree := map[string]*Field{
				"spec": {
					Name:   "spec",
					Type:   "object",
					Prefix: []string{},
					Children: map[string]*Field{
						"containers": {
							Name:   "containers",
							Type:   "[]Container",
							Prefix: []string{"spec"},
							Level:  1,
						},
					},
				},
			}

			nodes := CreateNodeTreeFromStore(fieldTree, fs, []string{})
			Expect(nodes).To(HaveKey("spec"))
			Expect(nodes["spec"].children).To(HaveKey("containers"))

			containersNode := nodes["spec"].children["containers"]
			// Should have 2 index nodes plus wildcard
			Expect(containersNode.children).To(HaveKey("0"))
			Expect(containersNode.children).To(HaveKey("1"))
		})

		It("should create nodes for map fields using FieldStore", func() {
			// Create pods with different labels
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

			// Create field tree with map field
			fieldTree := map[string]*Field{
				"metadata": {
					Name:   "metadata",
					Type:   "object",
					Prefix: []string{},
					Children: map[string]*Field{
						"labels": {
							Name:   "labels",
							Type:   "map[string]string",
							Prefix: []string{"metadata"},
							Level:  1,
						},
					},
				},
			}

			nodes := CreateNodeTreeFromStore(fieldTree, fs, []string{})
			Expect(nodes).To(HaveKey("metadata"))
			Expect(nodes["metadata"].children).To(HaveKey("labels"))

			labelsNode := nodes["metadata"].children["labels"]
			// Should have all distinct keys
			Expect(labelsNode.children).To(HaveKey("app"))
			Expect(labelsNode.children).To(HaveKey("env"))
			Expect(labelsNode.children).To(HaveKey("version"))
		})

		It("should work with empty FieldStore", func() {
			fieldTree := map[string]*Field{
				"spec": {
					Name:   "spec",
					Type:   "object",
					Prefix: []string{},
					Children: map[string]*Field{
						"containers": {
							Name:   "containers",
							Type:   "[]Container",
							Prefix: []string{"spec"},
							Level:  1,
						},
					},
				},
			}

			nodes := CreateNodeTreeFromStore(fieldTree, fs, []string{})
			Expect(nodes).To(HaveKey("spec"))
			containersNode := nodes["spec"].children["containers"]
			// No index nodes when store is empty
			Expect(containersNode.children).To(BeEmpty())
		})
	})

	Describe("UpdateNodeTreeFromStore", func() {
		var fs *FieldStore

		BeforeEach(func() {
			fs = NewFieldStore()
		})

		It("should preserve Expanded state when updating", func() {
			pod := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"metadata": map[string]interface{}{
						"name": "test-pod", "namespace": "default",
					},
					"spec": map[string]interface{}{
						"containers": []interface{}{
							map[string]interface{}{"name": "c1"},
						},
					},
				},
			}
			fs.Update("default/test-pod", pod)

			fieldTree := map[string]*Field{
				"spec": {
					Name:   "spec",
					Type:   "object",
					Prefix: []string{},
					Children: map[string]*Field{
						"containers": {
							Name:   "containers",
							Type:   "[]Container",
							Prefix: []string{"spec"},
							Level:  1,
						},
					},
				},
			}

			// Create initial tree
			existingNodes := CreateNodeTreeFromStore(fieldTree, fs, []string{})
			existingNodes["spec"].Expanded = true

			// Update tree
			updatedNodes := UpdateNodeTreeFromStore(existingNodes, fieldTree, fs, []string{})
			Expect(updatedNodes["spec"].Expanded).To(BeTrue())
		})
	})
})
