# Manual Test Checklist — SQLite Pull Model PR

> `feat/sqlite-pull-model` branch
> 두 모드 모두 테스트 필요: **Regular** (`KATTLE_USE_SQLSTORE` 미설정) / **Windowed** (`KATTLE_USE_SQLSTORE=1`)

---

## 1. Regular Mode 회귀 (기존 동작 보존)

| # | 시나리오 | 확인 사항 |
|---|---------|----------|
| 1.1 | 앱 시작 (플래그 없이) | 기존과 동일하게 동작, 콘솔에 SQLStore 관련 로그 없음 |
| 1.2 | GVK 선택 → 리소스 로딩 | Pods, Deployments 등 정상 로딩, 스피너 → 데이터 표시 |
| 1.3 | 실시간 업데이트 | Pod 삭제/생성 시 셀 플래시 애니메이션 동작 |
| 1.4 | 필드 트리에서 필드 드래그 추가 | 컬럼 추가 시 watch 재시작 없이 데이터 갱신 |
| 1.5 | 필드 호버 프리뷰 | 미선택 필드에 마우스 올리면 muted 프리뷰 컬럼 표시 |
| 1.6 | 글로벌 검색 (toolbar) | 클라이언트 사이드 fuzzy 필터 정상 동작 |
| 1.7 | 컬럼 정렬 (헤더 클릭) | 클라이언트 사이드 정렬 동작 |
| 1.8 | GVK 전환 | 데이터 클리어 → 새 GVK 로딩, 이전 GVK 잔류 데이터 없음 |
| 1.9 | Context 전환 | 다른 클러스터 선택 시 정상 전환 |

## 2. Windowed Mode 핵심 기능

| # | 시나리오 | 확인 사항 |
|---|---------|----------|
| 2.1 | 앱 시작 (`KATTLE_USE_SQLSTORE=1`) | 콘솔에 `SQLStore initialized` 로그, `Windowed mode enabled` 로그 |
| 2.2 | 리소스 로딩 (1K+ 리소스) | 스피너 → totalCount 표시, 스크롤바가 전체 개수 반영 |
| 2.3 | 가상 스크롤 | 빠르게 스크롤 시 skeleton → 실제 데이터 전환, 끊김 없음 |
| 2.4 | 서버사이드 정렬 | 헤더 클릭 → SQLite ORDER BY 정렬, 대량 데이터도 즉시 반응 |
| 2.5 | 서버사이드 검색 | 검색어 입력 → 200ms 디바운스 → totalCount 변경, 결과 정확 |
| 2.6 | 실시간 업데이트 | Pod 변경 시 `resource:update` 이벤트 → 해당 행 갱신 + 셀 플래시 |
| 2.7 | 리소스 삭제 반영 | 외부에서 리소스 삭제 → totalCount 감소, 행 사라짐 |

## 3. 비동기 필드 추출 (Async Field Extraction)

| # | 시나리오 | 확인 사항 |
|---|---------|----------|
| 3.1 | 새 필드 추가 (cache miss) | 필드 추가 시 skeleton 표시 → `fields:ready` 수신 → 실제 값 표시 |
| 3.2 | 기존 필드 재선택 (cache hit) | skeleton 없이 즉시 값 표시 |
| 3.3 | null vs undefined 구분 | 값이 없는 필드 = "-" 표시 (null), 미추출 필드 = skeleton (undefined) |
| 3.4 | 필드 제거 후 재추가 | 정상 동작, 이전 캐시 재활용 |

## 4. Watch 생명주기

| # | 시나리오 | 확인 사항 |
|---|---------|----------|
| 4.1 | 초기 싱크 | `watch:status` syncing → ready 전환, count 일치 |
| 4.2 | GVK 전환 시 StopWatch → StartWatch | 이전 watch 정리 후 새 watch 시작, 데이터 혼재 없음 |
| 4.3 | 빠른 GVK 연속 전환 | race condition 없음 (watchGen 체크), 최종 선택 GVK만 표시 |
| 4.4 | SetSelectedFields → resync 필요 시 | Windowed: watch 재시작, Regular: watch 유지 |
| 4.5 | eventWg race 없음 | 빠른 StartWatch/StopWatch 반복 시 panic 없음 |

## 5. SQLite / DB 관리

| # | 시나리오 | 확인 사항 |
|---|---------|----------|
| 5.1 | DB 파일 생성 | `/tmp/kattle-<PID>.db` 파일 생성 확인 |
| 5.2 | 정상 종료 시 정리 | 앱 종료 후 `.db`, `-wal`, `-shm` 파일 모두 삭제됨 |
| 5.3 | 비정상 종료 후 재시작 | 이전 크래시의 orphan DB 파일 정리됨 (다른 PID의 파일) |
| 5.4 | Context별 데이터 분리 | `DeleteByContext` 후 해당 context 데이터만 삭제 |
| 5.5 | FTS5 검색 | 이름/네임스페이스 기반 전문 검색 동작 (FTS 미지원 빌드에서는 LIKE fallback) |

## 6. 메모리 / 성능

| # | 시나리오 | 확인 사항 |
|---|---------|----------|
| 6.1 | Windowed 모드 WebView 메모리 | 12K 리소스 로딩 후 WebView 메모리 ~5MB 수준 유지 |
| 6.2 | KeyOnlyStore 메모리 | Go 힙에 key만 저장, 전체 오브젝트 없음 (pprof 확인) |
| 6.3 | GVK 전환 후 메모리 해제 | 이전 GVK 데이터 SQLite에서 삭제, `debug.FreeOSMemory()` 호출 확인 |
| 6.4 | 장시간 watch | 30분+ watch 시 메모리 누수 없음 (점진적 증가 없음) |

## 7. UI / UX 엣지 케이스

| # | 시나리오 | 확인 사항 |
|---|---------|----------|
| 7.1 | 빈 결과 | 리소스 0개인 GVK 선택 → "No resources found" (로딩 완료 후에만) |
| 7.2 | 검색 결과 없음 | 검색어 입력 → "No matches for ..." 메시지 |
| 7.3 | 로딩 중 "No resources" 플래시 방지 | 초기 싱크 완료 전에는 스피너, 빈 상태 미표시 |
| 7.4 | 컬럼 드래그 리오더 | Windowed 모드에서도 컬럼 순서 변경 정상 동작 |
| 7.5 | 컬럼 리사이즈 | Windowed 모드에서 컬럼 너비 조절 정상 |
| 7.6 | CSV 내보내기 | 현재 보이는 데이터 기반 내보내기 동작 |
| 7.7 | 프리뷰 필드 호버 → 빠른 이동 | 200ms 디바운스로 불필요한 요청 없음, watch 재시작 루프 없음 |
| 7.8 | Skeleton → 데이터 전환 | 스켈레톤이 깜빡이지 않고 부드럽게 실제 데이터로 전환 |

## 8. 크로스 모드 전환 (재시작 필요)

| # | 시나리오 | 확인 사항 |
|---|---------|----------|
| 8.1 | Regular → Windowed | 환경변수 설정 후 재시작, windowed 모드 정상 진입 |
| 8.2 | Windowed → Regular | 환경변수 해제 후 재시작, 기존 모드로 복귀, SQLite 잔여 파일 없음 |

---

**실행 방법:**
```bash
# Regular mode
wails dev

# Windowed mode
KATTLE_USE_SQLSTORE=1 wails dev

# 메모리 프로파일 (선택)
KATTLE_USE_SQLSTORE=1 KATTLE_DEBUG=1 wails dev
curl -o heap.pb.gz http://localhost:6060/debug/pprof/heap
go tool pprof -top heap.pb.gz
```
