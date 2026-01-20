package kube

import (
	"fmt"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type Node struct {
	Expanded bool
	Selected bool
	// TODO: new field to represent the values of node are all nil
	// reversed this would be a Line's Essential field(tbd), to reduce of schema context

	field     *Field
	name      string
	ancestors []string
	level     int
	children  map[string]*Node
}

// line things
func (n *Node) ToggleFolder() {
	if n.Foldable() {
		n.Expanded = !n.Expanded
	}
}

func (n *Node) SetExpanded(expanded bool) {
	n.Expanded = expanded
}

func (n *Node) Renderable(objs []*unstructured.Unstructured) bool {
	return n.Foldable() || n.Pickable(objs)
}

func (n *Node) Foldable() bool {
	return n.hasChildren()
}

func (n *Node) Pickable(objs []*unstructured.Unstructured) bool {
	if n.hasChildren() {
		return false
	}

	if n.field == nil {
		return !n.allNil(objs)
	}

	return n.field.IsPrimitive() && !n.allNil(objs)
}

func (n *Node) allNil(objs []*unstructured.Unstructured) bool {
	for _, obj := range objs {
		if ValStr(n, obj) != "-" {
			return false
		}
	}
	return true
}

func (n *Node) hasChildren() bool {
	return len(n.children) > 0
}

// line things end

func (n *Node) Children() map[string]*Node {
	return n.children
}

func (n *Node) Name() string {
	if n.field == nil {
		return n.name
	}
	return n.field.Name
}

func (n *Node) HeaderName() string {
	// uppercase all char of last part of split by '/'
	parts := strings.Split(n.name, "/")
	headerName := strings.ToUpper(parts[len(parts)-1])

	return headerName
}

func (n *Node) Prefix() []string {
	if n.field == nil {
		return nil
	}
	return n.field.Prefix
}

func (n *Node) NodeFullPath() []string {
	fullPath := []string{}
	fullPath = append(fullPath, n.ancestors...)
	fullPath = append(fullPath, n.name)
	return fullPath
}

func (n *Node) Type() string {
	if n.field == nil {
		return ""
	}
	return n.field.Type
}

func (n *Node) Required() bool {
	if n.field == nil {
		return false
	}
	return n.field.Required
}

// TODO: move to kube*Field, use for digging array or map(ref?) val to create node
func (n *Node) FullPath() []string {
	fullPath := []string{}
	if n.field == nil {
		return fullPath
	}
	fullPath = append(fullPath, n.field.Prefix...)
	fullPath = append(fullPath, n.field.Name)
	return fullPath
}

func (n *Node) Level() int {
	// Use ancestors length to calculate actual tree depth
	// This correctly handles index nodes (*, 0, 1, ...) which add extra levels
	return len(n.ancestors)
}

func CreateNodeTree(fieldTree map[string]*Field, objs []*unstructured.Unstructured, nodePrefix []string) map[string]*Node {
	result := make(map[string]*Node)

	for key, field := range fieldTree {
		prefix := field.Prefix
		if !comparePrefix(nodePrefix, field.Prefix) {
			prefix = nodePrefix
		}

		childPrefix := append(prefix, key)
		children := map[string]*Node(nil)

		if field.IsArray() {
			maxLength := getMaxLength(childPrefix, objs)
			children = make(map[string]*Node)

			// Add wildcard node for non-empty arrays (before creating index nodes)
			if maxLength > 0 && field.Children != nil {
				// Create independent children tree for wildcard node
				wildcardChildren := CreateNodeTree(field.Children, objs, append(childPrefix, "*"))
				children["*"] = &Node{
					field:     nil,
					name:      "*",
					ancestors: childPrefix,
					level:     field.Level + 1,
					children:  wildcardChildren,
				}
			}

			for i := 0; i < maxLength; i++ {
				idx := strconv.Itoa(i)
				grandChildren := map[string]*Node(nil)
				if field.Children != nil {
					grandChildren = CreateNodeTree(field.Children, objs, append(childPrefix, idx))
				}

				children[idx] = &Node{
					field:     nil,
					name:      idx,
					ancestors: childPrefix,
					level:     field.Level + 1,
					children:  grandChildren,
				}
			}
		} else if field.IsMap() {
			keys := getDistinctKeys(childPrefix, objs)
			children = make(map[string]*Node)

			// Add wildcard node for maps with keys (select all keys)
			if len(keys) > 0 {
				children["*"] = &Node{
					field:     nil,
					name:      "*",
					ancestors: childPrefix,
					level:     field.Level + 1,
					children:  nil, // leaf-like node for "select all siblings"
				}
			}

			for _, key := range keys {
				grandChildren := map[string]*Node(nil)
				if field.Children != nil {
					grandChildren = CreateNodeTree(field.Children, objs, append(childPrefix, key))
				}

				children[key] = &Node{
					field:     nil,
					name:      key,
					ancestors: childPrefix,
					level:     field.Level + 1,
					children:  grandChildren,
				}
			}

		} else if field.IsObject() {
			children = CreateNodeTree(field.Children, objs, childPrefix)
		}

		result[key] = &Node{
			field:     field,
			ancestors: prefix,
			name:      key,
			children:  children,
		}
	}

	return result
}

// isNumericPath checks if a path segment is likely a numeric index.
// This avoids calling strconv.Atoi and allocating error objects for non-numeric paths.
func isNumericPath(path string) bool {
	if len(path) == 0 {
		return false
	}
	// Check if the first character is a digit
	return path[0] >= '0' && path[0] <= '9'
}

func getNestedValue(obj map[string]interface{}, paths ...string) (interface{}, bool, error) {
	var current interface{} = obj

	for i, path := range paths {
		// Handle wildcard: use first index (0) for querying actual data
		if path == "*" {
			if slice, ok := current.([]interface{}); ok {
				if len(slice) == 0 {
					return nil, false, nil
				}
				current = slice[0]
			} else {
				return nil, false, fmt.Errorf("expected array for wildcard, got %T", current)
			}
		} else if isNumericPath(path) {
			// for array nodes - only try parsing if path looks like a number
			index, err := strconv.Atoi(path)
			if err != nil {
				// Shouldn't happen since we pre-checked, but handle gracefully
				return nil, false, fmt.Errorf("invalid index: %s", path)
			}
			if slice, ok := current.([]interface{}); ok {
				if index >= len(slice) {
					return nil, false, fmt.Errorf("index %d out of bounds", index)
				}
				current = slice[index]
			} else {
				return nil, false, fmt.Errorf("expected array, got %T", current)
			}
		} else {
			// for map nodes
			if m, ok := current.(map[string]interface{}); ok {
				var exists bool
				current, exists = m[path]
				if !exists {
					return nil, false, nil
				}
			} else {
				return nil, false, fmt.Errorf("expected map, got %T", current)
			}
		}

		// 마지막 필드면 현재 값 반환
		if i == len(paths)-1 {
			return current, true, nil
		}
	}

	return current, true, nil
}

func ValStr(node *Node, obj *unstructured.Unstructured) string {
	val, found, err := getNestedValue(obj.Object, node.NodeFullPath()...)
	if err != nil || !found {
		return "-"
	}

	if str, ok := val.(string); ok && len(str) == 0 { // edge case `""`
		return "\"\""
	}

	return fmt.Sprintf("%v", val)
}

func getMaxLength(arrayPath []string, objs []*unstructured.Unstructured) int {
	maxLength := 0 // if no array, return 1 to render only fields
	for _, obj := range objs {
		val, found, err := getNestedValue(obj.Object, arrayPath...)
		if err != nil || !found {
			continue
		}
		arr := val.([]interface{})
		if len(arr) > maxLength {
			maxLength = len(arr)
		}
	}
	return maxLength
}

func comparePrefix(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}

func getDistinctKeys(mapPath []string, objs []*unstructured.Unstructured) []string {
	keys := []string{}
	exists := map[string]struct{}{}

	for _, obj := range objs {
		val, found, err := getNestedValue(obj.Object, mapPath...)
		if err != nil || !found {
			continue
		}
		mapString := val.(map[string]interface{})
		for k := range mapString {
			if _, ok := exists[k]; !ok {
				exists[k] = struct{}{}
				keys = append(keys, k)
			}
		}
	}

	return keys
}

// pathToString converts a path slice to dot-separated string for FieldStore lookup
func pathToString(path []string) string {
	return strings.Join(path, ".")
}

// CreateNodeTreeFromStore creates a node tree using FieldStore's structure metadata.
// This is memory-efficient as it doesn't require full Kubernetes objects.
func CreateNodeTreeFromStore(fieldTree map[string]*Field, fs *FieldStore, nodePrefix []string) map[string]*Node {
	result := make(map[string]*Node)

	for key, field := range fieldTree {
		prefix := field.Prefix
		if !comparePrefix(nodePrefix, field.Prefix) {
			prefix = nodePrefix
		}

		childPrefix := append(prefix, key)
		children := map[string]*Node(nil)

		if field.IsArray() {
			maxLength := fs.GetMaxArrayLength(pathToString(childPrefix))
			children = make(map[string]*Node)

			// Add wildcard node for non-empty arrays (before creating index nodes)
			if maxLength > 0 && field.Children != nil {
				wildcardChildren := CreateNodeTreeFromStore(field.Children, fs, append(childPrefix, "*"))
				children["*"] = &Node{
					field:     nil,
					name:      "*",
					ancestors: childPrefix,
					level:     field.Level + 1,
					children:  wildcardChildren,
				}
			}

			for i := 0; i < maxLength; i++ {
				idx := strconv.Itoa(i)
				grandChildren := map[string]*Node(nil)
				if field.Children != nil {
					grandChildren = CreateNodeTreeFromStore(field.Children, fs, append(childPrefix, idx))
				}

				children[idx] = &Node{
					field:     nil,
					name:      idx,
					ancestors: childPrefix,
					level:     field.Level + 1,
					children:  grandChildren,
				}
			}
		} else if field.IsMap() {
			keys := fs.GetDistinctMapKeys(pathToString(childPrefix))
			children = make(map[string]*Node)

			// Add wildcard node for maps with keys (select all keys)
			if len(keys) > 0 {
				children["*"] = &Node{
					field:     nil,
					name:      "*",
					ancestors: childPrefix,
					level:     field.Level + 1,
					children:  nil,
				}
			}

			for _, mapKey := range keys {
				grandChildren := map[string]*Node(nil)
				if field.Children != nil {
					grandChildren = CreateNodeTreeFromStore(field.Children, fs, append(childPrefix, mapKey))
				}

				children[mapKey] = &Node{
					field:     nil,
					name:      mapKey,
					ancestors: childPrefix,
					level:     field.Level + 1,
					children:  grandChildren,
				}
			}

		} else if field.IsObject() {
			children = CreateNodeTreeFromStore(field.Children, fs, childPrefix)
		}

		result[key] = &Node{
			field:     field,
			ancestors: prefix,
			name:      key,
			children:  children,
		}
	}

	return result
}

// UpdateNodeTreeFromStore updates a node tree using FieldStore's structure metadata.
// Preserves Expanded/Selected state from existing nodes.
func UpdateNodeTreeFromStore(existing map[string]*Node, fieldTree map[string]*Field, fs *FieldStore, nodePrefix []string) map[string]*Node {
	result := make(map[string]*Node)

	for key, field := range fieldTree {
		prefix := field.Prefix
		if !comparePrefix(nodePrefix, field.Prefix) {
			prefix = nodePrefix
		}

		childPrefix := append(prefix, key)
		var children map[string]*Node

		existingNode, exists := existing[key]
		expanded := exists && existingNode.Expanded
		selected := exists && existingNode.Selected

		if field.IsArray() {
			maxLength := fs.GetMaxArrayLength(pathToString(childPrefix))
			children = make(map[string]*Node)

			// Add wildcard node for non-empty arrays
			if maxLength > 0 && field.Children != nil {
				existingWildcardChildren := map[string]*Node{}
				if exists && existingNode.children != nil && existingNode.children["*"] != nil {
					existingWildcardChildren = existingNode.children["*"].children
				}
				wildcardChildren := UpdateNodeTreeFromStore(existingWildcardChildren, field.Children, fs, append(childPrefix, "*"))
				children["*"] = &Node{
					field:     nil,
					name:      "*",
					ancestors: childPrefix,
					level:     field.Level + 1,
					children:  wildcardChildren,
					Expanded:  exists && existingNode.children != nil && existingNode.children["*"] != nil && existingNode.children["*"].Expanded,
					Selected:  exists && existingNode.children != nil && existingNode.children["*"] != nil && existingNode.children["*"].Selected,
				}
			}

			for i := 0; i < maxLength; i++ {
				idx := strconv.Itoa(i)
				var grandChildren map[string]*Node

				if field.Children != nil {
					existingChildren := map[string]*Node{}
					if exists && existingNode.children != nil && existingNode.children[idx] != nil {
						existingChildren = existingNode.children[idx].children
					}
					grandChildren = UpdateNodeTreeFromStore(existingChildren, field.Children, fs, append(childPrefix, idx))
				}

				children[idx] = &Node{
					field:     nil,
					name:      idx,
					ancestors: childPrefix,
					level:     field.Level + 1,
					children:  grandChildren,
					Expanded:  exists && existingNode.children != nil && existingNode.children[idx] != nil && existingNode.children[idx].Expanded,
					Selected:  exists && existingNode.children != nil && existingNode.children[idx] != nil && existingNode.children[idx].Selected,
				}
			}
		} else if field.IsMap() {
			keys := fs.GetDistinctMapKeys(pathToString(childPrefix))
			children = make(map[string]*Node)

			// Add wildcard node for maps with keys
			if len(keys) > 0 {
				children["*"] = &Node{
					field:     nil,
					name:      "*",
					ancestors: childPrefix,
					level:     field.Level + 1,
					children:  nil,
					Expanded:  exists && existingNode.children != nil && existingNode.children["*"] != nil && existingNode.children["*"].Expanded,
					Selected:  exists && existingNode.children != nil && existingNode.children["*"] != nil && existingNode.children["*"].Selected,
				}
			}

			for _, mapKey := range keys {
				var grandChildren map[string]*Node

				if field.Children != nil {
					existingChildren := map[string]*Node{}
					if exists && existingNode.children != nil {
						if existingChild, ok := existingNode.children[mapKey]; ok {
							existingChildren = existingChild.children
						}
					}
					grandChildren = UpdateNodeTreeFromStore(existingChildren, field.Children, fs, append(childPrefix, mapKey))
				}

				children[mapKey] = &Node{
					field:     nil,
					name:      mapKey,
					ancestors: childPrefix,
					level:     field.Level + 1,
					children:  grandChildren,
					Expanded:  exists && existingNode.children != nil && existingNode.children[mapKey] != nil && existingNode.children[mapKey].Expanded,
					Selected:  exists && existingNode.children != nil && existingNode.children[mapKey] != nil && existingNode.children[mapKey].Selected,
				}
			}
		} else if field.IsObject() {
			existingChildren := map[string]*Node{}
			if exists {
				existingChildren = existingNode.children
			}
			children = UpdateNodeTreeFromStore(existingChildren, field.Children, fs, childPrefix)
		}

		result[key] = &Node{
			field:     field,
			ancestors: prefix,
			name:      key,
			children:  children,
			Expanded:  expanded,
			Selected:  selected,
		}
	}

	return result
}

// TODO: refactor, pull up traverse with create to function
// TODO: besides, expandedNodes should be a state of the schemaModel(ideally expand would not be a state of node)
func UpdateNodeTree(existing map[string]*Node, fieldTree map[string]*Field, objs []*unstructured.Unstructured, nodePrefix []string) map[string]*Node {
	result := make(map[string]*Node)

	for key, field := range fieldTree {
		prefix := field.Prefix
		if !comparePrefix(nodePrefix, field.Prefix) {
			prefix = nodePrefix
		}

		childPrefix := append(prefix, key)
		var children map[string]*Node

		existingNode, exists := existing[key]
		expanded := exists && existingNode.Expanded
		selected := exists && existingNode.Selected

		if field.IsArray() {
			maxLength := getMaxLength(childPrefix, objs)
			children = make(map[string]*Node)

			// Add wildcard node for non-empty arrays (before creating index nodes)
			if maxLength > 0 && field.Children != nil {
				existingWildcardChildren := map[string]*Node{}
				if exists && existingNode.children != nil && existingNode.children["*"] != nil {
					existingWildcardChildren = existingNode.children["*"].children
				}
				wildcardChildren := UpdateNodeTree(existingWildcardChildren, field.Children, objs, append(childPrefix, "*"))
				children["*"] = &Node{
					field:     nil,
					name:      "*",
					ancestors: childPrefix,
					level:     field.Level + 1,
					children:  wildcardChildren,
					Expanded:  exists && existingNode.children != nil && existingNode.children["*"] != nil && existingNode.children["*"].Expanded,
					Selected:  exists && existingNode.children != nil && existingNode.children["*"] != nil && existingNode.children["*"].Selected,
				}
			}

			for i := 0; i < maxLength; i++ {
				idx := strconv.Itoa(i)
				var grandChildren map[string]*Node

				if field.Children != nil {
					existingChildren := map[string]*Node{}
					if exists && existingNode.children != nil && existingNode.children[idx] != nil {
						existingChildren = existingNode.children[idx].children
					}
					grandChildren = UpdateNodeTree(existingChildren, field.Children, objs, append(childPrefix, idx))
				}

				children[idx] = &Node{
					field:     nil,
					name:      idx,
					ancestors: childPrefix,
					level:     field.Level + 1,
					children:  grandChildren,
					Expanded:  exists && existingNode.children != nil && existingNode.children[idx] != nil && existingNode.children[idx].Expanded,
					Selected:  exists && existingNode.children != nil && existingNode.children[idx] != nil && existingNode.children[idx].Selected,
				}
			}
		} else if field.IsMap() {
			keys := getDistinctKeys(childPrefix, objs)
			children = make(map[string]*Node)

			// Add wildcard node for maps with keys (select all keys)
			if len(keys) > 0 {
				children["*"] = &Node{
					field:     nil,
					name:      "*",
					ancestors: childPrefix,
					level:     field.Level + 1,
					children:  nil,
					Expanded:  exists && existingNode.children != nil && existingNode.children["*"] != nil && existingNode.children["*"].Expanded,
					Selected:  exists && existingNode.children != nil && existingNode.children["*"] != nil && existingNode.children["*"].Selected,
				}
			}

			for _, mapKey := range keys {
				var grandChildren map[string]*Node

				if field.Children != nil {
					existingChildren := map[string]*Node{}
					if exists && existingNode.children != nil {
						if existingChild, ok := existingNode.children[mapKey]; ok {
							existingChildren = existingChild.children
						}
					}
					grandChildren = UpdateNodeTree(existingChildren, field.Children, objs, append(childPrefix, mapKey))
				}

				children[mapKey] = &Node{
					field:     nil,
					name:      mapKey,
					ancestors: childPrefix,
					level:     field.Level + 1,
					children:  grandChildren,
					Expanded:  exists && existingNode.children != nil && existingNode.children[mapKey] != nil && existingNode.children[mapKey].Expanded,
					Selected:  exists && existingNode.children != nil && existingNode.children[mapKey] != nil && existingNode.children[mapKey].Selected,
				}
			}
		} else if field.IsObject() {
			existingChildren := map[string]*Node{}
			if exists {
				existingChildren = existingNode.children
			}
			children = UpdateNodeTree(existingChildren, field.Children, objs, childPrefix)
		}

		result[key] = &Node{
			field:     field,
			ancestors: prefix,
			name:      key,
			children:  children,
			Expanded:  expanded,
			Selected:  selected,
		}
	}

	return result
}
