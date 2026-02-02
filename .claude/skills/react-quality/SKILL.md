---
name: react-quality
description: React/TypeScript code quality rules for kattle frontend. Apply when writing or reviewing frontend code.
disable-model-invocation: true
user-invocable: false
allowed-tools: Read, Edit, Write, Grep, Glob
---

# React/TypeScript Code Quality Guidelines

## Overview
This skill enforces critical code quality standards for React and TypeScript in the kattle frontend. Apply these rules rigorously when writing, modifying, or reviewing components.

## Essential Rules

### 1. No Type Assertions (`as`)
**Rule:** Use explicit type declarations instead of type assertions.

**Why:** Type assertions bypass TypeScript's type checking and hide potential bugs.

**Bad:**
```typescript
const user = data as User;
const count = value as number;
```

**Good:**
```typescript
const user: User = parseUserData(data);
const count: number = parseInt(value, 10);
```

---

### 2. No Unused Imports
**Rule:** Remove all unused imports from files.

**Why:** Clutters code, increases bundle size, and creates maintenance confusion.

**Check:** TypeScript will flag unused imports. Remove them during code review.

---

### 3. Safe Array Access
**Rule:** Always check for undefined before accessing array elements or object properties.

**Why:** Prevents runtime crashes from accessing undefined values.

**Bad:**
```typescript
const item = items[0];
const name = user.profile.name; // what if user or profile is undefined?
```

**Good:**
```typescript
const item = items?.[0];
const name = user?.profile?.name ?? 'Unknown';
```

---

### 4. Explicit Type Annotations for Complex Structures
**Rule:** Add type annotations for function parameters, return types, and complex state.

**Why:** Makes code self-documenting and catches type errors early.

**Bad:**
```typescript
function processData(data) {
  const result = { ...data };
  return result;
}

const [state, setState] = useState({});
```

**Good:**
```typescript
interface ProcessedData {
  id: string;
  timestamp: Date;
  value: number;
}

function processData(data: unknown): ProcessedData {
  const result: ProcessedData = { ...data as ProcessedData };
  return result;
}

interface AppState {
  user: User | null;
  isLoading: boolean;
}

const [state, setState] = useState<AppState>({
  user: null,
  isLoading: false,
});
```

---

### 5. React Element Keys - Unique and Stable
**Rule:** Always provide stable, unique keys in lists. Never use array indices.

**Why:** React uses keys to match elements across renders. Unstable keys cause state loss and bugs.

**Bad:**
```typescript
{items.map((item, index) => (
  <div key={index}>{item.name}</div>
))}
```

**Good:**
```typescript
{items.map((item) => (
  <div key={item.id}>{item.name}</div>
))}
```

---

### 6. useCallback for Event Handlers
**Rule:** Wrap event handler functions in `useCallback` to prevent unnecessary re-renders of child components.

**Why:** Prevents children from re-rendering when parent updates, improving performance.

**Bad:**
```typescript
const handleClick = () => {
  dispatch(updateState());
};

return <Button onClick={handleClick} />;
```

**Good:**
```typescript
const handleClick = useCallback(() => {
  dispatch(updateState());
}, [dispatch]);

return <Button onClick={handleClick} />;
```

---

### 7. Functional State Updates
**Rule:** Use the function form of `setState` when the new state depends on previous state.

**Why:** Ensures you're always working with the latest state, preventing race conditions.

**Bad:**
```typescript
const [count, setCount] = useState(0);
const increment = () => setCount(count + 1);
```

**Good:**
```typescript
const [count, setCount] = useState(0);
const increment = useCallback(() => {
  setCount((prev) => prev + 1);
}, []);
```

---

### 8. Complete Dependency Arrays
**Rule:** Always include all external values used in hooks (useEffect, useCallback, useMemo) in their dependency arrays.

**Why:** Missing dependencies cause stale closures and bugs that are hard to debug.

**Bad:**
```typescript
useEffect(() => {
  console.log(userId); // userId is missing from deps!
  fetchUser();
}, []);
```

**Good:**
```typescript
useEffect(() => {
  console.log(userId);
  fetchUser();
}, [userId]);
```

---

### 9. Component Decomposition
**Rule:** Split components when they exceed 300 lines. Extract sections into separate components.

**Why:** Large components are hard to understand, test, and maintain.

**Indicators of a "fat" component:**
- Multiple independent sections with their own state
- Deeply nested JSX
- Multiple responsibilities (data fetching, UI rendering, business logic)

**Action:** Extract into smaller, focused components with clear interfaces.

---

### 10. Props and State Typing
**Rule:** Define explicit interfaces for component props. Never use `any` or `unknown` without narrowing.

**Why:** Makes component contracts clear and catches prop errors early.

**Bad:**
```typescript
function Card(props: any) {
  return <div>{props.content}</div>;
}
```

**Good:**
```typescript
interface CardProps {
  content: string;
  variant?: 'default' | 'outlined';
  onClick?: (id: string) => void;
}

function Card({ content, variant = 'default', onClick }: CardProps) {
  return <div onClick={() => onClick?.(content)}>{content}</div>;
}
```

---

## Quick Checklist

Before submitting code or approving a PR, verify:

- [ ] No `as` type assertions
- [ ] No unused imports
- [ ] All array/object access is safe (using optional chaining or checks)
- [ ] Function parameters and return types have explicit annotations
- [ ] Component props defined in a clear interface
- [ ] List items have stable, unique `key` props
- [ ] Event handlers wrapped in `useCallback` (if passed to children)
- [ ] State updates use functional form when dependent on previous state
- [ ] All hook dependencies are complete and correct
- [ ] Components are decomposed (max 300 lines)
- [ ] No `any` types without narrowing

---

## When to Apply This Skill

Apply these rules when:
- Writing new React components
- Reviewing pull requests with TypeScript/React changes
- Refactoring existing components
- Debugging type-related issues

Prioritize rules 1-8 as they prevent the most common and serious bugs.
