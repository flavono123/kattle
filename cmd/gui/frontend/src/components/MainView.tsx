import { useState, useEffect, useRef, useCallback, useMemo } from "react";
import { CommandPalette } from "./CommandPalette";
import { DynamicFieldTree, DynamicFieldTreeHandle } from "./DynamicFieldTree";
import { DIYTable, DIYTableHandle } from "./DIYTable";
import { ContextBar } from "./ContextBar";
import { NavHeader } from "./NavHeader";
import { QuickAccessBar, QuickAccessBarHandle } from "./QuickAccessBar";
import { KeymapBar, FocusedPanel } from "./KeymapBar";
import { Button } from "./ui/button";
import { Kbd } from "./ui/kbd";
import { ResizablePanelGroup, ResizablePanel, ResizableHandle } from "./ui/resizable";
import { PanelLeft } from "lucide-react";
import { GetGVKs, IsWindowedModeEnabled } from "../../wailsjs/go/main/App";
import { main } from "../../wailsjs/go/models";
import { ImperativePanelHandle } from "react-resizable-panels";
import { useFavoriteViews } from "@/hooks/useFavoriteViews";
import { DEFAULT_COLUMNS } from "@/lib/constants";
import { useTheme } from "next-themes";
import { toggleThemeWithAnimation } from "@/lib/theme-animation";
import { isInputElementFocused } from "@/lib/dom-utils";

interface MainViewProps {
  selectedContexts: string[];
  connectedContexts: string[];
  onBackToContexts: () => void;
}

// Path delimiter must match DynamicFieldTree's delimiter
const PATH_DELIMITER = '\x00';

export function MainView({ selectedContexts, connectedContexts, onBackToContexts }: MainViewProps) {
  const [showCommandPalette, setShowCommandPalette] = useState(false);
  const [gvks, setGVKs] = useState<main.MultiClusterGVK[]>([]);
  const [loading, setLoading] = useState(true);
  const [selectedGVK, setSelectedGVK] = useState<main.MultiClusterGVK | null>(null);
  const [selectedFields, setSelectedFields] = useState<string[][]>([]);
  // Focus sync state between DynamicFieldTree and DIYTable
  const [focusedFieldPath, setFocusedFieldPath] = useState<string[] | null>(null);
  // Pending favorite ID to apply after GVK switch (use ref to avoid stale closure)
  const pendingFavoriteIdRef = useRef<string | null>(null);
  // Track if there's a pending favorite (state version for triggering re-render)
  const [hasPendingFavorite, setHasPendingFavorite] = useState(false);
  const loadedRef = useRef(false);
  const sidebarPanelRef = useRef<ImperativePanelHandle>(null);
  const fieldTreeRef = useRef<DynamicFieldTreeHandle>(null);
  const diyTableRef = useRef<DIYTableHandle>(null);
  const quickAccessBarRef = useRef<QuickAccessBarHandle>(null);
  const [isSidebarCollapsed, setIsSidebarCollapsed] = useState(false);
  const [sidebarWidthPercent, setSidebarWidthPercent] = useState(20); // Track sidebar width for ContextBar alignment
  const [focusedPanel, setFocusedPanel] = useState<FocusedPanel>(null);
  const [useWindowedMode, setUseWindowedMode] = useState(false); // Controlled by KATTLE_USE_SQLSTORE env
  const { setTheme, resolvedTheme } = useTheme();

  // Handle theme toggle with animation
  const handleThemeToggle = useCallback((event: React.MouseEvent | React.KeyboardEvent) => {
    toggleThemeWithAnimation(event, resolvedTheme, setTheme);
  }, [resolvedTheme, setTheme]);

  // Convert selectedFields to Set<string> for favorite comparison
  const selectedPaths = useMemo(() => {
    return new Set(selectedFields.map((f) => JSON.stringify(f)));
  }, [selectedFields]);

  // Preview field: focusedFieldPath if not in selectedFields (for muted preview column)
  // Excludes default columns (_context, metadata.name) as they're always visible
  const previewField = useMemo(() => {
    if (!focusedFieldPath) return undefined;

    // Default columns should not show as preview (they're always visible)
    const focusedPath = focusedFieldPath.join('.');
    const isDefaultColumn = DEFAULT_COLUMNS.includes(focusedPath as typeof DEFAULT_COLUMNS[number]);
    if (isDefaultColumn) return undefined;

    const isSelected = selectedFields.some((f) => f.join('.') === focusedPath);
    return isSelected ? undefined : focusedFieldPath;
  }, [focusedFieldPath, selectedFields]);

  // Favorite views hook
  const {
    allFavorites,
    activeFavorite,
    saveFavorite,
    applyFavorite,
    clearFavorite,
    deleteFavorite,
    renameFavorite,
  } = useFavoriteViews({
    currentGVK: selectedGVK,
    selectedPaths,
  });

  // Load GVKs once when MainView mounts
  useEffect(() => {
    // Prevent multiple loads
    if (loadedRef.current) return;
    loadedRef.current = true;

    const loadGVKs = async () => {
      console.log("MainView: Loading GVKs for contexts:", connectedContexts);
      setLoading(true);
      try {
        // Check if windowed mode should be enabled (KATTLE_USE_SQLSTORE=1)
        const windowedEnabled = await IsWindowedModeEnabled();
        setUseWindowedMode(windowedEnabled);
        if (windowedEnabled) {
          console.log("MainView: Windowed mode enabled (KATTLE_USE_SQLSTORE=1)");
        }

        const gvkList = await GetGVKs(connectedContexts);
        console.log("MainView: Loaded GVKs:", gvkList.length, "items");
        setGVKs(gvkList);
      } catch (error) {
        console.error("MainView: Failed to load GVKs:", error);
      } finally {
        setLoading(false);
        console.log("MainView: Loading complete");
      }
    };

    loadGVKs();
  }, [connectedContexts]);

  // Check if search is focused in nav panel
  const isNavSearchFocused = useCallback(() => {
    return fieldTreeRef.current?.isSearchFocused() ?? false;
  }, []);

  // Check if search is focused in result table
  const isTableSearchFocused = useCallback(() => {
    return diyTableRef.current?.isSearchFocused() ?? false;
  }, []);

  // Reset selectedFields when GVK changes
  useEffect(() => {
    setSelectedFields([]);
  }, [selectedGVK]);

  const toggleSidebar = () => {
    const panel = sidebarPanelRef.current;
    if (panel) {
      if (isSidebarCollapsed) {
        panel.expand();
      } else {
        panel.collapse();
      }
    }
  };

  const handleClearAllFields = useCallback(() => {
    fieldTreeRef.current?.clearSelections();
  }, []);

  const handleSearch = useCallback(() => {
    fieldTreeRef.current?.toggleSearch();
  }, []);

  const handleSaveFavorite = useCallback(async (name: string) => {
    await saveFavorite(name);
  }, [saveFavorite]);

  // Convert favorite fields to path set for NavigationPanel
  const fieldsToPathSet = useCallback((fields: string[][]) => {
    return new Set(fields.map((f) => f.join(PATH_DELIMITER)));
  }, []);

  // Apply favorite by ID (sets paths in NavigationPanel and activates)
  const applyFavoriteById = useCallback((id: string) => {
    const fields = applyFavorite(id);
    if (!fields || !fieldTreeRef.current) {
      return;
    }

    const pathSet = fieldsToPathSet(fields);
    fieldTreeRef.current.setSelectedPaths(pathSet);
  }, [applyFavorite, fieldsToPathSet]);

  // Called when NavigationPanel finishes loading - apply pending favorite if any
  const handleNavigationReady = useCallback(() => {
    if (pendingFavoriteIdRef.current) {
      applyFavoriteById(pendingFavoriteIdRef.current);
      pendingFavoriteIdRef.current = null;
      setHasPendingFavorite(false);
    }
  }, [applyFavoriteById]);

  // Apply favorite (same logic as CommandPalette)
  const handleApplyFavorite = useCallback((favorite: main.FavoriteViewResponse) => {
    const matchingGVK = gvks.find(
      (g) =>
        g.group === favorite.gvk.group &&
        g.version === favorite.gvk.version &&
        g.kind === favorite.gvk.kind
    );
    if (!matchingGVK) return;

    // Check if same GVK is already selected
    const isSameGVK = selectedGVK &&
      selectedGVK.group === matchingGVK.group &&
      selectedGVK.version === matchingGVK.version &&
      selectedGVK.kind === matchingGVK.kind;

    if (isSameGVK) {
      // Same GVK - apply directly without waiting for onReady
      applyFavoriteById(favorite.id);
    } else {
      // Different GVK - need to wait for NavigationPanel to load
      pendingFavoriteIdRef.current = favorite.id;
      setHasPendingFavorite(true);
      setSelectedGVK(matchingGVK);
    }
  }, [gvks, selectedGVK, applyFavoriteById]);

  // Global keyboard shortcuts
  useEffect(() => {
    const handleKeydown = (e: KeyboardEvent) => {
      // Skip if CommandPalette is open
      if (showCommandPalette) return;

      // cmd+k to toggle CommandPalette
      if ((e.metaKey || e.ctrlKey) && e.key === 'k') {
        e.preventDefault();
        setShowCommandPalette((prev) => !prev);
        return;
      }

      // cmd+shift+a to clear all field selections
      if ((e.metaKey || e.ctrlKey) && e.shiftKey && e.key.toLowerCase() === 'a') {
        e.preventDefault();
        fieldTreeRef.current?.clearSelections();
        return;
      }

      // cmd+shift+c to export to clipboard
      if ((e.metaKey || e.ctrlKey) && e.shiftKey && e.key.toLowerCase() === 'c') {
        e.preventDefault();
        diyTableRef.current?.exportToClipboard();
        return;
      }

      // cmd+shift+s to download as file
      if ((e.metaKey || e.ctrlKey) && e.shiftKey && e.key.toLowerCase() === 's') {
        e.preventDefault();
        diyTableRef.current?.exportToFile();
        return;
      }

      // cmd+s to save as favorite (when not already saved)
      if ((e.metaKey || e.ctrlKey) && !e.shiftKey && e.key.toLowerCase() === 's') {
        e.preventDefault();
        quickAccessBarRef.current?.openSavePopover();
        return;
      }

      // cmd+1~9 to apply favorite (works regardless of panel focus)
      if ((e.metaKey || e.ctrlKey) && e.key >= '1' && e.key <= '9') {
        const index = parseInt(e.key) - 1;
        if (index < allFavorites.length) {
          e.preventDefault();
          handleApplyFavorite(allFavorites[index]);
        }
        return;
      }

      // cmd+f to focus/toggle search in current panel
      if ((e.metaKey || e.ctrlKey) && e.key === 'f') {
        e.preventDefault();
        if (focusedPanel === 'nav') {
          fieldTreeRef.current?.toggleSearch();
        } else if (focusedPanel === 'table') {
          diyTableRef.current?.focusSearch();
        } else {
          // Default to nav panel
          setFocusedPanel('nav');
          fieldTreeRef.current?.toggleSearch();
        }
        return;
      }

      // Tab to switch panel focus (always works, even in search inputs)
      if (e.key === 'Tab' && !e.metaKey && !e.ctrlKey && !e.shiftKey) {
        e.preventDefault();
        setFocusedPanel((prev) => {
          if (prev === 'nav') return 'table';
          if (prev === 'table') return 'nav';
          return 'nav'; // Default to nav if no panel is focused
        });
        return;
      }

      // Arrow keys for navigation
      if (['ArrowUp', 'ArrowDown', 'ArrowLeft', 'ArrowRight'].includes(e.key)) {
        // Don't handle arrows if any input/textarea is focused
        if (isInputElementFocused()) return;

        if (focusedPanel === 'nav') {
          e.preventDefault();
          if (e.key === 'ArrowUp') {
            fieldTreeRef.current?.navigateUp();
          } else if (e.key === 'ArrowDown') {
            fieldTreeRef.current?.navigateDown();
          }
        } else if (focusedPanel === 'table') {
          e.preventDefault();
          if (e.key === 'ArrowUp') {
            diyTableRef.current?.navigateUp();
          } else if (e.key === 'ArrowDown') {
            diyTableRef.current?.navigateDown();
          } else if (e.key === 'ArrowLeft') {
            diyTableRef.current?.navigateLeft();
          } else if (e.key === 'ArrowRight') {
            diyTableRef.current?.navigateRight();
          }
        }
        return;
      }

      // Space for toggle/select/copy
      if (e.key === ' ') {
        // Don't handle space if any input/textarea is focused
        if (isInputElementFocused()) return;

        if (focusedPanel === 'nav') {
          e.preventDefault();
          fieldTreeRef.current?.toggleFocused();
        } else if (focusedPanel === 'table') {
          e.preventDefault();
          diyTableRef.current?.copyFocusedCell();
        }
        return;
      }
    };

    window.addEventListener('keydown', handleKeydown);
    return () => window.removeEventListener('keydown', handleKeydown);
  }, [showCommandPalette, focusedPanel, isNavSearchFocused, isTableSearchFocused, allFavorites, handleApplyFavorite]);

  // Handle column reorder - update fields and clear active favorite
  const handleFieldsReorder = useCallback((newFields: string[][]) => {
    setSelectedFields(newFields);
    clearFavorite();
  }, [clearFavorite]);

  // Handle column removal from ResultTable header
  const handleFieldRemove = useCallback((field: string[]) => {
    const pathKey = field.join(PATH_DELIMITER);
    const currentPaths = fieldTreeRef.current?.getSelectedPaths();
    if (currentPaths) {
      const newPaths = new Set(currentPaths);
      newPaths.delete(pathKey);
      fieldTreeRef.current?.setSelectedPaths(newPaths);
    }
    clearFavorite();
  }, [clearFavorite]);

  // Clear preview field before export
  const handlePreviewClear = useCallback(() => {
    setFocusedFieldPath(null);
  }, []);

  const gvkLabel = selectedGVK
    ? `${selectedGVK.kind.toLowerCase()} (${selectedGVK.group ? `${selectedGVK.group}/${selectedGVK.version}` : selectedGVK.version})`
    : "";

  return (
    <div className="h-screen bg-background flex flex-col">
      {/* Context Bar - full width at top, collapse button aligned with panel border */}
      {!isSidebarCollapsed && (
        <ContextBar
          selectedContexts={selectedContexts}
          connectedContexts={connectedContexts}
          onBackToContexts={onBackToContexts}
          onCollapse={toggleSidebar}
          sidebarWidthPercent={sidebarWidthPercent}
        />
      )}

      <ResizablePanelGroup direction="horizontal" className="flex-1">
        {/* Left Sidebar Navigation */}
        <ResizablePanel
          ref={sidebarPanelRef}
          defaultSize={20}
          minSize={15}
          maxSize={40}
          collapsible
          collapsedSize={0}
          onCollapse={() => setIsSidebarCollapsed(true)}
          onExpand={() => setIsSidebarCollapsed(false)}
          onResize={(size) => setSidebarWidthPercent(size)}
        >
          <div
            className="flex flex-col h-full relative"
            onMouseDown={() => setFocusedPanel('nav')}
          >
            {/* Focus overlay - fades out after focus */}
            {focusedPanel === 'nav' && (
              <div className="absolute inset-0 animate-panel-focus pointer-events-none z-50" />
            )}
            {/* Nav Header - GVK row (aligns with DIYTableToolbar) */}
            <NavHeader
              selectedGVK={selectedGVK}
              selectedFieldCount={selectedFields.length}
              onClearAllFields={handleClearAllFields}
              onSearch={handleSearch}
            />

            {/* Quick Access Bar for Favorites - always visible */}
            <QuickAccessBar
              ref={quickAccessBarRef}
              favorites={allFavorites}
              activeFavoriteId={activeFavorite?.id ?? null}
              selectedGVK={selectedGVK}
              gvkLabel={gvkLabel}
              fieldCount={selectedFields.length}
              isFavoriteSaved={activeFavorite !== null}
              onApply={handleApplyFavorite}
              onRename={renameFavorite}
              onDelete={deleteFavorite}
              onSaveFavorite={handleSaveFavorite}
            />

            {/* Navigation Content */}
            <div className="flex-1 overflow-auto">
              {selectedGVK ? (
                <DynamicFieldTree
                  ref={fieldTreeRef}
                  selectedGVK={selectedGVK}
                  connectedContexts={connectedContexts}
                  onReady={handleNavigationReady}
                  onFieldsSelected={setSelectedFields}
                  onFieldFocus={setFocusedFieldPath}
                  highlightedFieldPath={focusedFieldPath ?? undefined}
                  skipDefaultPaths={hasPendingFavorite}
                />
              ) : (
                <div className="h-full flex items-center justify-center px-4">
                  <Button
                    variant="ghost"
                    className="text-sm flex items-center gap-1 rounded-lg px-4 py-2 animate-cta-pulse"
                    onClick={() => setShowCommandPalette(true)}
                  >
                    Press <Kbd>⌘</Kbd><Kbd>K</Kbd> to show schema fields
                  </Button>
                </div>
              )}
            </div>
          </div>
        </ResizablePanel>

        <ResizableHandle />

        {/* Right Panel - Main Content */}
        <ResizablePanel defaultSize={80}>
          <div
            className="h-full relative"
            onMouseDown={() => setFocusedPanel('table')}
          >
            {/* Focus overlay - fades out after focus */}
            {focusedPanel === 'table' && (
              <div className="absolute inset-0 animate-panel-focus pointer-events-none z-50" />
            )}

            {/* Table or Empty State */}
            {selectedGVK ? (
              <DIYTable
                ref={diyTableRef}
                selectedFields={selectedFields}
                selectedGVK={selectedGVK}
                connectedContexts={connectedContexts}
                isTableFocused={focusedPanel === 'table'}
                useWindowedMode={useWindowedMode}
                onFieldsReorder={handleFieldsReorder}
                onFieldRemove={handleFieldRemove}
                onColumnFocus={setFocusedFieldPath}
                highlightedColumnPath={focusedFieldPath ?? undefined}
                previewField={previewField}
                onPreviewClear={handlePreviewClear}
                expandButton={isSidebarCollapsed ? (
                  <Button
                    variant="ghost"
                    size="icon"
                    onClick={(e) => {
                      e.stopPropagation();
                      toggleSidebar();
                    }}
                    className="h-6 w-6 shrink-0"
                  >
                    <PanelLeft className="w-4 h-4" />
                  </Button>
                ) : undefined}
              />
            ) : (
              <div className="h-full flex items-center justify-center px-4">
                <Button
                  variant="ghost"
                  className="text-sm flex items-center gap-1 rounded-lg px-4 py-2 animate-cta-pulse"
                  onClick={() => setShowCommandPalette(true)}
                >
                  Press <Kbd>⌘</Kbd><Kbd>K</Kbd> to select a resource
                </Button>
              </div>
            )}
          </div>
        </ResizablePanel>
      </ResizablePanelGroup>

      {/* Keymap Bar - always visible */}
      <KeymapBar
        focusedPanel={focusedPanel}
        selectedFieldCount={selectedFields.length}
        isSearchFocused={isNavSearchFocused() || isTableSearchFocused()}
        hasTableData={selectedFields.length > 0}
      />

      {/* CommandPalette Modal */}
      {showCommandPalette && (
        <CommandPalette
          contexts={connectedContexts}
          gvks={gvks}
          favorites={allFavorites}
          loading={loading}
          theme={resolvedTheme}
          onClose={() => setShowCommandPalette(false)}
          onGVKSelect={(gvk) => {
            setSelectedGVK(gvk);
            setShowCommandPalette(false);
          }}
          onFavoriteSelect={(favorite) => {
            handleApplyFavorite(favorite);
            setShowCommandPalette(false);
          }}
          onThemeToggle={handleThemeToggle}
        />
      )}
    </div>
  );
}
