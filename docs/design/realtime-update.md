# Realtime Update & Field Extraction

Windowed mode(SQLStore)에서 실시간 업데이트와 필드 추출이 어떻게 동작하는지 설명합니다.

## Null Convention (undefined vs null)

Backend `extractFieldsForSQL`에서 SQLStore에 저장하는 JSON의 값 규칙:

| 상태 | Go | JSON | JS | UI |
|------|-----|------|-----|-----|
| 추출됨, 값 있음 | `"Running"` | `"Running"` | `"Running"` | Running |
| 추출됨, 값 없음 | `nil` | `null` | `null` | `-` |
| 미추출 (키 자체 없음) | (absent) | (absent) | `undefined` | skeleton |

이 규칙 덕분에 frontend는 **데이터 자체**만으로 skeleton vs dash를 결정할 수 있습니다.
`extractingFields` 같은 state 기반 타이밍 추적이 필요 없습니다.

### 왜 이 방식인가

React `useEffect`로 `extractingFields` state를 설정하면 항상 한 프레임 뒤처집니다:
1. 새 컬럼이 나타나는 render → `extractingFields`는 아직 false (effect 미실행)
2. Effect 실행 → `extractingFields` true → 다음 render에서 skeleton

이 한 프레임 gap에서 사용자는 `-`를 봅니다. `confirmedFields`, `pendingFieldPaths` 같은 추가 state로
이 gap을 메우려 해도, `resource:update` re-fetch 타이밍과 꼬여서 해결이 안 됩니다.

**null convention**은 이 문제를 원천 제거합니다:
- 새 필드 추가 → SQLStore 데이터에 해당 키 없음 → `undefined` → skeleton (즉시, 어떤 render든)
- 추출 완료 + re-fetch → 키 존재 (`null` 또는 값) → `-` 또는 값 표시

## Data Flow

```
User selects new field (e.g., status.podIP)
    |
    v
SetSelectedFields([...existing, "status.podIP"])
    |
    +-- cache hit: field already in extractedFields
    |       → re-fetch from SQLStore (data already has null/value for this field)
    |       → skeleton clears immediately
    |
    +-- cache miss: new field not yet extracted
            → returns {extracting: true}
            → goroutine: LIST all resources → extractFieldsForSQL with new field
            |   - resource has podIP → stores "10.0.0.1"
            |   - resource has no podIP → stores null
            → emits "fields:ready"
            → frontend re-fetches → skeleton clears, shows values or "-"
```

During extraction, `resource:update` re-fetches are suppressed (`extractingFieldsRef.current` check)
to avoid showing partially-extracted data (mix of skeleton and values in same column).

## WaitGroup Ordering

`StartWatch`에서 `eventWg.Add(N)`은 goroutine 시작 **전에** 호출해야 합니다.
그렇지 않으면 `eventWg.Wait()`가 counter=0을 보고 즉시 `watchDone`을 닫아서,
`SetSelectedFields` extraction goroutine이 `isCancelled()=true`로 취소됩니다.

```go
// CORRECT: pre-add before goroutines start
syncWg.Add(len(validContexts))
eventWg.Add(len(validContexts))
for _, cg := range validContexts {
    go func(...) {
        defer syncWg.Done()
        // ...
        informerOk = true
        go func() { defer eventWg.Done(); ... }()
    }()
}

// WRONG: add inside goroutine (race with eventWg.Wait())
for _, ctx := range contexts {
    syncWg.Add(1)           // OK
    go func() {
        defer syncWg.Done()
        // ... network I/O ...
        eventWg.Add(1)      // TOO LATE: Wait() may have already returned
        go func() { defer eventWg.Done() }()
    }()
}
```
