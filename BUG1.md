# BUG1: DynamicFieldTree — map[string]string 필드 (annotations, labels) 펼칠 수 없음

## Status

Open — 테스트 블로커

## Symptom

DynamicFieldTree에서 `metadata.annotations`와 `metadata.labels` (타입: `map[string]string`)가
토글 화살표도, 체크박스도 없는 비활성 상태로 렌더된다.

- 펼칠 수 없음 (expand toggle 없음)
- 선택할 수 없음 (checkbox 없음)
- 빈 spacer `<span className="w-4 mr-1.5 shrink-0" />`만 렌더됨

이로 인해 다음 수동 테스트를 진행할 수 없다:

| 테스트 항목 | 영향 |
| --- | --- |
| 3.1 새 필드 추가 (cache miss → skeleton → 값) | labels/annotations 하위 필드 선택 불가 |
| 3.4 필드 제거 후 재추가 | 위와 동일 |
| 2.6 실시간 업데이트 | labels 변경에 대한 셀 플래시 확인 불가 |

## Root Cause

4-way 렌더링 조건문에서 "children이 없는 map 타입" 케이스가 빠져 있다.

### Go 측 (schema → field tree)

`internal/kube/schema.go`의 `createFieldList`에서 `map[string]string` 처리:

1. `AdditionalProperties.Schema`가 primitive `string` → children 생성 안됨 → `Field.Children = nil`
2. `internal/kube/node.go`의 `CreateNodeTree`에서 `field.IsMap()` → `children = make(map[string]*Node)` (빈 맵, nil 아님)
3. 리소스 데이터 없는 상태 (schema-only tree): `getDistinctKeys`가 빈 결과 → children 맵은 있지만 비어있음

결과: 프론트엔드에 `{ name: "annotations", type: "map[string]string", children: [] }` 전달됨.

### React 측 (렌더링 조건)

`DynamicFieldTree.tsx:73-75`:

```typescript
const hasChildren = node.children && node.children.length > 0;  // false (빈 배열)
const isArrayOrMap = node.type && (node.type.startsWith('[]') || node.type.startsWith('map['));  // true
const isLeaf = !hasChildren && !isArrayOrMap;  // false
```

4-way 조건 (`DynamicFieldTree.tsx:140-173`):

| 조건 | annotations 결과 | 렌더링 |
| --- | --- | --- |
| `node.name === '*' && !hasChildren` | false (name은 "annotations") | — |
| `hasChildren` | false (children 빈 배열) | — |
| `isLeaf` | false (map 타입이므로) | — |
| **else (fallthrough)** | **여기에 빠짐** | **빈 spacer** |

### 동일 패턴 파일

- `useTree.ts:356-360` — `isLeafNode` 헬퍼: 같은 로직으로 keyboard navigation도 불가
- `useTree.ts:996-1013` — `toggleFocused`: Enter/Space 눌러도 반응 없음

## Reproduction

데이터가 로드된 후에도 재현됨 (annotations/labels가 빈 리소스가 있는 경우).
Schema-only tree (watch 전) 에서는 항상 재현.

## Fix Options

### Option A: children 없는 map/array를 leaf로 처리 (minimal)

```typescript
// DynamicFieldTree.tsx:75
const isLeaf = !hasChildren;  // isArrayOrMap 조건 제거
```

영향: 빈 `[]ContainerPort` 같은 array 타입도 leaf 체크박스로 표시됨.

### Option B: primitive map만 leaf로 처리 (targeted)

```typescript
const isPrimitiveValueMap = node.type?.match(/^map\[string\](string|integer|boolean|number)$/);
const isLeaf = !hasChildren && (!isArrayOrMap || isPrimitiveValueMap);
```

영향: `map[string]string`만 leaf, `map[string]SomeObject`는 기존대로 expandable.

### Option C: Go 측에서 schema-only tree에 map의 빈 children을 보내지 않기

`node.go`에서 `len(children) == 0`이면 `children = nil`로 설정 → 프론트에서 hasChildren=false, isArrayOrMap=true, isLeaf=false → 여전히 문제. Go 단독으로는 해결 불가, React 측 수정 필수.

## Affected Files

| File | Lines | Role |
| --- | --- | --- |
| `cmd/gui/frontend/src/components/DynamicFieldTree.tsx` | 73-75, 140-173 | 렌더링 조건 |
| `cmd/gui/frontend/src/hooks/useTree.ts` | 356-360, 996-1013 | isLeafNode, toggleFocused |
| `internal/kube/node.go` | 178-200 | 빈 children map 생성 |
| `internal/kube/schema.go` | 188-194 | map value type children 생성 |
