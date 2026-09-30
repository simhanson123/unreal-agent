# Windows Native Harness 개발 기록

## 범위와 기준선

- 계획 기준일: 2026-09-29.
- 포크: `simhanson123/unreal-agent`.
- 변경 전 기준 커밋: `1b9f778` (`Avoid lost wakeup in process output drain test`).
- 원본 MIT 라이선스와 Go module 경로를 유지한다.
- 이번 변경은 Phase 0 조사 및 Phase 1/2의 첫 Windows 실행 기반이다.
- 전체 MVP, 보안 sandbox, provider 확장, Unreal/Blender 통합 완료를 의미하지 않는다.

## 구조 지도

```text
cmd/unreal-agent-runner
  -> cmd/internal/agentrunner
  -> coordinator
     -> inbox / sessionstore/localfile
     -> contextbuilder
     -> llm/responsesapi -> provider clients
     -> tool registry -> translator
     -> operation.LocalOperationManager
        -> serializable Shell operation
        -> primitives
           -> shared event / pipe / output lifecycle
           -> Unix process group backend
           -> Windows Job Object backend
```

유지해야 할 계약:

1. Tool translator는 동기 검증과 Operation 제출만 수행하며 I/O를 수행하지 않는다.
2. 세션은 append-only history를 유지한다. 기존 저장 형식과 Shell operation version 3을 변경하지 않는다.
3. 프로세스 시작, 출력, 종료 및 취소는 기존 primitive event로 전달한다.
4. stdout/stderr offset은 스트림별 연속적이며, 정상 종료 시 버퍼의 출력을 보존한다.
5. Shell 종료 또는 작업 취소 시 그 작업의 자식 프로세스를 정리한다.
6. 생성 작업의 재실행은 기존 파일 내용을 덮어쓰지 않는다.
7. 프로세스 실행 도중 중단된 세션은 명령을 임의로 재실행하지 않는다. 기존 unknown-outcome 실패 정책을 유지한다.
8. Job handle 등 OS 자원은 세션에 직렬화하지 않는다.

## 플랫폼 의존성 조사

| 영역 | 변경 전 | 이번 처리 |
| --- | --- | --- |
| 프로세스 시작 | 공통 파일에 `Setpgid` | 플랫폼별 설정 및 시작 함수 |
| 종료/자식 정리 | `kill(-pgid)` | Unix 동작 유지, Windows Job Object |
| 실행 전 자식 추적 | Unix process group | Windows suspended start, job 할당 후 thread resume |
| 출력 파일 | `unix.Open`, `O_NOFOLLOW`, `Ftruncate` | Unix no-follow 유지, Windows reparse-point handle 검사 |
| 파이프 drain | Unix nonblocking read | Unix 유지, Windows job 종료 후 EOF까지 읽기 |
| 파일 생성 | 비-Unix는 unsupported | Windows `os.Root` 기반 생성 및 기존 항목 검사 |
| 세션 publish | rename + directory fsync | Windows write-through rename, 파일 flush 유지 |
| 기본 shell | `$SHELL`, `/bin/sh` | Windows `pwsh`, `HARNESS_SHELL` 명시적 override |
| 모델 tool 이름 | `Bash` | Windows에서 `Shell`, 기존 `Bash` 등록 계약 유지 |
| CI | Linux/macOS 전체 테스트 | Windows production build + 공통 host contract 추가 |

## Windows 실행

Go 1.27 이상과 PowerShell 7이 필요하다. Git Bash나 WSL은 필수가 아니다.

```powershell
go build -trimpath -o bin/unreal-agent-runner.exe ./cmd/unreal-agent-runner
go test -race -count=1 -timeout=3m ./harness/hosttest ./cmd/hosttest
.\bin\unreal-agent-runner.exe -h
```

기존 API provider를 사용할 때는 해당 API key를 환경 변수로 설정한다.
실제 모델 호출은 비용과 프로젝트 수정이 발생할 수 있다.

```powershell
$env:OPENAI_API_KEY = "<your API key>"
.\bin\unreal-agent-runner.exe -workspace D:\Projects\Example -p "프로젝트 구조를 설명해 줘."
```

인증정보는 소스나 문서에 저장하지 않는다.

PowerShell은 profile을 로드하지 않고 non-interactive 모드로 실행한다.
명령은 UTF-16LE `EncodedCommand`로 전달하며 스트림 인코딩은 UTF-8로 설정한다.
실행 정책을 강제로 우회하지 않는다.

## 검증 정책

- `harness/hosttest`: Windows/Linux/macOS에서 같은 public API 계약을 실행한다.
- 기존 Unix process/FIFO/signal 테스트는 유지한다.
- Windows에서는 기존 전체 테스트 스위트가 아직 이식되지 않았다. 일부 테스트 파일 자체가 Unix 전용 API와 `/bin/...` 경로에 의존한다.
- Windows CI의 제한된 범위를 전체 플랫폼 지원 완료로 해석하지 않는다.
- 교차 컴파일은 해당 OS에서의 런타임 검증을 대신하지 않는다.
- symlink 테스트는 생성 권한이 없으면 명시적으로 skip한다.
- 실제 LLM 로그인, API 호출, UNC 공유, Windows ARM64 실기기, macOS 실기기 검증은 별도 단계다.

### 이번 로컬 검증 결과

| 검증 | 결과 |
| --- | --- |
| 원본 `1b9f778` Windows build | 실패: `syscall.Kill`, `Setpgid`, `unix.Open` 등 |
| 수정본 Windows x64 전체 production build | 통과 |
| Windows ARM64 교차 build | 통과 |
| Linux x64 전체 production build 및 전체 `go vet` | 통과 |
| macOS ARM64 전체 production build 및 전체 `go vet` | 통과 |
| Windows 공통 host 및 runner 계약 테스트 | 통과 |
| Windows host/runner `-race -count=3` | 통과 |
| Windows 이식 가능 회귀 테스트 포함 13개 패키지 | 통과 |
| Windows executable `-h` | 통과 |
| symlink 생성이 필요한 Windows 테스트 | 로컬 권한 부족으로 skip |
| Linux/macOS 실제 테스트 실행 | 로컬 실행 환경 부재로 미실행, CI에 유지 |

검증에는 Go 1.27.1 공식 Windows 배포본을 SHA-256 확인 후 사용했다.
Windows 실제 실행 검증은 `harness/hosttest`와 `cmd/hosttest`에 있으며,
stdout/stderr, stdin, 대용량 출력, 환경/작업 디렉터리, 종료 코드, 취소,
정상 종료 시 자식 정리, 호스트 비정상 종료 시 Job 정리, Unicode/긴 경로,
예약 경로 거부, Shell operation, 세션 재개 및 손상된 마지막 레코드 복구,
tool alias를 통한 실행 금지 우회 방지를 포함한다.

회귀 테스트에서 발견한 세션 append handle의 truncate 권한 문제,
Windows `DirEntry.Info` 캐시로 삭제된 세션이 남는 문제,
checkout CRLF로 embedded prompt가 달라지는 문제도 수정했다.

## 남은 제약

- Windows 범용 프로세스에 POSIX `SIGTERM`과 동등한 graceful 종료는 없다. 현재 취소는 Job Object 강제 종료를 사용한다. stdin EOF는 지원한다.
- Windows directory fsync는 POSIX와 동등하지 않다. 파일 flush와 write-through publish를 사용하되, 전원 장애 내구성의 완전한 동등성은 주장하지 않는다.
- Workspace sandbox와 NTFS ACL 정책은 미구현이다. Job Object는 프로세스 수명 관리 장치이지 보안 sandbox가 아니다.
- 최종 출력 파일의 reparse point와 ADS/device 경로를 거부하지만, 모든 도구의 workspace escape 방지는 후속 보안 단계다.
- Shell 프로세스 환경은 기존처럼 상속한다. secret filtering과 최소 환경 전달은 별도 작업이다.
- 일반 background operation은 Shell이 살아 있는 동안 실행된다. Shell 종료 후 자식만 남기는 detached daemon은 허용하지 않는다.
- Release workflow는 원본과 포크의 버전 태그에서 Linux/macOS archive 및 Windows AMD64 ZIP과 SHA256SUMS를 게시한다. 포크에서는 Docker 게시가 건너뛰어지며 Windows 테스트 성공이 필수다. `main` CI artifact는 GitHub Release가 아니며 태그를 게시해야 다운로드 가능하다. MSI/winget installer는 제공하지 않는다.
- Windows `v0.2.1` 프리뷰 릴리스는 기존 커밋 `924a022`로 게시했다. 이 릴리스에는 Windows ZIP 및 SHA256SUMS만 포함된다. 이후 버전 태그는 위 워크플로를 통해 자동 배포한다.
- 포크 저장소에서 GitHub push 이벤트가 워크플로를 시작하지 않는 현상을 확인했다. `workflow_dispatch`(수동 트리거)는 정상 동작하므로, Release 워크플로에 `workflow_dispatch` 트리거를 추가하고 버전 태그 ref에서 수동 실행하는 방식으로 배포한다. 원인이 해소되면 태그 push만으로 자동 실행된다. Settings → Actions에서 저장소 정책 확인이 필요하다.
- macOS CI에서 임시 디렉터리가 `/var` → `/private/var` 심볼릭 링크로 해석되어 환경 변수 계약 테스트가 실패했다. 경로 비교를 심볼릭 링크 해석 후 수행하도록 수정했다.
- 기존 `openaicodex` credential-file backend는 POSIX 파일 권한과 HOME 전제가 있어 Windows 인증 테스트가 아직 실패한다. 보안 검사를 무력화하지 않았으며, 현재 구현을 Codex login 지원 완료로 표시하지 않는다.
- 이식 가능한 세션을 읽는 것과 다른 OS에서 기존 경로를 사용하는 작업을 재개하는 것은 다르다. cross-OS 작업 경로 재매핑은 미구현이다.
- CMD compatibility와 Windows graceful console-control 종료는 아직 미구현이다.

## 후속 개발 순서

1. Windows 전체 기존 테스트 이식, graceful 종료 capability 및 junction/UNC 검증 강화.
2. Read/Search/Write/ApplyPatch와 Git diff 도구 추가. stale version 및 workspace boundary 검사 포함.
3. Provider와 인증 capability 분리, 공식 Codex SDK 로그인 통합 검증.
4. OpenAI API와 Anthropic API를 독립 backend로 정리.
5. Kimi 지원: `MOONSHOT_API_KEY` 기반 Moonshot API와 Kimi Code Console API key를 별도 인증/endpoint 설정으로 취급한다. 공식 문서로 protocol 및 tool capability를 확인한 후 adapter를 구현한다.
6. Claude Agent SDK는 공식 지원 인증만 사용한다. Claude Code credential 접근, identity 위조, undocumented OAuth는 구현하지 않는다.
7. Context/session 강화, permission/sandbox, TUI, MCP/skills 고도화.
8. Unreal bridge, Blender, vision feedback, 선택적 subagent, IDE/GUI.

Provider의 인증 방식 때문에 coordinator, tool history, permissions, operation lifecycle을 교체하지 않는다는 원칙을 유지한다.
