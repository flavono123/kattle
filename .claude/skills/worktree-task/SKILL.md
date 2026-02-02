---
name: worktree-task
description: Create a git worktree for a new TASK. Use when starting a new feature, fix, or refactor task that requires isolated development.
user-invocable: true
allowed-tools: Bash(git *)
---

# Worktree Task Creation

TASK 단위로 git worktree를 생성하여 격리된 개발 환경을 구성합니다.

## Context

- Current worktrees: !`git worktree list`
- Current branch: !`git branch --show-current`
- Repository root: !`git rev-parse --show-toplevel`

## Your Task

사용자가 제공한 TASK 정보를 바탕으로 worktree를 생성합니다.

### 입력 파싱

사용자 입력에서 다음을 추출:
- **task-name**: 짧은 식별자 (예: task-a, fix-memory, refactor-ui)
- **task-type**: feature, fix, refactor, chore 중 하나
- **description**: 브랜치 이름에 포함할 설명 (선택적)

### 명명 규칙

```
Worktree 경로: /Users/hansuk.hong/P/kattle-<task-name>
브랜치 이름: <task-type>/<task-name>-<description>
```

### 실행 단계

1. **Worktree 목록 확인** - 중복 방지
2. **Worktree 생성** - 새 브랜치와 함께
3. **결과 보고** - 생성된 경로 및 브랜치

### 명령어 템플릿

```bash
# 1. 중복 확인
git worktree list | grep -q "kattle-<task-name>" && echo "Already exists" && exit 1

# 2. Worktree 생성
git worktree add \
  /Users/hansuk.hong/P/kattle-<task-name> \
  -b <task-type>/<task-name>-<description>

# 3. 확인
git worktree list
```

## Examples

### Feature 작업
```
입력: /worktree-task feature task-d add-column-selector
결과:
  - 경로: /Users/hansuk.hong/P/kattle-task-d
  - 브랜치: feature/task-d-add-column-selector
```

### Bug Fix
```
입력: /worktree-task fix issue-456 null-pointer-in-watch
결과:
  - 경로: /Users/hansuk.hong/P/kattle-fix-issue-456
  - 브랜치: fix/issue-456-null-pointer-in-watch
```

### Refactor
```
입력: /worktree-task refactor cleanup remove-dead-code
결과:
  - 경로: /Users/hansuk.hong/P/kattle-cleanup
  - 브랜치: refactor/cleanup-remove-dead-code
```

## Output Format

```
Worktree created:
  Path:   /Users/hansuk.hong/P/kattle-<task-name>
  Branch: <task-type>/<task-name>-<description>

To work in this worktree:
  - Open in IDE: code /Users/hansuk.hong/P/kattle-<task-name>
  - Run commands: git -C /Users/hansuk.hong/P/kattle-<task-name> status
```
