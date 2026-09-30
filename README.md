# unreal-agent(+Windows)

An async-first agent harness from Unreal Labs, with native Windows support and a
Claude Code plugin. Fork of
[unreallabsai/unreal-agent](https://github.com/unreallabsai/unreal-agent).

- [harness/](harness/) — the library.
- [cmd/](cmd/) — executables that use the library.
- [benchmarks/](benchmarks/) — benchmark runners.
- [claude-code/](claude-code/) — Claude Code plugin that delegates tasks to the runner.

## Claude Code

Claude Code subscription credentials cannot be used by third-party software, so the
harness does not call Claude through Claude Code. Instead, Claude Code orchestrates and
delegates to the harness, which uses its own provider credentials:

```text
/plugin marketplace add simhanson123/unreal-agent-plus-windows
/plugin install unreal-agent-windows@unreal-agent-plus-windows
```

See [claude-code/README.md](claude-code/README.md).

## Windows native preview

This fork adds a native Windows process backend using Job Objects, PowerShell 7
execution, and session persistence support. PowerShell 7 is required at runtime;
Git Bash, WSL, and Go are not required to run a downloaded release.

Download `unreal-agent-runner_<version>_windows_amd64.zip` from the latest
[release](https://github.com/simhanson123/unreal-agent-plus-windows/releases) (for example
[`v0.2.4`](https://github.com/simhanson123/unreal-agent-plus-windows/releases/tag/v0.2.4)).
Extract it and run the binary in PowerShell:

```powershell
Expand-Archive .\unreal-agent-runner_0.2.4_windows_amd64.zip -DestinationPath .\unreal-agent
$env:OPENAI_API_KEY = "<your API key>"
.\unreal-agent\unreal-agent-runner.exe -workspace . -p "Summarize this project."
```

The archive includes `LICENSE`. Verify the ZIP against the release's
`SHA256SUMS` before use: `(Get-FileHash .\unreal-agent-runner_0.2.4_windows_amd64.zip -Algorithm SHA256).Hash`
must match its entry.

To build from source, install Go 1.27+ and run:

```powershell
go build -trimpath -o bin/unreal-agent-runner.exe ./cmd/unreal-agent-runner
go test -race ./harness/hosttest ./cmd/hosttest
.\bin\unreal-agent-runner.exe -h
```

To use a Codex (ChatGPT) subscription, sign in once with the official Codex CLI and
select the `openai-codex` provider. The runner reuses Codex's login and asks
`codex app-server` to refresh it when needed:

```powershell
codex login
$env:UNREAL_HARNESS_LLM_PROVIDER = "openai-codex"
$env:UNREAL_HARNESS_LLM_MODEL = "<a Codex model, e.g. the model in ~/.codex/config.toml>"
.\unreal-agent\unreal-agent-runner.exe -workspace . -p "Summarize this project."
```

This is an initial runtime port, not a completed sandbox or coding-agent MVP.
Anthropic and Kimi integration remain follow-up work.
See [Windows port status and roadmap](WINDOWS_PORT.md) for verified scope,
known limitations, and the implementation sequence.

## Glossary

- **Input**: an event with a caller-supplied globally unique ID that remains
  stable across redeliveries.
- **Inbox**: session-scoped, in-memory deduplication of external, control, and
  crash inputs.
- **Session**: append-only persisted history that can be forked.
- **LLM turn**: the coordinator-managed sequence around one logical LLM request.
- **Tool**: a capability described by a schema and bound to a translator.
- **Tool call**: a model-produced request to use a tool.
- **Tool translator**: validates a tool call and translates it into one or more
  operations. It runs synchronously on the coordinator's event loop and must not
  perform I/O or suspend the loop.
- **Tool call status**: the translation outcome: a validation error or references
  to submitted operations. Operation execution state is tracked separately;
  the translator formats these into a model-facing result.
- **Operation**: a serializable description of work produced by a tool translator
  for asynchronous execution. Implementations are encouraged to use the available
  [primitives](harness/primitives/).

## Components

| Component | Responsibility |
| --- | --- |
| Session inbox | Volatile, session-scoped input idempotency. |
| Coordinator | Persist accepted inputs, run LLM turns, resolve tool translators through the registry, and dispatch committed operations. |
| Session store | Persist canonical session history and operation state; support recovery and forks; atomically record tool-call status with operations. |
| Context builder | Statefully assemble model input in memory. Return the model input together with a record of anything omitted, truncated, or compacted. Perform no I/O and accept no persistence dependencies. |
| LLM Adapter | Send prepared model input to a provider and return a normalized completed response. Own authentication, cancellation, and provider errors. |
| Tool registry | Own the fixed Shell, legacy Bash, ViewImage, and skill-use definitions and their translators; expose the host-selected set. |
| Tool translator | Validate a tool call and produce its status and operations. Format a recorded call status and prepared operation output into model results. Perform no I/O. |
| Operation manager | Actor runtime for durable operations. The local implementation is swappable. |

## Extending the harness

Harness components are composable, and alternative implementations of their interfaces are encouraged.

We intend to preserve these invariants:

- Session-store items are serializable, and the storage format is versioned.
- We'll do our best to maintain backwards compatibility for sessions.
  An unsupported session version will always cause an explicit error on resume.
- Operations are versioned and always serializable.

For example, a proxy operations manager can send serialized operations to a
local operations manager running in a process inside a remote sandbox, allowing
tools to execute there.
