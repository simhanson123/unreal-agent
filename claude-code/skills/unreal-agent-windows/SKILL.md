---
name: unreal-agent-windows
description: Delegate a task to the unreal-agent(+Windows) harness, which runs its own agent loop with a non-Claude provider (OpenAI API, OpenRouter, Fireworks, local Ollama, or the user's Codex/ChatGPT subscription via `codex login`) and native Windows/Linux/macOS shell execution. Use when the user asks to run unreal-agent or unreal-agent-runner, delegate work or get a second opinion from Codex/GPT/OpenRouter/Ollama/another model, resume or inspect a harness session or run log, install the runner, or use a workspace's .harness/skills.
---

# unreal-agent(+Windows)

Claude Code is the orchestrator here. The harness never receives Claude Code credentials:
it authenticates only with its own provider credentials, and the scripts remove Claude Code
token variables from the runner environment. Never read, copy, or pass Claude Code
credentials to the harness, and never configure the harness to call Anthropic on the
user's Claude Code subscription.

All scripts are in `scripts/` next to this file and need PowerShell 7 (`pwsh`) on every OS.
Resolve `scripts/...` against this skill's base directory and run them with
`pwsh -NoProfile -File <absolute path> ...`.

## 1. Preflight

```
pwsh -NoProfile -File <skill>/scripts/Invoke-UnrealAgent.ps1 -Check -Workspace <dir> [-Provider <name>] [-Model <id>]
```

Resolve every `MISSING` line before running:

- Runner missing: run `scripts/Install-UnrealAgentRunner.ps1` (downloads the latest release
  from GitHub and verifies SHA256SUMS; `-Version vX.Y.Z` pins one). Tell the user before
  downloading. Alternatives: `UNREAL_AGENT_RUNNER=<path>` or a runner on `PATH`.
- Credential missing: ask the user to set it in their shell or in `<workspace>/.env`.
  Never ask them to paste a key into the chat, and never print key values.

| Provider | Credential | Model |
| --- | --- | --- |
| `openai` (default) | `OPENAI_API_KEY` | optional (runner default) |
| `openrouter` | `OPENROUTER_API_KEY` | required, `<vendor>/<model>` as listed by OpenRouter |
| `fireworks` | `FIREWORKS_API_KEY` | required |
| `ollama` | none (local server, `UNREAL_HARNESS_LLM_BASE_URL` to override) | required |
| `openai-codex` | Codex ChatGPT sign-in (`codex login`); or `OPENAI_CODEX_ACCESS_TOKEN` | required, e.g. the `model` in `~/.codex/config.toml` |

`UNREAL_HARNESS_LLM_API_KEY` overrides any provider key. Kimi/Moonshot and Anthropic
providers are not implemented in the runner yet.

`openai-codex` reuses the Codex CLI's own ChatGPT login (`CODEX_HOME/auth.json`). Codex keeps
ownership of it: when the token is about to expire or is rejected, the runner asks the official
`codex app-server` to refresh it, then rereads the file. If preflight reports no ChatGPT login,
ask the user to run `codex login` themselves (it opens a browser).

## 2. Delegate

Write the task to a UTF-8 prompt file (self-contained: the delegate sees nothing from this
conversation), then run:

```
pwsh -NoProfile -File <skill>/scripts/Invoke-UnrealAgent.ps1 -Workspace <dir> -PromptFile <file> [-Provider <name>] [-Model <id>] [-ThinkingLevel low|medium|high|xhigh|max] [-NoShell] [-TimeoutMinutes N]
```

- The delegate executes shell commands in `<dir>` with the user's privileges; the harness is
  **not** a security sandbox. Before a task that may modify files, confirm the workspace with
  the user, and prefer a git worktree or a clean tree so changes are reviewable.
- `-NoShell` withholds the Shell tool (answer/review-only delegation).
- Runs can be long (model calls plus shell operations). Use the tool's background mode for
  anything that might exceed a couple of minutes, and pass `-TimeoutMinutes` as a ceiling.
- Real provider calls cost money on the user's account; state the provider and model first.

The script prints a summary: status, exit code, session ID, token usage, each tool call with
its command and exit code, errors, and the delegate's final answer. It exits with the runner's
exit code (124 on timeout).

## 3. Verify and report

The delegate's claims are unverified. After a run that could change files:

1. `git -C <dir> status --short` and `git -C <dir> diff` to see what actually changed.
2. Re-run the relevant build/test yourself rather than trusting the delegate's report.
3. Report to the user: provider/model, what the delegate did, what you verified, and anything
   that failed. Quote the delegate's answer only where it matters.

## 4. Follow up, inspect, resume

- Continue a delegation with its context: rerun with `-SessionId <id from the summary>` and a
  new prompt. The same session directory must be used (`-SessionDirectory` if overridden).
- List recent runs: `scripts/Get-UnrealAgentRun.ps1 -List`
- Re-summarize a run: `scripts/Get-UnrealAgentRun.ps1 -Path <run directory>` (`-Full` for all
  assistant messages and full tool output, `-AsJson` for structured output).
- Runs live in `<state>/unreal-agent/claude-code-runs/<timestamp>-<session>/` (`request.json`,
  `output.jsonl`, `stderr.log`, `run.json`); sessions in `<state>/unreal-agent/sessions`, where
  `<state>` is `$XDG_STATE_HOME` or `~/.local/state`.

## Harness skills

A workspace's `.harness/skills/<name>/SKILL.md` files use the same frontmatter format as
Claude Code skills. The runner offers them to the delegate automatically. When one matches the
current task, you can also read it directly and follow it yourself without delegating.
