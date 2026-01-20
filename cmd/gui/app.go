package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/flavono123/kattle/internal/kube"
	"github.com/flavono123/kattle/internal/store"
)

// logMemoryStats logs current goroutine count and event metrics for debugging
func logMemoryStats(label string) {
	emitted, dropped := kube.GetEventMetrics()
	log.Printf("[DEBUG] %s: goroutines=%d, events_emitted=%d, events_dropped=%d",
		label, goruntime.NumGoroutine(), emitted, dropped)
}

// App struct
type App struct {
	ctx           context.Context
	favoriteStore *store.Store

	// Watch state
	watchMu     sync.RWMutex
	controllers []*watchController
	stopChs     []chan struct{}
	watchDone   chan struct{}
	fieldStore  *kube.FieldStore // key: "context/namespace/name" → value: extracted fields
}

// watchController wraps a ResourceController with context info
type watchController struct {
	contextName string
	controller  *kube.ResourceController
}

// NewApp creates a new App application struct
func NewApp() *App {
	return &App{}
}

// startup is called when the app starts. The context is saved
// so we can call the runtime methods
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx

	// Detect dev mode from wails environment
	env := runtime.Environment(ctx)
	devMode := env.BuildType == "dev"

	s, err := store.NewStore(store.StoreOptions{DevMode: devMode})
	if err != nil {
		log.Printf("failed to create favorite store: %v", err)
	} else {
		a.favoriteStore = s
		if err := a.favoriteStore.Load(); err != nil {
			log.Printf("failed to load favorite views: %v", err)
		}
		log.Printf("favorite store initialized (dev=%v)", devMode)
	}
}

// shutdown is called when the app is closing.
// Cleans up resources including active watches.
func (a *App) shutdown(ctx context.Context) {
	log.Printf("App shutting down, cleaning up resources...")
	a.StopWatch()
	log.Printf("App shutdown complete")
}

// Greet returns a greeting for the given name
func (a *App) Greet(name string) string {
	return fmt.Sprintf("Hello %s, It's show time!", name)
}

// ListContexts returns all available Kubernetes contexts
func (a *App) ListContexts() ([]string, error) {
	return kube.ListContexts()
}

// RefreshContexts invalidates the kubeconfig cache and reloads contexts
func (a *App) RefreshContexts() ([]string, error) {
	kube.InvalidateKubeconfigCache()
	return kube.ListContexts()
}

// GetCurrentContext returns the current active Kubernetes context
func (a *App) GetCurrentContext() (string, error) {
	return kube.GetCurrentContext()
}

// ContextConnectionResult represents the result of connecting to a context
type ContextConnectionResult struct {
	Context string `json:"context"`
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

// ConnectToContexts attempts to create clients for the specified contexts
// Returns a list of results indicating success or failure for each context
func (a *App) ConnectToContexts(contexts []string) []ContextConnectionResult {
	results := make([]ContextConnectionResult, 0, len(contexts))

	for _, contextName := range contexts {
		result := ContextConnectionResult{
			Context: contextName,
			Success: false,
		}

		// Try to create a client for this context
		// This validates the context and ensures we can connect
		discoveryClient, err := kube.DiscoveryClientForContext(contextName)
		if err != nil {
			result.Error = err.Error()
			results = append(results, result)
			continue
		}

		// Actually verify authentication by making a lightweight API call
		_, err = discoveryClient.ServerVersion()
		if err != nil {
			// Check if error is related to tsh authentication
			if strings.Contains(err.Error(), "tsh") {
				// Try tsh kube login
				attempted, loginErr := kube.TryTshKubeLogin(contextName)
				if attempted {
					if loginErr != nil {
						result.Error = fmt.Sprintf("tsh kube login failed: %v", loginErr)
						results = append(results, result)
						continue
					}

					// Login succeeded, invalidate cache and retry
					kube.InvalidateClientCache(contextName)

					// Recreate client and retry
					discoveryClient, err = kube.DiscoveryClientForContext(contextName)
					if err != nil {
						result.Error = err.Error()
						results = append(results, result)
						continue
					}

					_, err = discoveryClient.ServerVersion()
					if err != nil {
						result.Error = err.Error()
						results = append(results, result)
						continue
					}

					// Success after retry
					result.Success = true
					results = append(results, result)
					continue
				}
			}

			// Original error (not relogin or not using tsh)
			result.Error = err.Error()
		} else {
			result.Success = true
		}

		results = append(results, result)
	}

	return results
}

// MultiClusterGVK represents a Kubernetes resource (Group/Version/Kind) with context availability
type MultiClusterGVK struct {
	Group      string   `json:"group"`
	Version    string   `json:"version"`
	Kind       string   `json:"kind"`
	ShortNames []string `json:"shortNames"` // Short names from API (e.g., "po" for Pod, "deploy" for Deployment)
	Contexts   []string `json:"contexts"`   // Contexts where this GVK is available
	AllCount   int      `json:"allCount"`   // Total number of contexts
}

// GetGVKs retrieves all unique GVKs from the specified contexts
// Returns a merged and deduplicated list of GVKs with context availability info
func (a *App) GetGVKs(contexts []string) []MultiClusterGVK {
	// Map to track unique GVKs: key = "group/version/kind"
	resourceMap := make(map[string]*MultiClusterGVK)
	var mu sync.Mutex
	var wg sync.WaitGroup

	// Process contexts in parallel
	for _, contextName := range contexts {
		wg.Add(1)
		go func(ctx string) {
			defer wg.Done()

			gvkInfos, err := kube.GetGVKInfosForContext(ctx)
			if err != nil {
				// Skip contexts that fail
				return
			}

			for _, info := range gvkInfos {
				// Create unique key using GVK (no GVR conversion needed)
				key := fmt.Sprintf("%s/%s/%s", info.Group, info.Version, info.Kind)

				// Thread-safe map update
				mu.Lock()
				if existing, exists := resourceMap[key]; exists {
					// Add context to existing GVK
					existing.Contexts = append(existing.Contexts, ctx)
					// ShortNames should be consistent across contexts, but take the first non-empty one
					if len(existing.ShortNames) == 0 && len(info.ShortNames) > 0 {
						existing.ShortNames = info.ShortNames
					}
				} else {
					// Create new GVK entry
					resourceMap[key] = &MultiClusterGVK{
						Group:      info.Group,
						Version:    info.Version,
						Kind:       info.Kind,
						ShortNames: info.ShortNames,
						Contexts:   []string{ctx},
						AllCount:   len(contexts),
					}
				}
				mu.Unlock()
			}
		}(contextName)
	}

	// Wait for all goroutines to complete
	wg.Wait()

	// Convert map to slice
	results := make([]MultiClusterGVK, 0, len(resourceMap))
	for _, info := range resourceMap {
		results = append(results, *info)
	}

	return results
}

// TreeNode represents a node in the navigation tree (frontend format)
type TreeNode struct {
	Name     string      `json:"name"`
	Type     string      `json:"type"`     // e.g., "string", "[]Pod", "map[string]"
	FullPath []string    `json:"fullPath"` // for selection/search
	Level    int         `json:"level"`
	Children []*TreeNode `json:"children"`
	// Note: Expanded and Selected state are managed in the frontend
}

// GetNodeTree retrieves the node tree for a given GVK and contexts
// Returns a tree structure representing the schema + actual data
func (a *App) GetNodeTree(gvk MultiClusterGVK, contexts []string) ([]*TreeNode, error) {
	// Convert MultiClusterGVK to schema.GroupVersionKind
	schemaGVK := schema.GroupVersionKind{
		Group:   gvk.Group,
		Version: gvk.Version,
		Kind:    gvk.Kind,
	}

	// 1. Get field tree from schema (use first available context from GVK)
	var fields map[string]*kube.Field
	var err error
	if len(gvk.Contexts) > 0 {
		// Use the first context where this GVK is available
		fields, err = kube.CreateFieldTreeForContext(gvk.Contexts[0], schemaGVK)
	} else {
		fields, err = kube.CreateFieldTree(schemaGVK)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to create field tree: %w", err)
	}

	// 2. Create node tree - use FieldStore if watches are active (memory-efficient)
	var nodes map[string]*kube.Node
	if a.fieldStore.Count() > 0 {
		// Use FieldStore's structure metadata for tree building
		nodes = kube.CreateNodeTreeFromStore(fields, a.fieldStore, []string{})
	} else {
		// No active watch, fetch resources directly for tree building
		objs, fetchErr := a.getResourcesWithCleanup(schemaGVK, contexts)
		if fetchErr != nil {
			return nil, fmt.Errorf("failed to get resources: %w", fetchErr)
		}
		nodes = kube.CreateNodeTree(fields, objs, []string{})
	}

	// 3. Convert to frontend format (remove UI state, convert to array)
	return convertNodeTree(nodes), nil
}

// GetDefaultSelectedPaths returns the default fields to select for a GVK.
// For CRDs, this returns paths from additionalPrinterColumns.
// For built-in resources, this uses Table API printer columns when available.
func (a *App) GetDefaultSelectedPaths(gvk MultiClusterGVK, contexts []string) [][]string {
	schemaGVK := schema.GroupVersionKind{
		Group:   gvk.Group,
		Version: gvk.Version,
		Kind:    gvk.Kind,
	}

	// Try each context until we get printer columns
	for _, contextName := range contexts {
		paths, err := kube.GetPrinterColumnsForContext(contextName, schemaGVK)
		if err == nil && len(paths) > 0 {
			return paths
		}
	}

	// Also try with contexts from GVK if different
	for _, contextName := range gvk.Contexts {
		paths, err := kube.GetPrinterColumnsForContext(contextName, schemaGVK)
		if err == nil && len(paths) > 0 {
			return paths
		}
	}

	return nil
}

// getWatchedResources returns resources from active watch controllers if available
func (a *App) getWatchedResources() []*unstructured.Unstructured {
	a.watchMu.RLock()
	defer a.watchMu.RUnlock()

	if len(a.controllers) == 0 {
		return nil
	}

	var allObjs []*unstructured.Unstructured
	for _, wc := range a.controllers {
		objs := wc.controller.Objects()
		allObjs = append(allObjs, objs...)
	}
	return allObjs
}

// getResourcesWithCleanup fetches resources and properly cleans up controllers
func (a *App) getResourcesWithCleanup(gvk schema.GroupVersionKind, contexts []string) ([]*unstructured.Unstructured, error) {
	var allObjs []*unstructured.Unstructured
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, contextName := range contexts {
		wg.Add(1)
		go func(ctx string) {
			defer wg.Done()

			gvr, err := kube.GetGVRForContext(ctx, gvk)
			if err != nil {
				log.Printf("Warning: failed to get GVR for %s in context %s: %v", gvk.Kind, ctx, err)
				return
			}

			controller := kube.NewResourceControllerForContext(ctx, gvr)
			stopCh, err := controller.Inform()
			if err != nil {
				log.Printf("Warning: failed to start informer for %s in context %s: %v", gvk.Kind, ctx, err)
				return
			}

			// Get objects then immediately cleanup
			objs := controller.Objects()
			close(stopCh)
			controller.Close()

			mu.Lock()
			allObjs = append(allObjs, objs...)
			mu.Unlock()
		}(contextName)
	}

	wg.Wait()
	return allObjs, nil
}

// GetResources returns resources from active watch or fetches them directly
// Deprecated: Frontend should use watch events (ADDED) for initial data instead
// This is kept for backward compatibility and manual refresh
func (a *App) GetResources(gvk MultiClusterGVK, contexts []string) ([]map[string]interface{}, error) {
	// Prefer active watch data to avoid duplicate List calls
	objs := a.getWatchedResources()

	if len(objs) == 0 {
		// No active watch, fetch with cleanup
		schemaGVK := schema.GroupVersionKind{
			Group:   gvk.Group,
			Version: gvk.Version,
			Kind:    gvk.Kind,
		}
		var err error
		objs, err = a.getResourcesWithCleanup(schemaGVK, contexts)
		if err != nil {
			return nil, err
		}
	}

	// Convert to frontend format with _context field
	var allResources []map[string]interface{}
	for _, obj := range objs {
		resource := obj.Object
		// _context should already be set by watch, but ensure it exists
		if _, ok := resource["_context"]; !ok {
			resource["_context"] = ""
		}
		allResources = append(allResources, resource)
	}

	return allResources, nil
}

// ResourceEventMeta represents a lightweight watch event (Pull Model)
// Only contains metadata - frontend fetches full object via GetResources()
type ResourceEventMeta struct {
	Type string `json:"type"` // "ADDED", "MODIFIED", "DELETED"
	Key  string `json:"key"`  // "context/namespace/name" unique identifier
}

// makeResourceKey creates a unique cache key for a resource
func makeResourceKey(context, namespace, name string) string {
	return fmt.Sprintf("%s/%s/%s", context, namespace, name)
}

// StartWatch starts watching resources for the given GVK across specified contexts
// Watch events are emitted via Wails runtime events ("resource:update")
func (a *App) StartWatch(gvk MultiClusterGVK, contexts []string) error {
	// Stop any existing watch first
	a.StopWatch()

	a.watchMu.Lock()
	defer a.watchMu.Unlock()

	schemaGVK := schema.GroupVersionKind{
		Group:   gvk.Group,
		Version: gvk.Version,
		Kind:    gvk.Kind,
	}

	a.controllers = make([]*watchController, 0, len(contexts))
	a.stopChs = make([]chan struct{}, 0, len(contexts))
	a.watchDone = make(chan struct{})
	a.fieldStore = kube.NewFieldStore()

	var wg sync.WaitGroup

	for _, contextName := range contexts {
		gvr, err := kube.GetGVRForContext(contextName, schemaGVK)
		if err != nil {
			log.Printf("Warning: failed to get GVR for %s in context %s: %v", schemaGVK.Kind, contextName, err)
			continue
		}

		controller := kube.NewResourceControllerForContext(contextName, gvr)
		ctx := contextName // capture for closures

		// Synchronous callback for initial sync - called in informer's goroutine, no race condition.
		// This populates FieldStore directly during WaitForCacheSync, before any event can be dropped.
		onSync := func(eventType kube.EventType, obj *unstructured.Unstructured) {
			key := makeResourceKey(ctx, obj.GetNamespace(), obj.GetName())
			if eventType == kube.EventDeleted {
				a.fieldStore.Delete(key)
			} else {
				a.fieldStore.Update(key, obj)
			}
			// Note: We don't emit events to frontend during initial sync.
			// Frontend calls GetAllResourceKeys() after StartWatch returns.
		}

		// Start the informer with KeyOnlyStore for memory efficiency.
		// The onSync callback processes all initial objects synchronously.
		// After sync completes, ongoing events are emitted via WatchEvents() channel.
		stopCh, err := controller.InformWithKeyOnlyStore(onSync)
		if err != nil {
			log.Printf("Warning: failed to start watch for %s in context %s: %v", schemaGVK.Kind, contextName, err)
			continue
		}

		log.Printf("Initial sync complete for context %s, FieldStore count: %d", ctx, a.fieldStore.Count())

		a.controllers = append(a.controllers, &watchController{
			contextName: ctx,
			controller:  controller,
		})
		a.stopChs = append(a.stopChs, stopCh)

		// Start goroutine to forward ONGOING events to frontend (after initial sync).
		// These are real-time updates from watch, not initial list.
		wg.Add(1)
		go func(ctxName string, ctrl *kube.ResourceController) {
			defer wg.Done()
			for {
				select {
				case event := <-ctrl.WatchEvents():
					if event.Obj == nil {
						continue // skip invalid events
					}

					key := makeResourceKey(ctxName, event.Obj.GetNamespace(), event.Obj.GetName())

					if string(event.Type) == "DELETED" {
						a.fieldStore.Delete(key)
					} else {
						a.fieldStore.Update(key, event.Obj)
					}

					// Emit only lightweight metadata (no full object via eval)
					runtime.EventsEmit(a.ctx, "resource:update", ResourceEventMeta{
						Type: string(event.Type),
						Key:  key,
					})
				case <-ctrl.Done():
					return
				}
			}
		}(ctx, controller)
	}

	// Wait for all event forwarders to finish in background
	go func() {
		wg.Wait()
		close(a.watchDone)
	}()

	log.Printf("Started watching %s/%s/%s across %d contexts", gvk.Group, gvk.Version, gvk.Kind, len(a.controllers))
	logMemoryStats("StartWatch")
	return nil
}

// StopWatch stops all active resource watches
func (a *App) StopWatch() {
	a.watchMu.Lock()
	defer a.watchMu.Unlock()

	if len(a.stopChs) == 0 {
		return
	}

	// Close all stop channels to stop informers
	for _, stopCh := range a.stopChs {
		close(stopCh)
	}

	// Close all controllers to unblock goroutines and release resources
	for _, wc := range a.controllers {
		wc.controller.Close()
	}

	// Wait for event forwarders to finish (with timeout)
	if a.watchDone != nil {
		select {
		case <-a.watchDone:
		case <-time.After(2 * time.Second):
			log.Printf("Warning: watch cleanup timed out")
		}
	}

	a.controllers = nil
	a.stopChs = nil
	a.watchDone = nil

	// Clear field store
	if a.fieldStore != nil {
		a.fieldStore.Clear()
	}
	a.fieldStore = nil

	log.Printf("Stopped all resource watches")
	logMemoryStats("StopWatch")

	// Reset event metrics for next watch cycle
	kube.ResetEventMetrics()
}

// SetSelectedFields updates the fields to extract for table display.
// Called by frontend when user changes column selection.
// Fields should be dot-notation paths like "status.phase", "spec.replicas".
func (a *App) SetSelectedFields(fields []string) {
	if a.fieldStore != nil {
		a.fieldStore.SetSelectedFields(fields)
	}
}

// GetResourcesByKeys fetches resources from FieldStore by keys (Pull Model)
// Called by frontend after receiving resource:update events
func (a *App) GetResourcesByKeys(keys []string) []map[string]any {
	result := make([]map[string]any, 0, len(keys))
	if a.fieldStore == nil {
		return result
	}

	for _, key := range keys {
		if obj := a.fieldStore.ReconstructObject(key); obj != nil {
			// Extract context from key (format: "context/namespace/name")
			parts := strings.SplitN(key, "/", 2)
			if len(parts) > 0 {
				obj["_context"] = parts[0]
			}
			result = append(result, obj)
		}
	}
	return result
}

// GetAllResourceKeys returns all resource keys currently in the FieldStore.
// Called by frontend after StartWatch to get initial batch of keys.
// This is needed because trySend drops events when buffer is full during initial sync.
func (a *App) GetAllResourceKeys() []string {
	a.watchMu.RLock()
	defer a.watchMu.RUnlock()

	if a.fieldStore == nil {
		return []string{}
	}

	return a.fieldStore.List()
}

// convertNodeTree converts kube.Node map to frontend TreeNode array
func convertNodeTree(nodes map[string]*kube.Node) []*TreeNode {
	result := make([]*TreeNode, 0, len(nodes))

	for name, node := range nodes {
		// Skip apiVersion and kind (TUI also skips these)
		if name == "apiVersion" || name == "kind" {
			continue
		}

		treeNode := &TreeNode{
			Name:     node.Name(),
			Type:     node.Type(),
			FullPath: node.NodeFullPath(), // Use NodeFullPath instead of FullPath to include array indices
			Level:    node.Level(),
			Children: convertNodeTree(node.Children()),
		}

		result = append(result, treeNode)
	}

	// Sort result: * always comes first, then numeric indices (sorted numerically), then alphabetically
	sort.Slice(result, func(i, j int) bool {
		// * always comes first
		if result[i].Name == "*" {
			return true
		}
		if result[j].Name == "*" {
			return false
		}

		// Try to parse as numbers
		numI, errI := strconv.Atoi(result[i].Name)
		numJ, errJ := strconv.Atoi(result[j].Name)

		// Both are numbers: sort numerically
		if errI == nil && errJ == nil {
			return numI < numJ
		}

		// One is a number, one is not: numbers come first (after *)
		if errI == nil {
			return true
		}
		if errJ == nil {
			return false
		}

		// Both are strings: sort alphabetically
		return result[i].Name < result[j].Name
	})

	return result
}

// SaveFile opens a save file dialog and saves the content to the selected file
// Returns the path where the file was saved, or empty string if cancelled
func (a *App) SaveFile(defaultFilename string, content string) (string, error) {
	// Get user's Downloads directory as default location
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}
	defaultDir := filepath.Join(homeDir, "Downloads")

	// Open save file dialog
	filePath, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		DefaultDirectory: defaultDir,
		DefaultFilename:  defaultFilename,
		Title:            "Save CSV File",
		Filters: []runtime.FileFilter{
			{
				DisplayName: "CSV Files (*.csv)",
				Pattern:     "*.csv",
			},
			{
				DisplayName: "All Files (*.*)",
				Pattern:     "*.*",
			},
		},
	})

	if err != nil {
		return "", fmt.Errorf("failed to open save dialog: %w", err)
	}

	// User cancelled the dialog
	if filePath == "" {
		return "", nil
	}

	// Ensure .csv extension
	if !strings.HasSuffix(filePath, ".csv") {
		filePath += ".csv"
	}

	// Write content to file
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		return "", fmt.Errorf("failed to write file: %w", err)
	}

	return filePath, nil
}

// FavoriteView types for frontend binding
type FavoriteViewGVK struct {
	Group   string `json:"group"`
	Version string `json:"version"`
	Kind    string `json:"kind"`
}

type FavoriteViewResponse struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	GVK       FavoriteViewGVK `json:"gvk"`
	Fields    [][]string      `json:"fields"`
	CreatedAt string          `json:"createdAt"`
	UpdatedAt string          `json:"updatedAt"`
}

func favoriteViewToResponse(v *store.FavoriteView) FavoriteViewResponse {
	return FavoriteViewResponse{
		ID:   v.ID,
		Name: v.Name,
		GVK: FavoriteViewGVK{
			Group:   v.GVK.Group,
			Version: v.GVK.Version,
			Kind:    v.GVK.Kind,
		},
		Fields:    v.Fields,
		CreatedAt: v.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt: v.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

// ListFavoriteViews returns all favorite views.
func (a *App) ListFavoriteViews() ([]FavoriteViewResponse, error) {
	if a.favoriteStore == nil {
		return nil, fmt.Errorf("favorite store not initialized")
	}

	views := a.favoriteStore.ListAll()
	result := make([]FavoriteViewResponse, len(views))
	for i, v := range views {
		result[i] = favoriteViewToResponse(&v)
	}
	return result, nil
}

// GetFavoriteViewsForGVK returns favorite views for a specific GVK.
func (a *App) GetFavoriteViewsForGVK(group, version, kind string) ([]FavoriteViewResponse, error) {
	if a.favoriteStore == nil {
		return nil, fmt.Errorf("favorite store not initialized")
	}

	gvk := store.GVKRef{Group: group, Version: version, Kind: kind}
	views := a.favoriteStore.ListByGVK(gvk)
	result := make([]FavoriteViewResponse, len(views))
	for i, v := range views {
		result[i] = favoriteViewToResponse(&v)
	}
	return result, nil
}

// SaveFavoriteView saves current selection as a favorite.
func (a *App) SaveFavoriteView(name, group, version, kind string, fields [][]string) (*FavoriteViewResponse, error) {
	if a.favoriteStore == nil {
		return nil, fmt.Errorf("favorite store not initialized")
	}

	gvk := store.GVKRef{Group: group, Version: version, Kind: kind}
	view, err := a.favoriteStore.Create(name, gvk, fields)
	if err != nil {
		return nil, err
	}

	if err := a.favoriteStore.Save(); err != nil {
		return nil, fmt.Errorf("failed to save: %w", err)
	}

	result := favoriteViewToResponse(view)
	return &result, nil
}

// DeleteFavoriteView removes a favorite view by ID.
func (a *App) DeleteFavoriteView(id string) error {
	if a.favoriteStore == nil {
		return fmt.Errorf("favorite store not initialized")
	}

	if err := a.favoriteStore.Delete(id); err != nil {
		return err
	}

	return a.favoriteStore.Save()
}

// RenameFavoriteView updates the name of a favorite view.
func (a *App) RenameFavoriteView(id, newName string) (*FavoriteViewResponse, error) {
	if a.favoriteStore == nil {
		return nil, fmt.Errorf("favorite store not initialized")
	}

	view, err := a.favoriteStore.Rename(id, newName)
	if err != nil {
		return nil, err
	}

	if err := a.favoriteStore.Save(); err != nil {
		return nil, fmt.Errorf("failed to save: %w", err)
	}

	result := favoriteViewToResponse(view)
	return &result, nil
}
