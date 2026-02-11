package kube

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/go-openapi/jsonreference"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"k8s.io/kube-openapi/pkg/spec3"
	"k8s.io/kube-openapi/pkg/validation/spec"
)

// getDocumentForContext retrieves the OpenAPI document for a GVR from the specified context
// If contextName is empty, uses the current context
func getDocumentForContext(contextName string, gvr schema.GroupVersionResource) (*spec3.OpenAPI, error) {
	var result *spec3.OpenAPI

	discoveryClient, err := DiscoveryClientForContext(contextName)
	if err != nil {
		return nil, fmt.Errorf("failed to get discovery client: %v", err)
	}

	openapiv3 := discoveryClient.OpenAPIV3()

	paths, err := openapiv3.Paths()
	if err != nil {
		return nil, fmt.Errorf("failed to get openapi paths: %v", err)
	}
	schemabytes, err := paths[getDocumentPath(gvr)].Schema(runtime.ContentTypeJSON)
	if err != nil {
		return nil, fmt.Errorf("failed to get openapi schema: %v", err)
	}
	var document *spec3.OpenAPI
	if err := json.Unmarshal(schemabytes, &document); err != nil {
		return nil, fmt.Errorf("failed to unmarshal schema: %v", err)
	}
	result = document

	return result, nil
}

func getPathPrefix(gvr schema.GroupVersionResource) string {
	if gvr.Group != "" {
		return "/apis/" + gvr.Group
	}

	return "/api"
}

// findSchema searches for a schema with the given GVK in the OpenAPI document
func findSchema(document *spec3.OpenAPI, gvk schema.GroupVersionKind) (*spec.Schema, error) {
	// components/schemas에서 GVK에 해당하는 스키마 찾기
	for _, schema := range document.Components.Schemas {
		if matchXKubeGVK(schema.Extensions, gvk) {
			return schema, nil
		}
	}

	return nil, fmt.Errorf("GVK %v not found in OpenAPI schema", gvk)
}

func matchXKubeGVK(extension spec.Extensions, gvk schema.GroupVersionKind) bool {
	gvkList, ok := extension["x-kubernetes-group-version-kind"]
	if !ok {
		return false
	}

	if list, ok := gvkList.([]interface{}); ok {
		for _, item := range list {
			if gvkMap, ok := item.(map[string]interface{}); ok {
				if gvkMap["group"] == gvk.Group && gvkMap["version"] == gvk.Version && gvkMap["kind"] == gvk.Kind {
					return true
				}
			}
		}
	}

	return false
}

// CreateFieldTree creates a field tree for a GVK from the current context (legacy, kept for TUI compatibility)
func CreateFieldTree(gvk schema.GroupVersionKind) (map[string]*Field, error) {
	return CreateFieldTreeForContext("", gvk)
}

// CreateFieldTreeForContext creates a field tree for a GVK from the specified context
// If contextName is empty, uses the current context
func CreateFieldTreeForContext(contextName string, gvk schema.GroupVersionKind) (map[string]*Field, error) {
	gvr, err := GetGVRForContext(contextName, gvk)
	if err != nil {
		return nil, err
	}
	document, err := getDocumentForContext(contextName, gvr)
	if err != nil {
		return nil, err
	}
	schema, err := findSchema(document, gvk)
	if err != nil {
		return nil, err
	}
	history := make(map[string]bool)

	// Get reference string
	refString := schema.Ref.String()

	// Circular reference detection (commented out for now)
	// if refString != "" {
	// 	if history[refString] {
	// 		return nil, nil
	// 	}
	// 	history[refString] = true
	// }

	// Resolve schema reference if exists
	if resolved := resolveRef(refString, document); resolved != nil {
		schema = resolved
	}

	nodes, err := createFieldList(schema, []string{}, 0, document, history)
	if err != nil {
		return nil, err
	}

	return nodes, nil
}

func createFieldList(schema *spec.Schema, prefix []string, level int, document *spec3.OpenAPI, history map[string]bool) (map[string]*Field, error) {
	var result map[string]*Field
	nodes := make(map[string]*Field)

	if schema == nil {
		return nil, fmt.Errorf("schema is nil")
	}

	refString := schema.Ref.String()
	if refString != "" {
		if history[refString] {
			return nil, nil
		}
		history[refString] = true
	}

	// copy history to pass for each branch
	nextHistory := make(map[string]bool)
	for k, v := range history {
		nextHistory[k] = v
	}

	resolvedSchema := schema
	if resolved := resolveRef(refString, document); resolved != nil {
		resolvedSchema = resolved
	}

	for key, prop := range resolvedSchema.Properties {
		children, err := createFieldList(&prop, append(prefix, key), level+1, document, nextHistory)
		if err != nil {
			return nil, err
		}
		node := createField(key, prefix, resolvedSchema, level, document)
		node.Children = children
		nodes[key] = node

		result = nodes
	}

	for _, subSchema := range resolvedSchema.AllOf {
		nodes, err := createFieldList(&subSchema, prefix, level, document, nextHistory)
		if err != nil {
			return nil, err
		}
		result = nodes
	}

	if resolvedSchema.Items != nil {
		// HACK: special char might be needed such as `[]`?
		nodes, err := createFieldList(resolvedSchema.Items.Schema, prefix, level+1, document, nextHistory)
		if err != nil {
			return nil, err
		}
		result = nodes
	}
	if resolvedSchema.AdditionalProperties != nil && resolvedSchema.AdditionalProperties.Schema != nil {
		nodes, err := createFieldList(resolvedSchema.AdditionalProperties.Schema, prefix, level, document, nextHistory)
		if err != nil {
			return nil, err
		}
		result = nodes
	}

	return result, nil
}

func resolveRef(refString string, document *spec3.OpenAPI) *spec.Schema {
	ref, err := jsonreference.New(refString)
	if err != nil {
		return nil
	}

	if !ref.HasFragmentOnly {
		// Downloading is not supported. Treat as not found
		return nil
	}

	fragment := ref.GetURL().Fragment
	components := strings.Split(fragment, "/")

	// components e.g. #/components/schemas/io.k8s.api.core.v1.Pod -> io.k8s.api.core.v1.Pod
	return document.Components.Schemas[components[3]]
}

func createField(name string, prefix []string, schema *spec.Schema, level int, document *spec3.OpenAPI) *Field {
	var result Field

	result.Name = name
	result.Level = level
	result.Prefix = prefix
	fieldSchema := schema.Properties[name]
	result.Type = typeGuess(&fieldSchema, document)
	for _, required := range schema.Required {
		if required == name {
			result.Required = true
		}
	}

	result.Enum = extractEnum(&fieldSchema)

	return &result
}

func typeGuess(schema *spec.Schema, document *spec3.OpenAPI) string {
	if schema == nil {
		return "Object"
	}
	// Array 타입
	if schema.Items != nil && schema.Items.Schema != nil {
		return "[]" + typeGuess(schema.Items.Schema, document)
	}

	// Map 타입
	if schema.AdditionalProperties != nil && schema.AdditionalProperties.Schema != nil {
		return fmt.Sprintf("map[string]%s", typeGuess(schema.AdditionalProperties.Schema, document))
	}

	// Ref 타입
	if refString := schema.Ref.String(); refString != "" {
		// ref가 있는 경우 ref된 스키마 확인
		if resolved := resolveRef(refString, document); resolved != nil {
			// ref된 스키마가 primitive type이 아닌 경우에만 ref 이름 사용
			if resolved.Type == nil || resolved.Type[0] == "object" {
				// ref 문자열에서 마지막 컴포넌트만 추출 (예: io.k8s.api.core.v1.PodTemplateSpec -> PodTemplateSpec)
				parts := strings.Split(refString, "/")
				name := parts[len(parts)-1]
				nameParts := strings.Split(name, ".")
				return nameParts[len(nameParts)-1]
			}
			return strings.Join(resolved.Type, "|")
		}
	}

	// AllOf가 하나만 있고 properties가 없는 경우
	if len(schema.AllOf) == 1 && len(schema.Properties) == 0 {
		return typeGuess(&schema.AllOf[0], document)
	}

	// 기본 타입
	if len(schema.Type) > 0 {
		if schema.Type[0] == "object" {
			return "Object"
		}
		return strings.Join(schema.Type, "|")
	}

	return "Object"
}

func extractEnum(schema *spec.Schema) []string {
	var result []string

	if schema == nil || len(schema.Enum) == 0 {
		return result
	}

	// enum 값들 순회
	for _, element := range schema.Enum {
		// 빈 문자열은 "" 로 표시
		if str, ok := element.(string); ok && str == "" {
			result = append(result, `""`)
		} else {
			result = append(result, fmt.Sprintf("%v", element))
		}
	}

	return result
}

func getDocumentPath(gvr schema.GroupVersionResource) string {
	return strings.TrimPrefix(strings.Join([]string{getPathPrefix(gvr), gvr.Version}, "/"), "/")
}

// GetPrinterColumnsForContext retrieves printer columns for a GVK.
// For CRDs: extracts additionalPrinterColumns from CRD definition (has JSONPath).
// For built-in resources: uses Table API column definitions and maps column names to field paths.
// Returns field paths (e.g., [][]string{{"spec", "replicas"}, {"status", "phase"}})
func GetPrinterColumnsForContext(contextName string, gvk schema.GroupVersionKind) ([][]string, error) {
	gvr, err := GetGVRForContext(contextName, gvk)
	if err != nil {
		return nil, nil
	}

	// First, try to get additionalPrinterColumns from CRD
	paths := getPrinterColumnsFromCRD(contextName, gvk, gvr)
	if paths != nil {
		return paths, nil
	}

	// For built-in resources, use Table API column definitions
	return getPrinterColumnsFromTableAPI(contextName, gvk, gvr)
}

// getPrinterColumnsFromCRD extracts additionalPrinterColumns from CRD definition.
func getPrinterColumnsFromCRD(contextName string, gvk schema.GroupVersionKind, gvr schema.GroupVersionResource) [][]string {
	// Build CRD name: plural.group (e.g., "certificates.cert-manager.io")
	crdName := gvr.Resource
	if gvk.Group != "" {
		crdName = gvr.Resource + "." + gvk.Group
	}

	// CRD GVR
	crdGVR := schema.GroupVersionResource{
		Group:    "apiextensions.k8s.io",
		Version:  "v1",
		Resource: "customresourcedefinitions",
	}

	client, err := DynamicClientForContext(contextName)
	if err != nil {
		return nil
	}

	// Try to get CRD (cluster-scoped, so no namespace)
	crd, err := client.Resource(crdGVR).Get(context.Background(), crdName, metav1.GetOptions{})
	if err != nil {
		// Not a CRD or not found
		return nil
	}

	// Extract additionalPrinterColumns from CRD
	// Path: spec.versions[].additionalPrinterColumns[]
	versions, found, err := unstructured.NestedSlice(crd.Object, "spec", "versions")
	if err != nil || !found {
		return nil
	}

	for _, v := range versions {
		versionMap, ok := v.(map[string]interface{})
		if !ok {
			continue
		}

		name, _, _ := unstructured.NestedString(versionMap, "name")
		if name != gvk.Version {
			continue
		}

		columns, found, _ := unstructured.NestedSlice(versionMap, "additionalPrinterColumns")
		if !found || len(columns) == 0 {
			return nil
		}

		var paths [][]string
		for _, col := range columns {
			colMap, ok := col.(map[string]interface{})
			if !ok {
				continue
			}
			jsonPath, _, _ := unstructured.NestedString(colMap, "jsonPath")
			path := jsonPathToFieldPath(jsonPath)
			if len(path) > 0 {
				paths = append(paths, path)
			}
		}
		return paths
	}

	return nil
}

// getPrinterColumnsFromTableAPI uses Table API to get column definitions for built-in resources.
// Maps column names to field paths using known mappings.
func getPrinterColumnsFromTableAPI(contextName string, gvk schema.GroupVersionKind, gvr schema.GroupVersionResource) ([][]string, error) {
	config, err := kubeConfigForContext(contextName)
	if err != nil {
		return nil, nil
	}

	// Build API path
	var apiPath string
	if gvr.Group == "" {
		apiPath = fmt.Sprintf("/api/%s/%s", gvr.Version, gvr.Resource)
	} else {
		apiPath = fmt.Sprintf("/apis/%s/%s/%s", gvr.Group, gvr.Version, gvr.Resource)
	}

	// Configure REST client with Table Accept header
	config.AcceptContentTypes = "application/json;as=Table;g=meta.k8s.io;v=v1"
	config.ContentType = "application/json"
	config.GroupVersion = &schema.GroupVersion{Group: gvr.Group, Version: gvr.Version}
	config.NegotiatedSerializer = runtime.NewSimpleNegotiatedSerializer(runtime.SerializerInfo{})

	restClient, err := rest.RESTClientFor(config)
	if err != nil {
		return nil, nil
	}

	tableRaw, err := restClient.Get().
		AbsPath(apiPath).
		Param("limit", "1").
		Do(context.Background()).
		Raw()
	if err != nil {
		return nil, nil
	}

	var table metav1.Table
	if err := json.Unmarshal(tableRaw, &table); err != nil {
		return nil, nil
	}

	var paths [][]string
	for _, col := range table.ColumnDefinitions {
		// Skip "Name" column as it's always shown
		if col.Name == "Name" {
			continue
		}

		// Map column name to field path
		path := mapColumnNameToFieldPath(col.Name, gvk.Kind)
		if path != nil {
			paths = append(paths, path)
		}
	}

	return paths, nil
}

// mapColumnNameToFieldPath maps a Table API column name to a field path.
// Uses known mappings for common columns.
//
// Only maps to scalar/leaf fields that can be directly selected in the schema tree.
// Computed columns (calculated from multiple fields or arrays) are intentionally excluded
// since they don't have a 1:1 mapping to a selectable field.
func mapColumnNameToFieldPath(columnName string, kind string) []string {
	// Common mappings that apply to most resources
	commonMappings := map[string][]string{
		"Age":       {"metadata", "creationTimestamp"},
		"Namespace": {"metadata", "namespace"},
		"Labels":    {"metadata", "labels"},
	}

	if path, ok := commonMappings[columnName]; ok {
		return path
	}

	// Kind-specific mappings - only scalar fields that can be selected in schema tree
	// Comments indicate Table API columns that CANNOT be mapped (computed/array-based)
	kindMappings := map[string]map[string][]string{
		"Pod": {
			"Status": {"status", "phase"},
			"IP":     {"status", "podIP"},
			"Node":   {"spec", "nodeName"},
			// EXCLUDED - Computed columns (no 1:1 field mapping):
			// - "Ready": computed from status.containerStatuses[].ready (e.g., "1/1")
			// - "Restarts": computed from status.containerStatuses[].restartCount (sum)
			// - "Nominated Node": status.nominatedNodeName (priority 1, rarely used)
			// - "Readiness Gates": spec.readinessGates (array, priority 1)
		},
		"Deployment": {
			"Ready":      {"status", "readyReplicas"},
			"Up-to-date": {"status", "updatedReplicas"},
			"Available":  {"status", "availableReplicas"},
			// EXCLUDED - Computed/array columns:
			// - "Containers": computed from spec.template.spec.containers[].name (joined string)
			// - "Images": computed from spec.template.spec.containers[].image (joined string)
			// - "Selector": spec.selector.matchLabels (map, displayed as label selector string)
		},
		"Service": {
			"Type":       {"spec", "type"},
			"Cluster-IP": {"spec", "clusterIP"},
			// EXCLUDED - Array/computed columns:
			// - "External-IP": spec.externalIPs or status.loadBalancer.ingress (array)
			// - "Port(s)": computed from spec.ports[] (formatted string like "80/TCP")
			// - "Selector": spec.selector (map, displayed as label selector string)
		},
		"ConfigMap": {
			"Data": {"data"},
		},
		"Secret": {
			"Type": {"type"},
			"Data": {"data"},
		},
		"Node": {
			"Version":           {"status", "nodeInfo", "kubeletVersion"},
			"OS-Image":          {"status", "nodeInfo", "osImage"},
			"Kernel-Version":    {"status", "nodeInfo", "kernelVersion"},
			"Container-Runtime": {"status", "nodeInfo", "containerRuntimeVersion"},
			// EXCLUDED - Computed/array columns:
			// - "Status": computed from status.conditions[] (e.g., "Ready", "NotReady")
			// - "Roles": computed from metadata.labels (node-role.kubernetes.io/*)
			// - "Internal-IP": from status.addresses[] where type=InternalIP
			// - "External-IP": from status.addresses[] where type=ExternalIP
		},
		"Namespace": {
			"Status": {"status", "phase"},
		},
		"PersistentVolume": {
			"Reclaim Policy": {"spec", "persistentVolumeReclaimPolicy"},
			"Status":         {"status", "phase"},
			"StorageClass":   {"spec", "storageClassName"},
			"Reason":         {"status", "reason"},
			// EXCLUDED - Computed/array columns:
			// - "Capacity": spec.capacity.storage (map access)
			// - "Access Modes": spec.accessModes (array, displayed as "RWO,RWX")
			// - "Claim": spec.claimRef (object, displayed as "namespace/name")
		},
		"PersistentVolumeClaim": {
			"Status":       {"status", "phase"},
			"Volume":       {"spec", "volumeName"},
			"StorageClass": {"spec", "storageClassName"},
			// EXCLUDED - Computed/array columns:
			// - "Capacity": status.capacity.storage (map access)
			// - "Access Modes": spec.accessModes (array)
		},
		"StatefulSet": {
			"Ready": {"status", "readyReplicas"},
			// EXCLUDED:
			// - "Containers": computed from spec.template.spec.containers[].name
			// - "Images": computed from spec.template.spec.containers[].image
		},
		"DaemonSet": {
			"Desired":    {"status", "desiredNumberScheduled"},
			"Current":    {"status", "currentNumberScheduled"},
			"Ready":      {"status", "numberReady"},
			"Up-to-date": {"status", "updatedNumberScheduled"},
			"Available":  {"status", "numberAvailable"},
			// EXCLUDED:
			// - "Node Selector": spec.template.spec.nodeSelector (map)
			// - "Containers": computed from spec.template.spec.containers[].name
			// - "Images": computed from spec.template.spec.containers[].image
		},
		"ReplicaSet": {
			"Desired": {"spec", "replicas"},
			"Current": {"status", "replicas"},
			"Ready":   {"status", "readyReplicas"},
			// EXCLUDED:
			// - "Containers": computed from spec.template.spec.containers[].name
			// - "Images": computed from spec.template.spec.containers[].image
			// - "Selector": spec.selector.matchLabels (map)
		},
		"Job": {
			"Completions": {"spec", "completions"},
			"Duration":    {"status", "completionTime"},
			// EXCLUDED:
			// - "Status": computed from status.conditions[] and status.succeeded/failed
			// - "Containers": computed from spec.template.spec.containers[].name
			// - "Images": computed from spec.template.spec.containers[].image
			// - "Selector": spec.selector.matchLabels (map)
		},
		"CronJob": {
			"Schedule":      {"spec", "schedule"},
			"Suspend":       {"spec", "suspend"},
			"Last Schedule": {"status", "lastScheduleTime"},
			// EXCLUDED:
			// - "Active": status.active (array of object references)
			// - "Containers": computed from spec.jobTemplate.spec.template.spec.containers[].name
			// - "Images": computed from spec.jobTemplate.spec.template.spec.containers[].image
		},
		"Ingress": {
			"Class": {"spec", "ingressClassName"},
			// EXCLUDED:
			// - "Hosts": computed from spec.rules[].host (joined string)
			// - "Address": status.loadBalancer.ingress[].ip/hostname (array)
			// - "Ports": computed from spec.rules[].http.paths[].backend.service.port
		},
	}

	if kindMap, ok := kindMappings[kind]; ok {
		if path, ok := kindMap[columnName]; ok {
			return path
		}
	}

	return nil
}

// jsonPathToFieldPath converts a JSONPath expression to a field path.
// Examples:
//   - ".spec.replicas" -> ["spec", "replicas"]
//   - ".status.conditions[0].type" -> ["status", "conditions", "*", "type"]
//   - ".metadata.creationTimestamp" -> ["metadata", "creationTimestamp"]
//
// Note: Array indices and wildcards are converted to "*" for tree navigation.
func jsonPathToFieldPath(jsonPath string) []string {
	if jsonPath == "" {
		return nil
	}

	// Remove leading dot
	jsonPath = strings.TrimPrefix(jsonPath, ".")

	var parts []string
	current := ""

	for i := 0; i < len(jsonPath); i++ {
		ch := jsonPath[i]
		switch ch {
		case '.':
			if current != "" {
				parts = append(parts, current)
				current = ""
			}
		case '[':
			// Save current part before bracket
			if current != "" {
				parts = append(parts, current)
				current = ""
			}
			// Skip to closing bracket and add wildcard
			for i < len(jsonPath) && jsonPath[i] != ']' {
				i++
			}
			parts = append(parts, "*")
		default:
			current += string(ch)
		}
	}

	// Add remaining part
	if current != "" {
		parts = append(parts, current)
	}

	return parts
}
