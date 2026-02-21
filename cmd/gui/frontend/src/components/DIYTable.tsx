import { useState, useMemo, useRef, forwardRef, useImperativeHandle, useCallback, useEffect } from 'react';
import {
  useReactTable,
  getCoreRowModel,
  getFilteredRowModel,
  getSortedRowModel,
  type ColumnDef,
  type SortingState,
} from '@tanstack/react-table';
import { useVirtualizer } from '@tanstack/react-virtual';
import { rankItem } from '@tanstack/match-sorter-utils';
import {
  DndContext,
  closestCenter,
  KeyboardSensor,
  PointerSensor,
  useSensor,
  useSensors,
  type DragEndEvent,
} from '@dnd-kit/core';
import {
  arrayMove,
  SortableContext,
  sortableKeyboardCoordinates,
  horizontalListSortingStrategy,
  useSortable,
} from '@dnd-kit/sortable';
import { CSS } from '@dnd-kit/utilities';
import { ChevronUp, ChevronDown, ChevronsUpDown, X } from 'lucide-react';
import { cn, pluralize } from '../lib/utils';
import { Spinner } from './ui/spinner';
import { HoverCard, HoverCardTrigger, HoverCardContent } from './ui/hover-card';
import { CellContent } from './CellContent';
import { DIYTableToolbar, DIYTableToolbarHandle } from './DIYTableToolbar';
import { useCellHighlight } from '../hooks/useCellHighlight';
import { useResourceData } from '../hooks/useResourceData';
import { useWindowedData, type SortConfig } from '../hooks/useWindowedData';
import { useFlashingCells } from '../hooks/useFlashingCells';
import type { main } from '../../wailsjs/go/models';

// Delay before showing cell popover (prevents popover flash during scroll)
const POPOVER_DELAY_MS = 300;

interface DIYTableProps {
  selectedFields: string[][];  // From DynamicFieldTree
  selectedGVK: main.MultiClusterGVK;
  connectedContexts: string[];
  isTableFocused?: boolean;  // Whether the table panel is focused (from MainView)
  onFieldsReorder?: (newFields: string[][]) => void;  // Callback when columns are reordered
  onFieldRemove?: (field: string[]) => void;  // Callback when a column is removed
  onColumnFocus?: (path: string[] | null) => void;  // Callback when column header is focused
  highlightedColumnPath?: string[];  // Column to highlight (from DynamicFieldTree hover)
  previewField?: string[];  // Unchecked field to preview as muted column at the end
  onPreviewClear?: () => void;  // Callback to clear preview before export
  expandButton?: React.ReactNode;  // Sidebar expand button (shown when collapsed)
  /** Enable windowed mode for large datasets (>1000 rows). Uses server-side sorting and lazy loading. */
  useWindowedMode?: boolean;
}

export interface DIYTableHandle {
  focusSearch: () => void;
  isSearchFocused: () => boolean;
  navigateUp: () => void;
  navigateDown: () => void;
  navigateLeft: () => void;
  navigateRight: () => void;
  copyFocusedCell: () => void;
  exportToClipboard: () => void;
  exportToFile: () => void;
}

// Helper function to get nested value from object using path
function getNestedValue(obj: any, path: string[]): any {
  let value = obj;
  for (const key of path) {
    value = value?.[key];
    if (value === undefined) break;
  }
  return value;
}

// Fuzzy filter without score-based sorting (allows column sorting to work)
const fuzzyFilter = (row: any, columnId: string, filterValue: string) => {
  const itemRank = rankItem(row.getValue(columnId), filterValue);
  // Don't call addMeta - this prevents score-based sorting
  return itemRank.passed;
};

// Custom PointerSensor that ignores resize handle
class ResizeAwarePointerSensor extends PointerSensor {
  static activators = [
    {
      eventName: 'onPointerDown' as const,
      handler: ({ nativeEvent }: { nativeEvent: PointerEvent }) => {
        const target = nativeEvent.target as HTMLElement;
        // Don't start drag if clicking on resize handle
        if (target.closest('[data-resize-handle]')) {
          return false;
        }
        return true;
      },
    },
  ];
}

// Sortable header component for drag-and-drop reordering
interface SortableHeaderProps {
  id: string;
  headerText: string;
  jsonPath: string;  // full JSON path for hover tooltip (original casing)
  sortDirection: false | 'asc' | 'desc';
  onSort: ((event: unknown) => void) | undefined;
  width: number;
  minWidth: number;
  onResize: ((e: React.MouseEvent | React.TouchEvent) => void) | undefined;
  isResizing: boolean;
  isDraggable: boolean;  // false for fixed columns (context, name)
  onRemove?: () => void;  // callback to remove this column
  onHover?: () => void;  // callback when header is hovered
  onHoverEnd?: () => void;  // callback when hover ends
  isHighlighted?: boolean;  // whether this column is highlighted (from NP hover)
  isPreview?: boolean;  // whether this is a preview column (muted styling)
}

function SortableHeader({
  id,
  headerText,
  jsonPath,
  sortDirection,
  onSort,
  width,
  minWidth,
  onResize,
  isResizing,
  isDraggable,
  onRemove,
  onHover,
  onHoverEnd,
  isHighlighted,
  isPreview,
}: SortableHeaderProps) {
  const [copied, setCopied] = useState(false);
  const {
    attributes,
    listeners,
    setNodeRef,
    transform,
    transition,
    isDragging,
  } = useSortable({ id, disabled: !isDraggable });

  const style = {
    transform: CSS.Transform.toString(transform),
    transition,
    width: `${width}px`,
    minWidth: `${minWidth}px`,
    opacity: isDragging ? 0.5 : 1,
    zIndex: isDragging ? 20 : undefined,
  };

  // Draggable columns: entire header is draggable (click = sort, drag 5px+ = reorder)
  const dragProps = isDraggable ? { ...attributes, ...listeners } : {};

  // Copy JSON path to clipboard
  const handleCopyPath = async (e: React.MouseEvent) => {
    e.stopPropagation();
    try {
      await navigator.clipboard.writeText(jsonPath);
      setCopied(true);
      setTimeout(() => setCopied(false), 1000);
    } catch (error) {
      console.error('Failed to copy:', error);
    }
  };

  return (
    <div
      ref={setNodeRef}
      style={style}
      className={cn(
        "relative px-2 h-full text-left text-sm font-semibold flex-shrink-0 text-muted-foreground group flex items-center",
        isDragging && "bg-accent",
        isDraggable && "cursor-grab active:cursor-grabbing",
        isHighlighted && "bg-focus",
        isPreview && "opacity-50 border-l border-dashed border-border"
      )}
      onMouseEnter={onHover}
      onMouseLeave={onHoverEnd}
      {...dragProps}
    >
      {/* Sort button with HoverCard for JSON path tooltip */}
      <HoverCard openDelay={300} closeDelay={200}>
        <HoverCardTrigger asChild>
          <div
            className="flex items-center gap-1 select-none hover:text-foreground transition-colors"
            onClick={(e) => onSort?.(e)}
          >
            <span className="truncate capitalize">{headerText}</span>
            {/* Hide sort icons for preview columns */}
            {!isPreview && (
              <span className="flex-shrink-0">
                {sortDirection === 'asc' ? (
                  <ChevronUp className="h-4 w-4" />
                ) : sortDirection === 'desc' ? (
                  <ChevronDown className="h-4 w-4" />
                ) : (
                  <ChevronsUpDown className="h-4 w-4 opacity-0 group-hover:opacity-50 transition-opacity" />
                )}
              </span>
            )}
          </div>
        </HoverCardTrigger>
        <HoverCardContent
          side="top"
          align="start"
          className="w-auto p-2 text-xs"
        >
          <div className="flex items-center gap-2">
            <code
              className="px-1.5 py-0.5 rounded bg-muted font-mono cursor-pointer hover:bg-accent transition-colors"
              onClick={handleCopyPath}
            >
              {jsonPath}
            </code>
            {copied && <span className="text-primary font-medium">Copied</span>}
          </div>
        </HoverCardContent>
      </HoverCard>
      {/* Remove column button - only for draggable (non-fixed) columns */}
      {isDraggable && onRemove && (
        <button
          onClick={(e) => {
            e.stopPropagation();
            onRemove();
          }}
          className="absolute right-2 top-1/2 -translate-y-1/2 p-0.5 rounded opacity-0 group-hover:opacity-100 hover:bg-destructive/20 hover:text-destructive transition-opacity"
          title="Remove column"
        >
          <X className="h-3 w-3" />
        </button>
      )}
      {/* Column resize handle */}
      <div
        data-resize-handle
        onMouseDown={(e) => onResize?.(e)}
        onTouchStart={(e) => onResize?.(e)}
        className={cn(
          "absolute right-0 top-0 h-full w-0.5 cursor-col-resize select-none touch-none",
          "hover:bg-primary/50",
          isResizing && "bg-primary"
        )}
      />
    </div>
  );
}

export const DIYTable = forwardRef<DIYTableHandle, DIYTableProps>(({
  selectedFields,
  selectedGVK,
  connectedContexts,
  isTableFocused = true,
  onFieldsReorder,
  onFieldRemove,
  onColumnFocus,
  highlightedColumnPath,
  previewField,
  onPreviewClear,
  expandButton,
  useWindowedMode = false,
}, ref) => {
  // Convert selectedFields from string[][] to string[] (dot notation) for the hook
  const selectedFieldPaths = useMemo(() => {
    return selectedFields.map(field => field.join('.'));
  }, [selectedFields]);

  const [globalFilter, setGlobalFilter] = useState('');
  const [sorting, setSorting] = useState<SortingState>([]);

  // Convert TanStack sorting state to server-side sort config
  const serverSort: SortConfig | undefined = useMemo(() => {
    if (sorting.length === 0) return undefined;
    const sort = sorting[0];
    if (!sort) return undefined;
    return {
      field: sort.id,
      descending: sort.desc,
    };
  }, [sorting]);

  // Convert previewField from string[] to dot notation for the hook
  const previewFieldPath = previewField ? previewField.join('.') : undefined;

  // Use windowed data for large datasets (lazy loading)
  const windowedResult = useWindowedData(
    useWindowedMode ? selectedGVK : null,
    useWindowedMode ? connectedContexts : [],
    {
      selectedFields: selectedFieldPaths,
      sort: serverSort,
      overscan: 30,
      search: globalFilter,  // Server-side search
      previewField: previewFieldPath,  // Hover preview field (debounced 200ms in hook)
    }
  );

  // Use standard data fetching for small datasets
  const standardResult = useResourceData(
    !useWindowedMode ? selectedGVK : null,
    !useWindowedMode ? connectedContexts : [],
    { watch: true, selectedFields: selectedFieldPaths, previewField: previewFieldPath }
  );

  // Unified interface
  const loading = useWindowedMode ? windowedResult.loading : standardResult.loading;
  const filterPending = useWindowedMode ? windowedResult.filterPending : false;
  const watchStatus = useWindowedMode ? windowedResult.watchStatus : standardResult.watchStatus;
  const initialSyncComplete = useWindowedMode ? windowedResult.initialSyncComplete : true;  // Standard mode always synced
  const totalCount = useWindowedMode ? windowedResult.totalCount : standardResult.data.length;
  const changedCells = useWindowedMode ? windowedResult.changedCells : standardResult.changedCells;
  const loadingFields = useWindowedMode ? new Set<string>() : standardResult.loadingFields;  // Cell-level skeleton for loading fields
  const extractedFields = useWindowedMode ? new Set<string>() : standardResult.extractedFields;  // Fields extracted from backend

  // Windowed mode skeleton: controlled by data itself, not by extractingFields state.
  // Backend stores explicit null for extracted fields with no value (JSON null → JS null → "-").
  // Missing keys in JSON → JS undefined → skeleton (field not yet extracted).

  // For windowed mode, data array contains ONLY loaded rows (~80) instead of totalCount placeholder objects.
  // This reduces TanStack Table row model from 6616 to ~80 items (98.8% reduction).
  // Skeleton rows are rendered directly in the virtualizer loop, bypassing TanStack.
  const data = useMemo(() => {
    if (!useWindowedMode) {
      return standardResult.data;
    }
    // Convert Map values to array (only loaded rows)
    return Array.from(windowedResult.visibleRows.values());
  }, [useWindowedMode, standardResult.data, windowedResult.visibleRows]);

  // Maps virtual index → data array index for loaded rows.
  // Unloaded virtual rows won't be in the map → render skeleton directly.
  const virtualToRowIndex = useMemo(() => {
    if (!useWindowedMode) return null;
    const map = new Map<number, number>();
    let dataIdx = 0;
    for (const virtualIdx of windowedResult.visibleRows.keys()) {
      map.set(virtualIdx, dataIdx);
      dataIdx++;
    }
    return map;
  }, [useWindowedMode, windowedResult.visibleRows]);

  // Row ID function
  const getRowId = useCallback((row: Record<string, unknown>, index: number) => {
    if (useWindowedMode) {
      // Windowed mode: data only contains loaded rows, use _key for stable identity
      const key = row._key as string | undefined;
      if (key) return key;
      return `_row_${index}`;
    }
    return standardResult.getRowId(row);
  }, [useWindowedMode, standardResult.getRowId]);

  // Track flashing cells for real-time update visualization
  const { isFlashing } = useFlashingCells(changedCells);
  const tableContainerRef = useRef<HTMLDivElement>(null);
  const toolbarRef = useRef<DIYTableToolbarHandle>(null);

  // DnD sensors for column reordering
  const sensors = useSensors(
    useSensor(ResizeAwarePointerSensor, {
      activationConstraint: { distance: 5 },  // 5px movement before drag starts
    }),
    useSensor(KeyboardSensor, {
      coordinateGetter: sortableKeyboardCoordinates,
    })
  );

  // Calculate fixed column count (context + name or just name)
  const fixedColumnCount = connectedContexts.length === 1 ? 1 : 2;

  // Handle drag end for column reordering
  const handleDragEnd = useCallback((event: DragEndEvent) => {
    const { active, over } = event;
    if (!over || active.id === over.id || !onFieldsReorder) return;

    // Find indices in selectedFields (not including fixed columns)
    const oldIndex = selectedFields.findIndex(f => f.join('.') === active.id);
    const newIndex = selectedFields.findIndex(f => f.join('.') === over.id);

    if (oldIndex !== -1 && newIndex !== -1) {
      const newFields = arrayMove(selectedFields, oldIndex, newIndex);
      onFieldsReorder(newFields);
    }
  }, [selectedFields, onFieldsReorder]);

  // Unified focus state (keyboard navigation + mouse hover)
  const [focusedRowIndex, setFocusedRowIndex] = useState<number | null>(null);
  const [focusedColIndex, setFocusedColIndex] = useState<number | null>(null);
  // Debounced popover state (shows popover after delay)
  const [popoverCell, setPopoverCell] = useState<{ row: number; col: number } | null>(null);
  const popoverTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  // Track copied cell for "Copied" feedback
  const [copiedCellKey, setCopiedCellKey] = useState<string | null>(null);

  // Debounce popover display
  useEffect(() => {
    // Clear any existing timeout
    if (popoverTimeoutRef.current) {
      clearTimeout(popoverTimeoutRef.current);
      popoverTimeoutRef.current = null;
    }

    // Always clear popover immediately when focus changes
    setPopoverCell(null);

    if (focusedRowIndex !== null && focusedColIndex !== null) {
      // Set timeout to show popover after delay
      popoverTimeoutRef.current = setTimeout(() => {
        setPopoverCell({ row: focusedRowIndex, col: focusedColIndex });
      }, POPOVER_DELAY_MS);
    }

    return () => {
      if (popoverTimeoutRef.current) {
        clearTimeout(popoverTimeoutRef.current);
      }
    };
  }, [focusedRowIndex, focusedColIndex]);

  // Get highlight function based on current search query
  const getHighlightIndices = useCellHighlight(globalFilter);

  // Reset focus when data changes
  useEffect(() => {
    setFocusedRowIndex(null);
    setFocusedColIndex(null);
  }, [selectedGVK]);

  // Clear cell focus when search input is focused
  const handleSearchFocusChange = useCallback((focused: boolean) => {
    if (focused) {
      setFocusedRowIndex(null);
      setFocusedColIndex(null);
    }
  }, []);

  // Clear cell focus when table panel loses focus (e.g., Tab to nav panel)
  useEffect(() => {
    if (!isTableFocused) {
      setFocusedRowIndex(null);
      setFocusedColIndex(null);
    }
  }, [isTableFocused]);

  // Calculate column width based on field name and data
  // Returns { size: initial display width, maxSize: max possible width }
  const calculateColumnWidth = (fieldName: string, values: any[]): { size: number; maxSize: number } => {
    // Header is displayed in title case (CSS capitalize), estimate 8px per char
    // Add 40px for padding (px-4 = 32px) + resize handle (8px)
    const headerWidth = fieldName.length * 8 + 40;

    // Sample first 100 values to estimate max width
    const sampleSize = Math.min(100, values.length);
    const samples = values.slice(0, sampleSize);

    let maxValueWidth = 0;
    for (const value of samples) {
      const text = typeof value === 'object' && value !== null
        ? JSON.stringify(value)
        : String(value ?? '');
      const width = text.length * 8 + 32;
      maxValueWidth = Math.max(maxValueWidth, width);
    }

    // size: initial display width based on actual content
    const size = Math.max(headerWidth, maxValueWidth, 80);
    // maxSize: at least 400px so users can expand columns beyond content width
    const maxSize = Math.max(size, 400);

    return { size, maxSize };
  };

  // Memoize column widths separately (only recalculate when fields or data change)
  const columnWidths = useMemo(() => {
    // Always add default columns at the beginning
    let fieldsToUse: string[][];

    if (connectedContexts.length === 1) {
      // Single context: name + selectedFields
      fieldsToUse = [['metadata', 'name'], ...selectedFields];
    } else {
      // Multiple contexts: context, name + selectedFields
      fieldsToUse = [['_context'], ['metadata', 'name'], ...selectedFields];
    }

    const widths: Record<string, { size: number; maxSize: number }> = {};
    fieldsToUse.forEach((fieldPath) => {
      const fieldName = fieldPath[fieldPath.length - 1];
      const columnId = fieldPath.join('.');
      // Use last field name for header width (except _context which displays as 'context')
      const headerText = fieldName === '_context' ? 'context' : fieldName;
      const columnValues = data.map((row) => getNestedValue(row, fieldPath));
      widths[columnId] = calculateColumnWidth(headerText, columnValues);
    });

    return widths;
  }, [selectedFields, connectedContexts, data]);

  // Create dynamic columns from selectedFields with default columns prepended
  // Also includes preview column at the end if previewField is set
  const columns = useMemo<ColumnDef<Record<string, unknown>>[]>(() => {
    // Always add default columns at the beginning
    let fieldsToUse: string[][];

    if (connectedContexts.length === 1) {
      // Single context: name + selectedFields
      fieldsToUse = [['metadata', 'name'], ...selectedFields];
    } else {
      // Multiple contexts: context, name + selectedFields
      fieldsToUse = [['_context'], ['metadata', 'name'], ...selectedFields];
    }

    const cols: ColumnDef<Record<string, unknown>>[] = fieldsToUse.map((fieldPath) => {
      const fieldName = fieldPath[fieldPath.length - 1];
      const columnId = fieldPath.join('.');
      const widths = columnWidths[columnId] || { size: 100, maxSize: 300 };

      return {
        id: columnId,
        header: fieldName === '_context' ? 'context' : fieldName,  // Show last field name (capitalize in UI)
        accessorFn: (row) => getNestedValue(row, fieldPath),
        size: widths.size,  // Use pre-calculated initial width
        minSize: 80,  // Minimum column width
        maxSize: widths.maxSize,  // Dynamic max based on header/values
        cell: (info) => {
          const value = info.getValue();

          // For objects/arrays, get full text for highlighting
          const fullText = typeof value === 'object' && value !== null
            ? JSON.stringify(value)
            : String(value ?? '');

          // Get highlight indices based on full text
          const indices = getHighlightIndices(fullText);

          // Render with CellContent component (handles truncation + highlighting)
          return (
            <CellContent
              value={value}
              highlightIndices={indices}
            />
          );
        },
      };
    });

    // Add preview column at the end if previewField is set
    if (previewField) {
      const previewFieldName = previewField[previewField.length - 1];
      const previewColumnId = `_preview.${previewField.join('.')}`;
      const previewWidths = columnWidths[previewField.join('.')] || { size: 150, maxSize: 300 };

      cols.push({
        id: previewColumnId,
        header: previewFieldName,
        accessorFn: (row) => getNestedValue(row, previewField),
        size: previewWidths.size,
        minSize: 80,
        maxSize: previewWidths.maxSize,
        meta: { isPreview: true },  // Mark as preview column
        cell: (info) => {
          const value = info.getValue();
          const fullText = typeof value === 'object' && value !== null
            ? JSON.stringify(value)
            : String(value ?? '');
          const indices = getHighlightIndices(fullText);

          return (
            <CellContent
              value={value}
              highlightIndices={indices}
            />
          );
        },
      });
    }

    return cols;
  }, [selectedFields, connectedContexts, columnWidths, getHighlightIndices, previewField]);

  // Create table instance
  // In windowed mode: disable client-side sorting/filtering (handled by server)
  const table = useReactTable({
    data,  // Windowed: ~80 loaded rows; Standard: all rows
    columns,
    getRowId,  // Stable row identity for real-time updates
    columnResizeMode: 'onChange',  // Enable column resizing
    globalFilterFn: useWindowedMode ? undefined : fuzzyFilter,
    enableSortingRemoval: false,  // Toggle between asc/desc only (no "none" state)
    manualSorting: useWindowedMode,  // Server-side sorting in windowed mode
    manualFiltering: useWindowedMode,  // Server-side filtering in windowed mode
    manualPagination: useWindowedMode,  // Server-side pagination in windowed mode
    rowCount: useWindowedMode ? totalCount : undefined,  // Total rows for scrollbar sizing
    state: {
      globalFilter,  // Keep filter state for UI, server handles filtering in windowed mode
      sorting,
    },
    onGlobalFilterChange: setGlobalFilter,  // Allow filter changes in both modes
    onSortingChange: setSorting,
    getCoreRowModel: getCoreRowModel(),
    getFilteredRowModel: useWindowedMode ? undefined : getFilteredRowModel(),
    getSortedRowModel: useWindowedMode ? undefined : getSortedRowModel(),
  });

  // Get filtered rows for virtualization
  const { rows } = table.getRowModel();

  // Navigation callbacks (must be defined after rows and columns)
  const navigateUp = useCallback(() => {
    setFocusedRowIndex((prev) => {
      if (prev === null) return rows.length > 0 ? 0 : null;
      return Math.max(0, prev - 1);
    });
    if (focusedColIndex === null && columns.length > 0) {
      setFocusedColIndex(0);
    }
  }, [rows.length, focusedColIndex, columns.length]);

  const navigateDown = useCallback(() => {
    setFocusedRowIndex((prev) => {
      if (prev === null) return rows.length > 0 ? 0 : null;
      return Math.min(rows.length - 1, prev + 1);
    });
    if (focusedColIndex === null && columns.length > 0) {
      setFocusedColIndex(0);
    }
  }, [rows.length, focusedColIndex, columns.length]);

  const navigateLeft = useCallback(() => {
    setFocusedColIndex((prev) => {
      if (prev === null) return columns.length > 0 ? 0 : null;
      return Math.max(0, prev - 1);
    });
    if (focusedRowIndex === null && rows.length > 0) {
      setFocusedRowIndex(0);
    }
  }, [columns.length, focusedRowIndex, rows.length]);

  const navigateRight = useCallback(() => {
    setFocusedColIndex((prev) => {
      if (prev === null) return columns.length > 0 ? 0 : null;
      return Math.min(columns.length - 1, prev + 1);
    });
    if (focusedRowIndex === null && rows.length > 0) {
      setFocusedRowIndex(0);
    }
  }, [columns.length, focusedRowIndex, rows.length]);

  const copyFocusedCell = useCallback(() => {
    if (focusedRowIndex === null || focusedColIndex === null) return;
    const row = rows[focusedRowIndex];
    if (!row) return;
    const cell = row.getVisibleCells()[focusedColIndex];
    if (!cell) return;

    const value = cell.getValue();
    const text = typeof value === 'object' && value !== null
      ? JSON.stringify(value)
      : String(value ?? '');

    navigator.clipboard.writeText(text).then(() => {
      // Set copied cell key to show "Copied" feedback
      const cellKey = `${row.id}-${cell.column.id}`;
      setCopiedCellKey(cellKey);
      // Clear after 1 second
      setTimeout(() => setCopiedCellKey(null), 1000);
    }).catch(console.error);
  }, [focusedRowIndex, focusedColIndex, rows]);

  // Expose handle methods
  useImperativeHandle(ref, () => ({
    focusSearch: () => {
      toolbarRef.current?.focusSearch();
    },
    isSearchFocused: () => {
      return toolbarRef.current?.isSearchFocused() ?? false;
    },
    navigateUp,
    navigateDown,
    navigateLeft,
    navigateRight,
    copyFocusedCell,
    exportToClipboard: () => {
      toolbarRef.current?.exportToClipboard();
    },
    exportToFile: () => {
      toolbarRef.current?.exportToFile();
    },
  }), [navigateUp, navigateDown, navigateLeft, navigateRight, copyFocusedCell]);

  // Get total width from table state (updates on resize)
  const totalColumnsWidth = table.getTotalSize();

  // Track previous visible range to prevent infinite loops
  const prevVisibleRangeRef = useRef({ start: -1, end: -1 });

  // Setup row virtualizer
  // In windowed mode, use totalCount for proper scrollbar sizing
  // and notify about visible range changes for lazy loading
  const rowVirtualizer = useVirtualizer({
    count: useWindowedMode ? totalCount : rows.length,
    getScrollElement: () => tableContainerRef.current,
    estimateSize: () => 28,  // Estimated row height in pixels (reduced from 35)
    overscan: useWindowedMode ? 30 : 10,  // More overscan in windowed mode for smoother scrolling
    onChange: useWindowedMode ? (instance) => {
      const range = instance.range;
      if (range) {
        // Only notify if range actually changed (prevents infinite loops)
        const prev = prevVisibleRangeRef.current;
        if (prev.start !== range.startIndex || prev.end !== range.endIndex) {
          prevVisibleRangeRef.current = { start: range.startIndex, end: range.endIndex };
          windowedResult.onVisibleRangeChange(range.startIndex, range.endIndex);
        }
      }
    } : undefined,
  });

  // Auto-scroll to keep focused row visible
  useEffect(() => {
    if (focusedRowIndex !== null) {
      rowVirtualizer.scrollToIndex(focusedRowIndex, { align: 'auto' });
    }
  }, [focusedRowIndex, rowVirtualizer]);

  // Prepare export data (headers and rows for CSV)
  // Excludes preview columns - only exports selected fields
  const exportData = useMemo(() => {
    const allHeaders = table.getHeaderGroups()[0]?.headers || [];
    // Filter out preview columns (id starts with _preview.)
    const exportHeaders = allHeaders.filter(h => !h.column.id.startsWith('_preview.'));

    const headers = exportHeaders.map((header) => {
      return typeof header.column.columnDef.header === 'string'
        ? header.column.columnDef.header
        : String(header.column.columnDef.header);
    });

    const exportRows = rows.map((row) => {
      const allCells = row.getVisibleCells();
      // Filter out preview columns
      const exportCells = allCells.filter(cell => !cell.column.id.startsWith('_preview.'));
      return exportCells.map((cell) => {
        const value = cell.getValue();
        // Handle objects/arrays - convert to JSON string for CSV
        if (typeof value === 'object' && value !== null) {
          return JSON.stringify(value);
        }
        return value;
      });
    });

    return { headers, rows: exportRows };
  }, [table, rows, selectedFields, connectedContexts]);

  return (
    <div className="flex flex-col h-full">
      {/* Toolbar with Search and Export */}
      <DIYTableToolbar
        ref={toolbarRef}
        globalFilter={globalFilter}
        onGlobalFilterChange={setGlobalFilter}
        filteredRowCount={useWindowedMode ? totalCount : rows.length}
        totalRowCount={useWindowedMode ? totalCount : data.length}
        isLoading={loading || filterPending || (useWindowedMode && !initialSyncComplete)}
        headers={exportData.headers}
        rows={exportData.rows}
        resourceKind={selectedGVK?.kind || 'resources'}
        onSearchFocusChange={handleSearchFocusChange}
        onBeforeExport={onPreviewClear}
        expandButton={expandButton}
      />

      {/* Table Content with Virtual Scrolling */}
      <div
        ref={tableContainerRef}
        className="flex-1 overflow-auto"
        onMouseLeave={() => {
          // Clear cell focus when mouse leaves the table area
          setFocusedRowIndex(null);
          setFocusedColIndex(null);
        }}
      >
        {loading && (!useWindowedMode || totalCount === 0) ? (
          <div className="h-full flex flex-col items-center justify-center gap-2">
            <Spinner className="w-8 h-8" />
            <p className="text-sm text-muted-foreground">
              Loading {pluralize(selectedGVK?.kind?.toLowerCase() ?? 'resource')}...
            </p>
          </div>
        ) : (useWindowedMode ? totalCount : data.length) === 0 && watchStatus === 'connected' && initialSyncComplete ? (
          // Only show "No resources found" when fully connected AND initial sync is complete
          // This prevents brief flash during initial load and reconnection
          <div className="h-full flex items-center justify-center">
            <p className="text-sm text-muted-foreground">No resources found</p>
          </div>
        ) : (useWindowedMode ? totalCount : data.length) === 0 ? (
          // Still connecting, reconnecting, or waiting for initial sync - show loading state
          <div className="h-full flex flex-col items-center justify-center gap-2">
            <Spinner className="w-8 h-8" />
            <p className="text-sm text-muted-foreground">
              Loading {pluralize(selectedGVK?.kind?.toLowerCase() ?? 'resource')}...
            </p>
          </div>
        ) : (useWindowedMode ? totalCount : rows.length) === 0 ? (
          <div className="h-full flex items-center justify-center">
            <p className="text-sm text-muted-foreground">
              No matches for "{globalFilter}"
            </p>
          </div>
        ) : (
          <div style={{ minWidth: `${totalColumnsWidth}px` }}>
            {/* Header (sticky) with DnD for column reordering */}
            <DndContext
              sensors={sensors}
              collisionDetection={closestCenter}
              onDragEnd={handleDragEnd}
            >
              <div className="sticky top-0 bg-background z-10 border-b border-border h-8">
                {table.getHeaderGroups().map((headerGroup) => (
                  <SortableContext
                    key={headerGroup.id}
                    items={selectedFields.map(f => f.join('.'))}
                    strategy={horizontalListSortingStrategy}
                  >
                    <div className="flex h-full">
                      {headerGroup.headers.map((header, headerIndex) => {
                        const headerText = typeof header.column.columnDef.header === 'string'
                          ? header.column.columnDef.header
                          : String(header.column.columnDef.header);
                        const sortDirection = header.column.getIsSorted();

                        // Check if this is a preview column (id starts with _preview.)
                        const isPreviewColumn = header.column.id.startsWith('_preview.');

                        // Draggable: not fixed columns and not preview column
                        const isDraggable = headerIndex >= fixedColumnCount && !isPreviewColumn;

                        // Get the field for this column (for removal and hover sync)
                        const fieldIndex = headerIndex - fixedColumnCount;
                        const field = isDraggable ? selectedFields[fieldIndex] : undefined;

                        // Get field path from column ID (for default columns too)
                        const columnFieldPath = header.column.id.split('.');

                        // Check if this column is highlighted from DynamicFieldTree hover
                        const isHighlighted = highlightedColumnPath && !isPreviewColumn
                          ? header.column.id === highlightedColumnPath.join('.')
                          : false;

                        // Get full JSON path for hover tooltip (strip _preview. prefix if present)
                        const jsonPath = isPreviewColumn
                          ? header.column.id.replace('_preview.', '')
                          : header.column.id;

                        return (
                          <SortableHeader
                            key={header.id}
                            id={header.column.id}
                            headerText={headerText}
                            jsonPath={jsonPath}
                            sortDirection={isPreviewColumn ? false : sortDirection}
                            onSort={isPreviewColumn ? undefined : header.column.getToggleSortingHandler()}
                            width={header.getSize()}
                            minWidth={header.column.columnDef.minSize || 80}
                            onResize={isPreviewColumn ? undefined : header.getResizeHandler()}
                            isResizing={header.column.getIsResizing()}
                            isDraggable={isDraggable}
                            onRemove={field && onFieldRemove ? () => onFieldRemove(field) : undefined}
                            onHover={() => {
                              // Clear body cell focus when hovering header
                              setFocusedRowIndex(null);
                              setFocusedColIndex(null);
                              // Sync with NavigationPanel
                              if (onColumnFocus && !isPreviewColumn) {
                                onColumnFocus(columnFieldPath);
                              }
                            }}
                            onHoverEnd={onColumnFocus && !isPreviewColumn ? () => onColumnFocus(null) : undefined}
                            isHighlighted={isHighlighted}
                            isPreview={isPreviewColumn}
                          />
                        );
                      })}
                    </div>
                  </SortableContext>
                ))}
              </div>
            </DndContext>

            {/* Body (virtualized) */}
            <div
              style={{
                height: `${rowVirtualizer.getTotalSize()}px`,
                position: 'relative',
              }}
            >
              {rowVirtualizer.getVirtualItems().map((virtualRow) => {
                const isRowFocused = focusedRowIndex === virtualRow.index;

                // Windowed mode: use virtualToRowIndex to find loaded row
                if (useWindowedMode) {
                  const dataIndex = virtualToRowIndex?.get(virtualRow.index);

                  // Unloaded row → render skeleton directly (bypasses TanStack Table)
                  if (dataIndex == null) {
                    return (
                      <div
                        key={`placeholder-${virtualRow.index}`}
                        className="flex border-b border-border absolute top-0 left-0"
                        style={{
                          height: `${virtualRow.size}px`,
                          transform: `translateY(${virtualRow.start}px)`,
                          width: `max(${totalColumnsWidth}px, 100%)`,
                        }}
                      >
                        {columns.map((col, colIndex) => (
                          <div
                            key={`skeleton-${virtualRow.index}-${colIndex}`}
                            className="px-1 py-1 flex-shrink-0 flex items-center"
                            style={{
                              width: `${col.size || 100}px`,
                              minWidth: `${col.minSize || 80}px`,
                            }}
                          >
                            <div className="h-4 bg-muted/50 rounded animate-pulse w-3/4" />
                          </div>
                        ))}
                      </div>
                    );
                  }

                  // Loaded row → render via TanStack row model
                  const row = rows[dataIndex];
                  if (!row) {
                    // Edge case: data array updated but TanStack row model not yet refreshed
                    return null;
                  }

                  return (
                    <div
                      key={row.id}
                      className={cn(
                        "flex border-b border-border hover:bg-focus transition-colors absolute top-0 left-0",
                        isRowFocused && "bg-focus"
                      )}
                      style={{
                        height: `${virtualRow.size}px`,
                        transform: `translateY(${virtualRow.start}px)`,
                        width: `max(${totalColumnsWidth}px, 100%)`,
                      }}
                    >
                      {row.getVisibleCells().map((cell, cellIndex) => {
                        const isCellFocused = isRowFocused && focusedColIndex === cellIndex;
                        const showCellPopover = popoverCell?.row === virtualRow.index && popoverCell?.col === cellIndex;
                        const cellKey = `${row.id}-${cell.column.id}`;
                        const showCopied = copiedCellKey === cellKey;
                        const isPreviewCell = cell.column.id.startsWith('_preview.');
                        const isColumnHighlighted = highlightedColumnPath && cell.column.id === highlightedColumnPath.join('.');
                        const value = cell.getValue();
                        const fullText = typeof value === 'object' && value !== null
                          ? JSON.stringify(value)
                          : String(value ?? '');
                        const highlightIndices = getHighlightIndices(fullText);

                        return (
                          <div
                            key={cell.id}
                            className={cn(
                              "px-1 py-1 text-sm flex-shrink-0",
                              isFlashing(row.id, cell.column.id) && "animate-cell-flash",
                              isPreviewCell && "opacity-50 border-l border-dashed border-border",
                              isColumnHighlighted && "bg-focus"
                            )}
                            style={{
                              width: `${cell.column.getSize()}px`,
                              minWidth: `${cell.column.columnDef.minSize || 80}px`,
                            }}
                            onMouseEnter={() => {
                              setFocusedRowIndex(virtualRow.index);
                              setFocusedColIndex(cellIndex);
                            }}
                          >
                            {value === undefined ? (
                              <div className="h-4 bg-muted/50 rounded animate-pulse w-3/4" />
                            ) : (
                              <CellContent
                                value={value}
                                highlightIndices={highlightIndices}
                                isFocused={isCellFocused}
                                showPopover={showCellPopover}
                                showCopied={showCopied}
                              />
                            )}
                          </div>
                        );
                      })}
                    </div>
                  );
                }

                // Non-windowed mode: standard rendering
                const row = rows[virtualRow.index];
                if (!row) return null;

                return (
                  <div
                    key={row.id}
                    className={cn(
                      "flex border-b border-border hover:bg-focus transition-colors absolute top-0 left-0",
                      isRowFocused && "bg-focus"
                    )}
                    style={{
                      height: `${virtualRow.size}px`,
                      transform: `translateY(${virtualRow.start}px)`,
                      width: `max(${totalColumnsWidth}px, 100%)`,
                    }}
                  >
                    {row.getVisibleCells().map((cell, cellIndex) => {
                      const isCellFocused = isRowFocused && focusedColIndex === cellIndex;
                      const showCellPopover = popoverCell?.row === virtualRow.index && popoverCell?.col === cellIndex;
                      const cellKey = `${row.id}-${cell.column.id}`;
                      const showCopied = copiedCellKey === cellKey;
                      const isPreviewCell = cell.column.id.startsWith('_preview.');
                      const isColumnHighlighted = highlightedColumnPath && cell.column.id === highlightedColumnPath.join('.');

                      const value = cell.getValue();

                      const fieldPath = isPreviewCell
                        ? cell.column.id.replace('_preview.', '')
                        : cell.column.id;

                      const essentialFieldPrefixes = [
                        'metadata.name', 'metadata.namespace', 'metadata.uid',
                        'metadata.resourceVersion', 'metadata.creationTimestamp',
                        'metadata.labels', 'metadata.ownerReferences',
                        'metadata.deletionTimestamp', 'metadata.finalizers',
                        '_context',
                      ];
                      const isEssentialField = essentialFieldPrefixes.some(
                        prefix => fieldPath === prefix || fieldPath.startsWith(prefix + '.')
                      );
                      const isFieldExtracted = isEssentialField || extractedFields.has(fieldPath);
                      const showSkeleton = (loadingFields.has(fieldPath) && value === undefined) ||
                        (!isFieldExtracted && value === undefined);
                      const fullText = typeof value === 'object' && value !== null
                        ? JSON.stringify(value)
                        : String(value ?? '');
                      const highlightIndices = getHighlightIndices(fullText);

                      return (
                        <div
                          key={cell.id}
                          className={cn(
                            "px-1 py-1 text-sm flex-shrink-0",
                            isFlashing(row.id, cell.column.id) && "animate-cell-flash",
                            isPreviewCell && "opacity-50 border-l border-dashed border-border",
                            isColumnHighlighted && "bg-focus"
                          )}
                          style={{
                            width: `${cell.column.getSize()}px`,
                            minWidth: `${cell.column.columnDef.minSize || 80}px`,
                          }}
                          onMouseEnter={() => {
                            setFocusedRowIndex(virtualRow.index);
                            setFocusedColIndex(cellIndex);
                          }}
                        >
                          {showSkeleton ? (
                            <div className="h-4 bg-muted/50 rounded animate-pulse w-3/4" />
                          ) : (
                            <CellContent
                              value={value}
                              highlightIndices={highlightIndices}
                              isFocused={isCellFocused}
                              showPopover={showCellPopover}
                              showCopied={showCopied}
                            />
                          )}
                        </div>
                      );
                    })}
                  </div>
                );
              })}
            </div>
          </div>
        )}
      </div>
    </div>
  );
});

DIYTable.displayName = 'DIYTable';
