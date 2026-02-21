import { Input } from './ui/input';
import { Button } from './ui/button';
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from './ui/dropdown-menu';
import { Download, Clipboard, FileDown, AlertCircle } from 'lucide-react';
import { useState, forwardRef, useRef, useImperativeHandle, useCallback } from 'react';
import { Kbd } from './ui/kbd';
import { convertToCSV, copyToClipboard, downloadCSV } from '@/lib/csv-export';
import { pluralize } from '@/lib/utils';
import { SaveFile } from '../../wailsjs/go/main/App';

interface DIYTableToolbarProps {
  globalFilter: string;
  onGlobalFilterChange: (value: string) => void;
  filteredRowCount: number;
  totalRowCount: number;
  /** Whether data is still loading (hides count during initial load) */
  isLoading?: boolean;
  // Export data
  headers: string[];
  rows: any[][];
  resourceKind?: string; // For filename generation
  onSearchFocusChange?: (focused: boolean) => void;
  onBeforeExport?: () => void; // Called before export (e.g., to clear preview)
  // Sidebar expand button (shown when sidebar is collapsed)
  expandButton?: React.ReactNode;
}

export interface DIYTableToolbarHandle {
  focusSearch: () => void;
  isSearchFocused: () => boolean;
  exportToClipboard: () => void;
  exportToFile: () => void;
}

export const DIYTableToolbar = forwardRef<DIYTableToolbarHandle, DIYTableToolbarProps>(({
  globalFilter,
  onGlobalFilterChange,
  filteredRowCount,
  totalRowCount,
  isLoading = false,
  headers,
  rows,
  resourceKind = 'resources',
  onSearchFocusChange,
  onBeforeExport,
  expandButton,
}, ref) => {
  const [exporting, setExporting] = useState(false);
  const [exportStatus, setExportStatus] = useState<'idle' | 'copied' | 'downloaded' | 'error'>('idle');
  const searchInputRef = useRef<HTMLInputElement>(null);

  const handleExportToClipboard = useCallback(async () => {
    onBeforeExport?.();
    try {
      setExporting(true);
      const csvContent = convertToCSV(headers, rows);
      await copyToClipboard(csvContent);
      setExportStatus('copied');
      setTimeout(() => setExportStatus('idle'), 1000);
    } catch (error) {
      console.error('Failed to copy to clipboard:', error);
      setExportStatus('error');
      setTimeout(() => setExportStatus('idle'), 1000);
    } finally {
      setExporting(false);
    }
  }, [headers, rows, onBeforeExport]);

  const handleExportToFile = useCallback(async () => {
    onBeforeExport?.();
    try {
      setExporting(true);
      const csvContent = convertToCSV(headers, rows);
      const now = new Date();
      const timestamp = now.toISOString()
        .replace(/T/, '_')
        .replace(/:/g, '-')
        .replace(/\.\d+Z$/, ''); // YYYY-MM-DD_HH-MM-SS
      const filename = `${resourceKind}-${timestamp}.csv`;

      const savedPath = await downloadCSV(csvContent, filename, SaveFile);

      if (savedPath) {
        // File was saved successfully
        console.log('File saved to:', savedPath);
        setExportStatus('downloaded');
      } else {
        // User cancelled
        setExportStatus('idle');
      }

      setTimeout(() => setExportStatus('idle'), 1000);
    } catch (error) {
      console.error('Failed to download file:', error);
      setExportStatus('error');
      setTimeout(() => setExportStatus('idle'), 1000);
    } finally {
      setExporting(false);
    }
  }, [headers, rows, resourceKind, onBeforeExport]);

  // Expose methods via ref
  useImperativeHandle(ref, () => ({
    focusSearch: () => {
      searchInputRef.current?.focus();
    },
    isSearchFocused: () => {
      return document.activeElement === searchInputRef.current;
    },
    exportToClipboard: handleExportToClipboard,
    exportToFile: handleExportToFile,
  }), [handleExportToClipboard, handleExportToFile]);

  return (
    <div className="h-10 px-3 border-b border-border flex items-center">
      <div className="flex items-center justify-between gap-4 w-full">
        {/* Left: Expand button (when collapsed) + Search input with resource count */}
        <div className="flex items-center gap-3">
          {expandButton}
          <Input
            ref={searchInputRef}
            placeholder="Search ..."
            value={globalFilter}
            onChange={(e) => onGlobalFilterChange(e.target.value)}
            onKeyDown={(e) => {
              // Prevent keydown events from propagating to MainView's global keymap
              // This ensures typing in search doesn't trigger cell navigation or other shortcuts
              e.stopPropagation();
            }}
            onFocus={() => onSearchFocusChange?.(true)}
            onBlur={() => onSearchFocusChange?.(false)}
            className="w-48 h-6 py-0 px-2 text-sm"
          />
          <span className="text-sm text-muted-foreground whitespace-nowrap">
            {isLoading
              ? `Loading ${pluralize(resourceKind)}...`
              : globalFilter
                ? `${filteredRowCount}/${totalRowCount} ${pluralize(resourceKind)}`
                : `${totalRowCount} ${pluralize(resourceKind)}`
            }
          </span>
        </div>

        {/* Right: Export button */}
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button
              variant="ghost"
              size="sm"
              disabled={exporting || rows.length === 0}
              className="gap-2 h-6 py-0 text-sm"
            >
              {exportStatus === 'idle' && <Download className="h-4 w-4" />}
              {exportStatus === 'copied' && <Clipboard className="h-4 w-4 text-primary" />}
              {exportStatus === 'downloaded' && <FileDown className="h-4 w-4 text-primary" />}
              {exportStatus === 'error' && <AlertCircle className="h-4 w-4 text-destructive" />}

              <span className={
                exportStatus === 'copied' || exportStatus === 'downloaded'
                  ? 'text-primary'
                  : exportStatus === 'error'
                  ? 'text-destructive'
                  : ''
              }>
                {exportStatus === 'copied' && 'Copied'}
                {exportStatus === 'downloaded' && 'Downloaded'}
                {exportStatus === 'error' && 'Error'}
                {exportStatus === 'idle' && 'Export'}
              </span>
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            <DropdownMenuItem onClick={handleExportToClipboard}>
              <Clipboard className="mr-2 h-4 w-4" />
              <span className="flex-1">Copy to Clipboard</span>
              <span className="ml-4 flex items-center gap-0.5">
                <Kbd>⌘</Kbd><Kbd>⇧</Kbd><Kbd>C</Kbd>
              </span>
            </DropdownMenuItem>
            <DropdownMenuItem onClick={handleExportToFile}>
              <FileDown className="mr-2 h-4 w-4" />
              <span className="flex-1">Download as File</span>
              <span className="ml-4 flex items-center gap-0.5">
                <Kbd>⌘</Kbd><Kbd>⇧</Kbd><Kbd>S</Kbd>
              </span>
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
    </div>
  );
});

DIYTableToolbar.displayName = 'DIYTableToolbar';
