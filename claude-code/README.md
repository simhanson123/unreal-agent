# Claude Code plugin: unreal-agent(+Windows)

This plugin adds the `unreal-agent-windows` skill to Claude Code. Claude Code stays the
orchestrator and delegates tasks to `unreal-agent-runner`, which runs its own agent loop with a
non-Claude provider and native shell execution on Windows (PowerShell 7, Job Objects), Linux,
and macOS.

## Why the direction is inverted

Claude Code subscription credentials are for Claude Code itself; third-party software must not
reuse them. So the harness does not call Claude through Claude Code. Instead, Claude Code calls
the harness:

```text
User -> Claude Code (Claude, your subscription)
          -> unreal-agent-windows skill
               -> unreal-agent-runner (OpenAI / OpenRouter / Fireworks / Ollama / Codex token)
                    -> native shell in the workspace
```

The skill scripts remove Claude Code token variables (`CLAUDE_CODE_OAUTH_TOKEN`,
`ANTHROPIC_AUTH_TOKEN`, `CLAUDE_CODE_*TOKEN*/*SECRET*/*KEY*`) from the runner environment, so
neither the harness nor the commands its model runs can see them.

## Install

Requirements: PowerShell 7 (`pwsh`) on every OS.

In Claude Code:

```text
/plugin marketplace add simhanson123/unreal-agent-plus-windows
/plugin install unreal-agent-windows@unreal-agent-plus-windows
```

Or copy `skills/unreal-agent-windows` to `~/.claude/skills/` (personal) or
`<project>/.claude/skills/` (project).

The runner is found through `UNREAL_AGENT_RUNNER`, then `PATH`, then the per-user install
directory. To install the latest release with checksum verification:

```powershell
pwsh -NoProfile -File skills/unreal-agent-windows/scripts/Install-UnrealAgentRunner.ps1
```

It installs to `%LOCALAPPDATA%\unreal-agent-plus-windows\bin\<version>` on Windows and
`${XDG_DATA_HOME:-~/.local/share}/unreal-agent-plus-windows/bin/<version>` elsewhere.

## Use

Ask Claude Code, for example: "Have unreal-agent with OpenRouter review this module and report
back", or invoke `/unreal-agent-windows`. The scripts can also be run directly:

```powershell
$skill = "skills/unreal-agent-windows/scripts"
pwsh -NoProfile -File $skill/Invoke-UnrealAgent.ps1 -Check -Provider ollama -Model qwen3-coder
pwsh -NoProfile -File $skill/Invoke-UnrealAgent.ps1 -Workspace D:\Projects\Game -PromptFile task.md -Provider openai
pwsh -NoProfile -File $skill/Invoke-UnrealAgent.ps1 -Workspace D:\Projects\Game -SessionId <id> -Prompt "Now add tests."
pwsh -NoProfile -File $skill/Get-UnrealAgentRun.ps1 -List
```

Each run is stored in `<state>/unreal-agent/claude-code-runs/<timestamp>-<session>/` with
`request.json`, `output.jsonl`, `stderr.log`, and `run.json`.

## Limits

- The harness is not a security sandbox; the delegate runs commands with your privileges.
- Anthropic and Kimi/Moonshot providers are not implemented in the runner yet.
- The `openai-codex` auth-file backend is not Windows-ready; use `OPENAI_CODEX_ACCESS_TOKEN`
  or the official `codex exec` CLI.
