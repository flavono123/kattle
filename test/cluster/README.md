# Local Test Cluster for Manual Testing

`feat/sqlite-pull-model` PR의 수동 테스트를 위한 로컬 Kind 클러스터 구성.

## Prerequisites

- [Kind](https://kind.sigs.k8s.io/) (`brew install kind`)
- [kubectl](https://kubernetes.io/docs/tasks/tools/)

## Setup

```bash
bash test/cluster/setup.sh
```

멱등 — 이미 클러스터가 있으면 매니페스트만 재적용.

## Cleanup

```bash
kind delete cluster --name kattle-test
```

## Test Resources

| Resource | Namespace | Purpose |
|----------|-----------|---------|
| Namespace `kattle-test` | — | Test isolation |
| Deployment `web` (3 replicas) | kattle-test | Realtime update (rollout), scale |
| Deployment `api` (2 replicas) | kattle-test | Realtime update target |
| Pod `standalone-a` ~ `d` (4) | kattle-test | Individual delete test |
| Pod `multi-container` | kattle-test | Multi-container field tree |
| Pod `rich-metadata` | kattle-test | Diverse labels/annotations |
| ConfigMap x2 | kattle-test | Non-Pod GVK test |
| Service `web-svc` | kattle-test | Non-Pod GVK test |

## Test Scenarios

Kattle 실행:

```bash
KATTLE_USE_SQLSTORE=1 wails dev
```

UI에서 context `kind-kattle-test` 선택 → Pods GVK 선택.

### 2.6 Realtime Update (Pod 변경 → Cell Flash)

```bash
# Trigger rollout — nginx image tag 변경
kubectl --context kind-kattle-test -n kattle-test \
  set image deploy/web nginx=nginx:1.27-bookworm

# Scale up
kubectl --context kind-kattle-test -n kattle-test \
  scale deploy/web --replicas=5

# Reset
kubectl --context kind-kattle-test -n kattle-test \
  set image deploy/web nginx=nginx:1.27-alpine
kubectl --context kind-kattle-test -n kattle-test \
  scale deploy/web --replicas=3
```

**Expected**: Pod list에 신규 Pod 나타나고, 변경된 셀에 flash animation.

### 2.7 Resource Delete → totalCount 감소

```bash
# Delete a standalone pod
kubectl --context kind-kattle-test -n kattle-test \
  delete pod standalone-a

# Verify count
kubectl --context kind-kattle-test -n kattle-test \
  get pods --no-headers | wc -l
```

**Expected**: 테이블에서 해당 행 사라지고, 스크롤바/totalCount 1 감소.

### 3.1 New Field (cache miss → skeleton → value)

1. UI에서 필드 트리 열기
2. `spec.containers.*.resources.limits.memory` 같은 아직 선택하지 않은 필드 추가
3. **Expected**: 컬럼 추가 시 skeleton 표시 → `fields:ready` 이벤트 후 값 표시

### 3.2 Existing Field Re-select (cache hit)

1. `metadata.name` 필드 선택 해제
2. 다시 선택
3. **Expected**: skeleton 없이 즉시 값 표시 (cache hit)

### 3.4 Field Remove + Re-add (cache reuse)

1. 임의 필드 체크 해제 (컬럼 제거)
2. 다시 체크 (컬럼 재추가)
3. **Expected**: cache에 데이터가 남아 있으므로 즉시 표시

## SQLite DB Inspection

Kattle 실행 중 DB 파일 위치: `/tmp/kattle-*.db` (WAL mode — 읽기 안전)

```bash
# Find DB file
ls -la /tmp/kattle-*.db

# CLI (macOS built-in)
sqlite3 /tmp/kattle-*.db ".tables"
sqlite3 /tmp/kattle-*.db "SELECT key, json_extract(data, '$.metadata.name') FROM resources LIMIT 10;"
```

### GUI Tools

| Tool | Install |
|------|---------|
| DB Browser for SQLite | `brew install --cask db-browser-for-sqlite` |
| Beekeeper Studio | `brew install --cask beekeeper-studio` |
| SQLite Viewer (VSCode) | Extension: `alexcvzz.vscode-sqlite` |
