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
}, ref) => {
  // Convert selectedFields from string[][] to string[] (dot notation) for the hook
  const selectedFieldPaths = useMemo(() => {
    return selectedFields.map(field => field.join('.'));
  }, [selectedFields]);

  // Use extracted hook for data fetching (watch enabled for real-time updates)
  // Pass selectedFields so the backend extracts the correct field values
  const { data, loading, getRowId, changedCells } = useResourceData(
    selectedGVK,
    connectedContexts,
    { watch: true, selectedFields: selectedFieldPaths }
  );

  // Track flashing cells for real-time update visualization
  const { isFlashing } = useFlashingCells(changedCells);

  const [globalFilter, setGlobalFilter] = useState('');
  const [sorting, setSorting] = useState<SortingState>([]);
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
  const columns = useMemo<ColumnDef<any>[]>(() => {
    // Always add default columns at the beginning
    let fieldsToUse: string[][];

    if (connectedContexts.length === 1) {
      // Single context: name + selectedFields
      fieldsToUse = [['metadata', 'name'], ...selectedFields];
    } else {
      // Multiple contexts: context, name + selectedFields
      fieldsToUse = [['_context'], ['metadata', 'name'], ...selectedFields];
    }

    const cols: ColumnDef<any>[] = fieldsToUse.map((fieldPath) => {
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
  const table = useReactTable({
    data,
    columns,
    getRowId,  // Stable row identity for real-time updates
    columnResizeMode: 'onChange',  // Enable column resizing
    globalFilterFn: fuzzyFilter,
    enableSortingRemoval: false,  // Toggle between asc/desc only (no "none" state)
    state: {
      globalFilter,
      sorting,
    },
    onGlobalFilterChange: setGlobalFilter,
    onSortingChange: setSorting,
    getCoreRowModel: getCoreRowModel(),
    getFilteredRowModel: getFilteredRowModel(),
    getSortedRowModel: getSortedRowModel(),
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

  // Setup row virtualizer
  const rowVirtualizer = useVirtualizer({
    count: rows.length,
    getScrollElement: () => tableContainerRef.current,
    estimateSize: () => 28,  // Estimated row height in pixels (reduced from 35)
    overscan: 10,  // Render 10 extra rows above/below viewport
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
        filteredRowCount={rows.length}
        totalRowCount={data.length}
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
        {loading ? (
          <div className="h-full flex flex-col items-center justify-center gap-2">
            <Spinner className="w-8 h-8" />
            <p className="text-sm text-muted-foreground">
              Loading {pluralize(selectedGVK?.kind?.toLowerCase() ?? 'resource')}...
            </p>
          </div>
        ) : data.length === 0 ? (
          <div className="h-full flex items-center justify-center">
            <p className="text-sm text-muted-foreground">No resources found</p>
          </div>
        ) : rows.length === 0 ? (
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
                const row = rows[virtualRow.index];
                const isRowFocused = focusedRowIndex === virtualRow.index;
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
                      // Debounced popover: only show after delay (popoverCell state)
                      const showCellPopover = popoverCell?.row === virtualRow.index && popoverCell?.col === cellIndex;
                      const cellKey = `${row.id}-${cell.column.id}`;
                      const showCopied = copiedCellKey === cellKey;
                      const isPreviewCell = cell.column.id.startsWith('_preview.');
                      const isColumnHighlighted = highlightedColumnPath && cell.column.id === highlightedColumnPath.join('.');

                      // Get cell value and highlight indices for direct rendering
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
                            // Mouse hover moves focus to this cell
                            setFocusedRowIndex(virtualRow.index);
                            setFocusedColIndex(cellIndex);
                          }}
                        >
                          <CellContent
                            value={value}
                            highlightIndices={highlightIndices}
                            isFocused={isCellFocused}
                            showPopover={showCellPopover}
                            showCopied={showCopied}
                          />
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
