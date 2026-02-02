# Kattle

## Project Overview

Kubernetes 탐색 도구 - CLI TUI + Desktop GUI
- Go 백엔드 + React 프론트엔드 (Wails)
- TUI (cmd/kupid): **Deprecated** - 유지보수만, 신규 개발 없음
- **주요 타겟**: Desktop GUI (cmd/gui)

## Architecture

```
cmd/kupid/          # CLI TUI (Bubble Tea) - DEPRECATED
cmd/gui/            # Desktop GUI (Wails + React) - PRIMARY TARGET
internal/kube/      # Kubernetes 클라이언트 (메모리 최적화 중점)
internal/ui/        # TUI 컴포넌트
website/            # 문서 사이트 (Astro)
```

## Current Focus

메모리 최적화 (Go 백엔드):
- KeyOnlyStore: 메타데이터만 저장하는 informer store
- FieldStore: 필요한 필드만 추출 저장
- String interning: 중복 문자열 최소화

프로파일링 베이스라인:
- `baseline_heap.pb.gz` - 힙 메모리 기준점
- `baseline_goroutine.txt` - 고루틴 수 기준점

---

## Session Prerequisites

장기 자율 세션 시작 전 확인:

```bash
# 1. wails dev 실행 중 (바인딩 자동 갱신, 핫 리로드)
pgrep -f "wails dev" || echo "Start: wails dev (background)"

# 2. pprof 엔드포인트 접근 가능 (메모리 프로파일링)
curl -s http://localhost:6060/debug/pprof/ > /dev/null && echo "pprof OK"

# 3. Vite dev server (Playwright 접근용, 기본 localhost:5173)
curl -s http://localhost:5173/ > /dev/null && echo "Vite OK"
```

Manager는 세션 시작 시 이 조건들을 확인하고, 누락 시 사용자에게 알림.

### 자율 실행 권한 설정

자율 루프가 매 명령마다 중단되지 않으려면 세션 시작 시 다음 권한을 승인:

```
Allowed prompts (ExitPlanMode 또는 첫 실행 시 설정):
- Bash: run go tests
- Bash: run npm/vitest tests
- Bash: capture memory profiles (curl, pprof)
- Bash: git operations (status, add, commit)
- Edit/Write: modify source files in internal/, cmd/
```

또는 CLI 옵션 사용: `claude --allowedTools 'Bash(run tests),Edit(*)'`

**주의**: `--dangerously-skip-permissions`는 보안상 권장하지 않음.

---

## Scenario Input (세션별)

세션 시작 시 사용자가 관측/검증 시나리오를 제공:

```yaml
# 예시: 메모리 관측 시나리오
scenario:
  type: memory-observation
  context: prodlive-gs-koreacentral
  resource: pods

  # 메모리 목표
  memory_targets:
    current_baseline: 450MB          # 현재 베이스라인 (참고용)
    target_baseline: 200MB           # 달성 목표
    max_threshold: 300MB             # 이 이상이면 FAIL
    goroutine_limit: 100             # 고루틴 수 상한

  checkpoints:
    - name: initial-load
      trigger: "리소스 목록 요청 직후"
      capture: [heap, goroutine]
    - name: after-load
      trigger: "로딩 완료 후 안정화 (5s)"
      capture: [heap, goroutine]

  # 검증 결과 판정
  verdict:
    pass: "heap <= max_threshold AND goroutines <= goroutine_limit"
    improve_baseline: "heap < current_baseline"
```

```yaml
# 예시: 기능 구현 시나리오
scenario:
  type: feature-implementation
  spec: planning/260105-optimize-go-mem.spec.md
  verification_mode: per-commit

  # 메모리 회귀 방지
  memory_guard:
    max_regression: 10%              # 베이스라인 대비 최대 허용 증가
    baseline_file: baseline_heap.pb.gz
```

Manager는 시나리오를 파싱하여:
1. 필요한 subagent 결정
2. Playwright로 UI 자동화 (memory-observation인 경우)
3. 체크포인트에서 memory-capture 실행
4. 결과 수집 및 보고

---

## Autonomous Loop Workflow

### Architecture Constraint

```
┌─────────────────────────────────────────────────────────────┐
│                      MANAGER (Main Session)                 │
│                                                             │
│   • 모든 서브에이전트를 직접 spawn/관리                       │
│   • 서브에이전트는 서브에이전트를 spawn 불가 (2단계 제한)      │
│   • 작업 규모 판단 → 분할 여부 결정                          │
└─────────────────────────────────────────────────────────────┘
         │
         │ spawn (직접 관리)
         ▼
┌─────────────────────────────────────────────────────────────┐
│                      SUBAGENTS (Flat)                       │
│                                                             │
│   worker ─── reviewer ─── verifier ─── analyzer             │
│                                                             │
└─────────────────────────────────────────────────────────────┘
```

### Main Loop

```
┌─────────────────┐
│   TASK INPUT    │
│  (spec, issue)  │
└────────┬────────┘
         │
         ▼
┌──────────────────────────────────────┐
│           1. ASSESS                  │
│                                      │
│  Q: 작업이 충분히 작은가?             │
│                                      │
│  YES → 단일 작업으로 진행             │
│  NO  → 커밋 단위로 분할              │
│        (subtask = 1 commit)          │
└──────────────────┬───────────────────┘
                   │
         ┌─────────┴─────────┐
         │                   │
         ▼                   ▼
   ┌───────────┐      ┌─────────────────────────┐
   │  SMALL    │      │         LARGE           │
   │  단일작업  │      │   Subtask 1 ← commit 1  │
   └─────┬─────┘      │   Subtask 2 ← commit 2  │
         │            │   Subtask N ← commit N  │
         │            └───────────┬─────────────┘
         │                        │
         └────────────┬───────────┘
                      │
                      ▼
┌─────────────────────────────────────────────────────────────┐
│                    2. EXECUTE (per subtask)                 │
│                                                             │
│   Manager spawns team for this subtask:                     │
│                                                             │
│   ┌──────────┐    ┌──────────┐    ┌──────────┐             │
│   │  WORKER  │───▶│ REVIEWER │───▶│ VERIFIER │             │
│   │ (code)   │    │ (review) │    │ (test+mem)│             │
│   └──────────┘    └──────────┘    └──────────┘             │
│                                                             │
│   • Worker: go-worker 또는 frontend-worker                  │
│   • Reviewer: 필요시 참여 (복잡한 변경)                      │
│   • Verifier: 테스트 + 메모리 검증                          │
│                                                             │
│   Large task → 여러 subtask 팀 병렬 가능:                   │
│   [Subtask1: worker→verifier] ∥ [Subtask2: worker→verifier] │
│                                                             │
└─────────────────────────┬───────────────────────────────────┘
                          │
                          ▼
┌─────────────────────────────────────────────────────────────┐
│                    3. VERIFY                                │
│                                                             │
│   ┌─────────────────────────────────────────┐               │
│   │ Verification Mode (configurable)        │               │
│   ├─────────────────────────────────────────┤               │
│   │ per-commit  │ 매 커밋마다 (현재 기본값)  │               │
│   │ per-branch  │ 브랜치 완료 시            │               │
│   │ on-demand   │ 사용자 요청 시            │               │
│   └─────────────────────────────────────────┘               │
│                                                             │
│   Verifier 실행:                                            │
│   • go test ./...                                           │
│   • npm test:run                                            │
│   • memory profile vs baseline                              │
│                                                             │
└─────────────────────────┬───────────────────────────────────┘
                          │
                   ┌──────┴──────┐
                   ▼             ▼
             ┌────────┐    ┌────────┐
             │  PASS  │    │  FAIL  │
             └───┬────┘    └───┬────┘
                 │             │
                 │             ▼
                 │      ┌─────────────┐
                 │      │  DIAGNOSE   │
                 │      │  (Manager)  │
                 │      └──────┬──────┘
                 │             │
                 │             │ retry < 3?
                 │             ├─── YES ──▶ (back to EXECUTE)
                 │             │
                 │             │ NO
                 │             ▼
                 │      ┌─────────────┐
                 │      │  ESCALATE   │──▶ User
                 │      └─────────────┘
                 │
                 ▼
┌─────────────────────────────────────────────────────────────┐
│                    4. COMMIT                                │
│                                                             │
│   • Stage changes                                           │
│   • Commit (1 subtask = 1 commit)                          │
│   • Update baseline (if memory improved)                    │
│                                                             │
└─────────────────────────┬───────────────────────────────────┘
                          │
                          ▼
                 ┌─────────────────┐
                 │ More subtasks?  │
                 └────────┬────────┘
                          │
                  YES     │     NO
                    ◀─────┴─────▶
                    │           │
           (next subtask)       ▼
                         ┌─────────────┐
                         │  FINALIZE   │
                         │  • Summary  │
                         │  • PR       │
                         └──────┬──────┘
                                │
                                ▼
                  ┌───────────────────────────┐
                  │  POST-ANALYSIS (선택)     │
                  │                           │
                  │  feature-dev:code-explorer│
                  │  • 변경사항 아키텍처 분석  │
                  │  • 영향 범위 문서화        │
                  │  • 기술 부채 식별          │
                  └───────────────────────────┘
```

### Task Sizing Decision

```
┌─────────────────────────────────────────────────────────────┐
│                    MANAGER의 판단 기준                       │
├─────────────────────────────────────────────────────────────┤
│                                                             │
│  충분히 작은 작업 (분할 안 함):                              │
│  • 단일 파일 또는 밀접한 2-3개 파일 수정                     │
│  • 하나의 논리적 변경 (single concern)                       │
│  • 예: 버그 수정, 작은 리팩토링, 단일 함수 추가              │
│                                                             │
│  분할이 필요한 작업:                                         │
│  • 여러 독립적 변경이 포함됨                                 │
│  • 각 변경이 독립적으로 테스트/커밋 가능                     │
│  • 예: 여러 모듈에 걸친 기능, 대규모 리팩토링                │
│                                                             │
│  분할 단위 = 커밋 단위 = Subtask                            │
│                                                             │
└─────────────────────────────────────────────────────────────┘
```

### Team Composition per Subtask

| Subtask 특성 | Worker | Reviewer | Verifier |
|-------------|--------|----------|----------|
| Go only | go-worker | (선택) | verifier |
| Frontend only | frontend-worker | (선택) | verifier |
| Go + Frontend | go-worker, frontend-worker | (선택) | verifier |
| 복잡한 로직 | worker | reviewer | verifier |
| 메모리 관련 | worker | - | verifier + memory-analyzer |

**Reviewer 참여 조건**: 복잡한 알고리즘, 아키텍처 변경, 동시성 코드

### Exit Conditions

- ✅ 모든 subtask 완료 + 검증 통과
- ⚠️ 재시도 3회 초과 → 사용자 에스컬레이션
- 🛑 사용자 중단 요청

---

## Subagent Coordination

### 커스텀 에이전트 (kattle:)

| Agent | 용도 | Skills | Tech Stack | Model |
|-------|------|--------|------------|-------|
| `kattle:go-worker` | Go 코드 수정 | go-quality | Go, client-go, informer, pprof | opus |
| `kattle:frontend-worker` | React/TS 수정 | react-quality | React, TypeScript, Wails, Vite, Tailwind, Radix | opus |
| `kattle:memory-analyzer` | 메모리 프로파일 분석 | memory-profiling, macos-memory | Go pprof, macOS leaks/heap/vmmap | opus |
| `kattle:verifier` | 테스트 + 메모리 검증 | test-runner, memory-capture | Go test, Vitest, pprof, Playwright | opus |
| `kattle:reviewer` | 프로젝트 규칙 리뷰 | go-quality, react-quality | Go, React, TypeScript, Wails | opus |

#### Agent Descriptions (에이전트 생성 시 사용)

```
kattle:go-worker
Modifies Go code in internal/kube and cmd packages for the kattle Kubernetes explorer, focusing on memory-efficient patterns using client-go informers, KeyOnlyStore, and FieldStore with strict adherence to project error handling and concurrency standards.

kattle:frontend-worker
Modifies React/TypeScript frontend code in cmd/gui/frontend for the kattle Wails desktop app, implementing virtualized tables, real-time Kubernetes resource updates via Wails IPC events, and Radix UI components with strict type safety and no type assertions.

kattle:memory-analyzer
Analyzes Go application memory profiles using pprof and macOS native tools (leaks, heap, vmmap) to identify memory leaks, excessive allocations, and goroutine leaks, comparing against baseline profiles and recommending specific optimization strategies.

kattle:verifier
Runs Go and frontend tests, captures memory profiles at specified checkpoints, compares heap/goroutine counts against baselines and thresholds, and uses Playwright to automate UI scenarios for memory observation during context switching and resource loading.

kattle:reviewer
Reviews code changes against kattle project conventions (CODE_QUALITY_GO.md, CODE_QUALITY.md), focusing on memory optimization patterns, proper error wrapping, safe concurrency, type safety, and architectural consistency with existing codebase patterns.
```

### 빌트인 에이전트 (subagent_type)

| subagent_type | 용도 | 워크플로 단계 |
|---------------|------|--------------|
| `Explore` | 빠른 코드 탐색 | ASSESS (파일/심볼 검색) |
| `feature-dev:code-reviewer` | 범용 코드 리뷰 | EXECUTE 후 (버그/보안 체크) |
| `feature-dev:code-explorer` | 깊은 아키텍처 분석 | POST-ANALYSIS (변경 영향 분석) |

### 리뷰어 사용 구분

| 상황 | 사용할 에이전트 |
|------|----------------|
| 메모리 최적화 관련 변경 | `kattle:reviewer` (프로젝트 규칙 적용) |
| 일반 버그/로직/보안 | `feature-dev:code-reviewer` (범용) |
| 복잡한 아키텍처 변경 | 둘 다 순차 실행 |

### Task 호출 방식

**커스텀 에이전트 호출**: `.claude/agents/` 디렉토리에 정의된 에이전트는 이름으로 호출:

```python
# 커스텀 에이전트 (kattle:*)
Task(subagent_type="kattle:go-worker", prompt="...")

# 빌트인 에이전트
Task(subagent_type="Explore", prompt="...")
Task(subagent_type="feature-dev:code-reviewer", prompt="...")
```

**주의**: 커스텀 에이전트가 인식되지 않으면 `general-purpose`로 폴백되며, 에이전트 본문의 지침이 적용되지 않을 수 있음. 이 경우 prompt에 필요한 컨텍스트를 직접 포함.

### Manager Spawn Examples

```python
# 단일 작업 (small task)
Task(subagent_type="kattle:go-worker", prompt="Fix nil check in client.go:142")
Task(subagent_type="kattle:verifier", prompt="Run tests and verify memory")

# 복잡한 단일 작업 (with review)
Task(subagent_type="kattle:go-worker", prompt="Implement new caching logic")
Task(subagent_type="kattle:reviewer", prompt="Review the caching implementation")
Task(subagent_type="kattle:verifier", prompt="Run tests and memory check")

# 대규모 작업 - Subtask 병렬 실행 (단일 메시지에서)
[
  Task(subagent_type="kattle:go-worker", prompt="Subtask 1: Refactor store interface"),
  Task(subagent_type="kattle:frontend-worker", prompt="Subtask 2: Update UI bindings"),
]
# (완료 후 각각 verify)
Task(subagent_type="kattle:verifier", prompt="Verify subtask 1")
Task(subagent_type="kattle:verifier", prompt="Verify subtask 2")
```

---

## Key Files

- `planning/*.spec.md` - 기능 스펙 문서
- `CODE_QUALITY_GO.md` - Go 코드 품질 가이드라인
- `cmd/gui/frontend/CODE_QUALITY.md` - React 코드 품질 가이드라인

## Commands

```bash
# Build & Run
wails dev                           # Development mode (GUI)
go run ./cmd/kupid                  # TUI mode

# Test
go test ./...                       # Go tests
npm --prefix cmd/gui/frontend test:run  # Frontend tests

# Profile
curl -o heap.pb.gz http://localhost:6060/debug/pprof/heap
go tool pprof -top heap.pb.gz
go tool pprof -diff_base=baseline_heap.pb.gz heap.pb.gz
```
