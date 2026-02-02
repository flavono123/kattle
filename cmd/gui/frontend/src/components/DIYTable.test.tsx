/**
 * Tests for DIYTable component
 *
 * Focus on:
 * - Cell focus cleared when isTableFocused becomes false (Tab to nav panel)
 * - Cell focus cleared when mouse leaves table area
 */

import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { DIYTable } from './DIYTable';

// Mock data
const mockData = [
  { _context: 'ctx1', metadata: { name: 'pod-1', namespace: 'default' } },
  { _context: 'ctx1', metadata: { name: 'pod-2', namespace: 'kube-system' } },
  { _context: 'ctx1', metadata: { name: 'pod-3', namespace: 'default' } },
];

// Mock useResourceData hook
vi.mock('../hooks/useResourceData', () => ({
  useResourceData: vi.fn(() => ({
    data: mockData,
    loading: false,
    error: null,
    refresh: vi.fn(),
    watchStatus: 'connected',
    getRowId: (row: any) => `${row._context}/${row.metadata.namespace}/${row.metadata.name}`,
    changedCells: [],
  })),
}));

// Mock useFlashingCells hook
vi.mock('../hooks/useFlashingCells', () => ({
  useFlashingCells: vi.fn(() => ({
    isFlashing: () => false,
  })),
}));

// Mock useWindowedData hook (not used when useWindowedMode=false, but needs to be mocked)
vi.mock('../hooks/useWindowedData', () => ({
  useWindowedData: vi.fn(() => ({
    totalCount: 0,
    visibleRows: new Map(),
    loading: false,
    error: null,
    watchStatus: 'disconnected',
    getRowData: () => undefined,
    getRowId: () => '',
    onVisibleRangeChange: vi.fn(),
    fetchRange: { start: 0, end: 0 },
    refresh: vi.fn(),
  })),
}));

// Mock useVirtualizer to render all rows (bypass virtualization in tests)
vi.mock('@tanstack/react-virtual', () => ({
  useVirtualizer: vi.fn(({ count }) => ({
    getVirtualItems: () =>
      Array.from({ length: count }, (_, i) => ({
        index: i,
        start: i * 28,
        size: 28,
        key: i,
      })),
    getTotalSize: () => count * 28,
    scrollToIndex: vi.fn(),
  })),
}));

// Mock Wails SaveFile function
vi.mock('../../wailsjs/go/main/App', () => ({
  SaveFile: vi.fn(),
}));

const defaultGVK = {
  group: '',
  version: 'v1',
  kind: 'Pod',
  contexts: ['ctx1'],
  allCount: 1,
  shortNames: ['po'],
};

const defaultProps = {
  selectedFields: [['metadata', 'namespace']],
  selectedGVK: defaultGVK,
  connectedContexts: ['ctx1'],
  isTableFocused: true,
};

// Helper to find focused cells (CellContent uses underline class when focused)
const getFocusedCellCount = () => {
  // CellContent with isFocused=true has "underline bg-accent/50" class
  return document.querySelectorAll('[class*="underline"][class*="bg-accent"]').length;
};


describe('DIYTable - Cell Focus Clear on Panel Switch', () => {
  it('should clear cell focus when isTableFocused becomes false', async () => {
    const { rerender } = render(<DIYTable {...defaultProps} isTableFocused={true} />);

    // Wait for table to render
    await waitFor(() => {
      expect(screen.getByText('pod-1')).toBeInTheDocument();
    });

    // Hover over a cell to create focus
    const cell = screen.getByText('pod-1');
    fireEvent.mouseEnter(cell.closest('div[class*="px-1"]')!);

    // The cell should show focused state (underline class)
    expect(getFocusedCellCount()).toBeGreaterThan(0);

    // Now simulate Tab to nav panel by changing isTableFocused to false
    rerender(<DIYTable {...defaultProps} isTableFocused={false} />);

    // After isTableFocused becomes false, cell focus should be cleared
    expect(getFocusedCellCount()).toBe(0);
  });

  it('should not show cell focus when table panel is not focused', async () => {
    render(<DIYTable {...defaultProps} isTableFocused={false} />);

    await waitFor(() => {
      expect(screen.getByText('pod-1')).toBeInTheDocument();
    });

    // Initially no cells should be focused
    expect(getFocusedCellCount()).toBe(0);
  });
});

describe('DIYTable - Cell Focus Clear on Mouse Leave', () => {
  it('should clear cell focus when mouse leaves table area', async () => {
    render(<DIYTable {...defaultProps} />);

    await waitFor(() => {
      expect(screen.getByText('pod-1')).toBeInTheDocument();
    });

    // Find the table container (has overflow-auto class)
    const tableContainer = document.querySelector('.overflow-auto');
    expect(tableContainer).not.toBeNull();

    // Hover over a cell to create focus
    const cell = screen.getByText('pod-1');
    fireEvent.mouseEnter(cell.closest('div[class*="px-1"]')!);

    // Verify cell is focused
    expect(getFocusedCellCount()).toBeGreaterThan(0);

    // Mouse leave from table container
    fireEvent.mouseLeave(tableContainer!);

    // After mouse leave, cell focus should be cleared
    expect(getFocusedCellCount()).toBe(0);
  });
});

describe('DIYTable - Search Focus Clear', () => {
  it('should clear cell focus when search input is focused', async () => {
    render(<DIYTable {...defaultProps} />);

    await waitFor(() => {
      expect(screen.getByText('pod-1')).toBeInTheDocument();
    });

    // Hover over a cell to create focus
    const cell = screen.getByText('pod-1');
    fireEvent.mouseEnter(cell.closest('div[class*="px-1"]')!);

    // Verify cell is focused
    expect(getFocusedCellCount()).toBeGreaterThan(0);

    // Focus the search input
    const searchInput = screen.getByPlaceholderText('Search ...');
    fireEvent.focus(searchInput);

    // After search focus, cell focus should be cleared
    expect(getFocusedCellCount()).toBe(0);
  });
});

describe('DIYTable - Column Hover Sync (RT → NP)', () => {
  it('should call onColumnFocus with path when hovering a column header', async () => {
    const mockOnColumnFocus = vi.fn();

    render(
      <DIYTable
        {...defaultProps}
        onColumnFocus={mockOnColumnFocus}
      />
    );

    await waitFor(() => {
      // Use getAllByText since there are multiple cells with same value
      expect(screen.getAllByText('pod-1').length).toBeGreaterThan(0);
    });

    // Find the 'namespace' column header (from defaultProps.selectedFields)
    const namespaceHeader = screen.getByText('namespace');
    const headerCell = namespaceHeader.closest('div[class*="cursor-grab"]');

    // Hover over the column header
    fireEvent.mouseEnter(headerCell!);

    // onColumnFocus should be called with the field path
    expect(mockOnColumnFocus).toHaveBeenCalledWith(['metadata', 'namespace']);
  });

  it('should call onColumnFocus with null when mouse leaves column header', async () => {
    const mockOnColumnFocus = vi.fn();

    render(
      <DIYTable
        {...defaultProps}
        onColumnFocus={mockOnColumnFocus}
      />
    );

    await waitFor(() => {
      expect(screen.getAllByText('pod-1').length).toBeGreaterThan(0);
    });

    const namespaceHeader = screen.getByText('namespace');
    const headerCell = namespaceHeader.closest('div[class*="cursor-grab"]');

    // Hover then leave
    fireEvent.mouseEnter(headerCell!);
    expect(mockOnColumnFocus).toHaveBeenCalledWith(['metadata', 'namespace']);

    fireEvent.mouseLeave(headerCell!);
    expect(mockOnColumnFocus).toHaveBeenLastCalledWith(null);
  });
});

describe('DIYTable - Column Highlight from NP (NP → RT)', () => {
  it('should apply highlight style to entire column when highlightedColumnPath matches', async () => {
    render(
      <DIYTable
        {...defaultProps}
        highlightedColumnPath={['metadata', 'namespace']}
      />
    );

    await waitFor(() => {
      expect(screen.getAllByText('pod-1').length).toBeGreaterThan(0);
    });

    // The 'namespace' column header should have highlight style
    const namespaceHeader = screen.getByText('namespace');
    const headerCell = namespaceHeader.closest('div[class*="cursor-grab"]');
    expect(headerCell?.className).toContain('bg-focus');

    // Data cells in the namespace column should also have highlight style
    // 'default' and 'kube-system' are namespace values from mockData
    const defaultCells = screen.getAllByText('default');
    expect(defaultCells.length).toBeGreaterThan(0);
    // Find the cell container (parent div with px-1 class)
    const cellContainer = defaultCells[0].closest('div[class*="px-1"]');
    expect(cellContainer?.className).toContain('bg-focus');
  });

  it('should not highlight columns when highlightedColumnPath is undefined', async () => {
    render(
      <DIYTable
        {...defaultProps}
        highlightedColumnPath={undefined}
      />
    );

    await waitFor(() => {
      expect(screen.getAllByText('pod-1').length).toBeGreaterThan(0);
    });

    // No column should have highlight style
    const namespaceHeader = screen.getByText('namespace');
    const headerCell = namespaceHeader.closest('div[class*="cursor-grab"]');
    expect(headerCell?.className).not.toContain('bg-focus');
  });
});

describe('DIYTable - Preview Column', () => {
  it('should render preview column with muted style when previewField is provided', async () => {
    render(
      <DIYTable
        {...defaultProps}
        previewField={['metadata', 'name']}
      />
    );

    await waitFor(() => {
      expect(screen.getAllByText('pod-1').length).toBeGreaterThan(0);
    });

    // Should render the preview column header ('name' from previewField)
    const nameHeaders = screen.getAllByText('name');
    expect(nameHeaders.length).toBeGreaterThan(0);

    // Preview column header has opacity-50 and border-dashed (not cursor-grab since not draggable)
    const previewHeaderCell = document.querySelector('div[class*="opacity-50"][class*="border-dashed"]');
    expect(previewHeaderCell).not.toBeNull();
  });

  it('should render preview column data with muted style', async () => {
    render(
      <DIYTable
        {...defaultProps}
        previewField={['metadata', 'name']}
      />
    );

    await waitFor(() => {
      expect(screen.getAllByText('pod-1').length).toBeGreaterThan(0);
    });

    // Preview column cells also have opacity-50 and border-dashed
    const previewCells = document.querySelectorAll('div[class*="opacity-50"][class*="border-dashed"]');
    // Should have at least header + data cells
    expect(previewCells.length).toBeGreaterThan(1);
  });

  it('should not show sort icons on preview column header', async () => {
    render(
      <DIYTable
        {...defaultProps}
        previewField={['metadata', 'name']}
      />
    );

    await waitFor(() => {
      expect(screen.getAllByText('pod-1').length).toBeGreaterThan(0);
    });

    // Find preview column header (has opacity-50 and border-dashed, contains "name" text)
    const previewHeaderCell = document.querySelector('div[class*="opacity-50"][class*="border-dashed"]');
    expect(previewHeaderCell).not.toBeNull();

    // Preview header should not have sort chevron icons
    const sortIcons = previewHeaderCell?.querySelectorAll('svg');
    expect(sortIcons?.length ?? 0).toBe(0);
  });
});

describe('DIYTable - Basic Rendering', () => {
  it('should render table with data', async () => {
    render(<DIYTable {...defaultProps} />);

    await waitFor(() => {
      expect(screen.getByText('pod-1')).toBeInTheDocument();
      expect(screen.getByText('pod-2')).toBeInTheDocument();
      expect(screen.getByText('pod-3')).toBeInTheDocument();
    });
  });

  it('should render column headers', async () => {
    render(<DIYTable {...defaultProps} />);

    await waitFor(() => {
      // Headers are uppercase
      expect(screen.getByText('name')).toBeInTheDocument();
      expect(screen.getByText('namespace')).toBeInTheDocument();
    });
  });

  it('should render search toolbar', () => {
    render(<DIYTable {...defaultProps} />);

    expect(screen.getByPlaceholderText('Search ...')).toBeInTheDocument();
  });
});
