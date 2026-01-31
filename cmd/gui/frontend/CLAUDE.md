# Frontend (React + Wails)

## Stack

- React 18, TypeScript, Vite
- Tailwind CSS, Radix UI
- @tanstack/react-table, @tanstack/react-virtual

## Wails IPC

Go 함수 호출:
```typescript
window.go.main.App.MethodName()
```

이벤트 구독:
```typescript
import { EventsOn } from '../wailsjs/runtime/runtime';
EventsOn("event-name", (data) => {
  // handle event
});
```

## Wails Dev Mode

**`wails dev`가 백그라운드에서 실행 중이라고 가정.**

- Go 코드 변경 시 바인딩 자동 재생성
- Frontend 핫 리로드 자동
- 명시적 `wails generate module` 불필요

바인딩 위치: `src/wailsjs/` (자동 생성, 수정 금지)

### 바인딩 생성 규칙

```
wails dev 실행 중?
├── YES → 바인딩 자동 생성됨, 추가 작업 불필요
│         (Go 파일 저장 시 수 초 내 반영)
│
└── NO  → Fallback: wails generate module 실행
          bash -c 'cd /path/to/kattle && wails generate module'
```

**충돌 방지**:
- `wails dev`가 실행 중일 때 `wails generate module` 실행 금지 (파일 충돌 가능)
- 바인딩 함수가 이미 존재하면 재생성 불필요 (idempotent)
- Go 메서드 시그니처 변경 시에만 바인딩 갱신 필요

**확인 방법**:
```bash
# wails dev 실행 여부
pgrep -f "wails dev" && echo "RUNNING" || echo "NOT RUNNING"

# 바인딩 존재 여부 (예: App.ListResources)
grep -q "ListResources" src/wailsjs/go/main/App.js && echo "EXISTS"
```

## Key Patterns

- **8000+ 필드 가상화 테이블**: @tanstack/react-virtual 사용
- **실시간 리소스 업데이트**: `sync:progress` 이벤트 구독
- **동적 필드 트리 렌더링**: 계층적 JSON 구조 시각화

## Testing

```bash
npm run test:run    # Vitest + React Testing Library
npm run test        # Watch mode
```

## Backend Collaboration

- Go 백엔드 변경 시 Wails 바인딩 재생성 필요 (`wails dev` 실행 중이면 자동)
- 메모리 최적화는 backend 담당, frontend는 UI 반영만
- 새 Go 메서드 추가 후: `wails dev` 실행 중이면 자동 반영, 아니면 `wails generate module`

## Directory Structure

```
src/
├── components/     # React 컴포넌트
├── hooks/          # Custom hooks
├── lib/            # Utilities
└── wailsjs/        # Generated Wails bindings (DO NOT EDIT)
```

## Code Quality

See `CODE_QUALITY.md` in this directory for detailed guidelines.

Key rules:
- No type assertions (`as`)
- No unused imports
- Safe array access (check undefined)
- useCallback for event handlers
- Complete dependency arrays
