# Frontend Code Quality Guidelines

## Core Principles

- No type assertions (`as`)
- No unused imports
- Safe array access (check undefined)
- `useCallback` for event handlers
- Complete dependency arrays
- Focused components (<300 lines)

---

## 1. Type Assertions

**Never use type assertions.** They bypass TypeScript's type checking.

```tsx
// ❌ Don't
const indices = [] as [number, number][];
const result = data as MyType;

// ✅ Do
const indices: [number, number][] = [];
const result: MyType = { /* explicit properties */ };
```

---

## 2. Type Safety

### Safe Array Access

```tsx
// ❌ Don't
const item = items[index].property;

// ✅ Do
const item = items[index];
if (item) {
  const property = item.property;
}
```

### Explicit Type Annotations

```tsx
// ✅ Do
interface Result { id: string; name: string; }

const results = useMemo<Result[]>(() => {
  return items.map((item): Result => ({
    id: item.id,
    name: item.name,
  }));
}, [items]);
```

### React Element Keys

All list elements must have unique, stable keys.

```tsx
// ❌ Don't
parts.push(<mark key={idx}>{text}</mark>); // Index as key

// ✅ Do
let keyCounter = 0;
parts.push(<mark key={`mark-${keyCounter++}`}>{text}</mark>);
```

---

## 3. React Best Practices

### useCallback for Handlers

```tsx
// ❌ Don't
const handleClick = (id: string) => setSelected(id);

// ✅ Do
const handleClick = useCallback((id: string) => {
  setSelected(id);
}, []);
```

### Functional State Updates

When new state depends on previous state:

```tsx
// ✅ Do
const handleToggle = useCallback((item: string) => {
  setSelectedItems((prev) => {
    const newSet = new Set(prev);
    newSet.has(item) ? newSet.delete(item) : newSet.add(item);
    return newSet;
  });
}, []);
```

### Complete Dependency Arrays

```tsx
// ❌ Don't
useEffect(() => {
  handleSearch(query);
}, [query]); // Missing handleSearch

// ✅ Do
useEffect(() => {
  handleSearch(query);
}, [query, handleSearch]);
```

---

## 4. Imports

### Import Only What You Need

```tsx
// ❌ Don't
import * as React from "react";
import { FC, ReactNode, useState, useEffect, useMemo } from "react";

// ✅ Do
import { useState, useEffect } from "react";
```

### Import Order

1. External (React, libraries)
2. Internal (utils, components)
3. Types (if using `import type`)

---

## 5. Type Definitions

```tsx
// Interface for object shapes
interface User {
  id: string;
  name: string;
}

// Type for unions
type Status = "idle" | "loading" | "success" | "error";

// Readonly for immutable props
interface Props {
  readonly items: readonly string[];
  readonly onSelect: (id: string) => void;
}
```

---

## 6. Component Decomposition

### Signs a Component Needs Splitting

- Exceeds ~200-300 lines
- Handles unrelated UI sections
- Multiple `useState` for different features
- Complex state logic

### Extraction Strategies

| Extract to | When |
|------------|------|
| **Component** | Visually distinct UI section, reusable |
| **Custom Hook** | Reusable state logic, complex effects |
| **Utility Function** | Pure data transformation, no React deps |

```tsx
// ❌ Fat component
function NavigationPanel() {
  return (
    <div>
      <div className="header">
        {/* 50+ lines */}
      </div>
      <div className="context">
        {/* 50+ lines */}
      </div>
    </div>
  );
}

// ✅ Focused components
function NavigationPanel() {
  return (
    <div>
      <NavHeader title={title} onCollapse={onCollapse} />
      <ContextDisplay contexts={contexts} />
    </div>
  );
}
```

### When NOT to Split

- Premature abstraction (no clear benefit)
- Over-fragmentation (single-use 10-line components)
- Prop drilling (5+ props through layers)

---

## 7. Enforcement

### Pre-commit Checks

```bash
tsc --noEmit          # Type check
eslint .              # Lint
npm run test:run      # Tests
```

### Recommended Config

```json
// tsconfig.json
{
  "compilerOptions": {
    "strict": true,
    "noUncheckedIndexedAccess": true,
    "noUnusedLocals": true,
    "noUnusedParameters": true
  }
}
```

```json
// eslint rules
{
  "@typescript-eslint/consistent-type-assertions": ["error", { "assertionStyle": "never" }],
  "react-hooks/exhaustive-deps": "error"
}
```

---

## Quick Checklist

Before submitting code:

- [ ] No type assertions (`as`)
- [ ] No unused imports
- [ ] Safe array access (check undefined)
- [ ] Event handlers use `useCallback`
- [ ] Functional state updates when needed
- [ ] Complete dependency arrays
- [ ] Unique keys for list items
- [ ] Components <300 lines, single concern
- [ ] Tests pass: `npm run test:run`
