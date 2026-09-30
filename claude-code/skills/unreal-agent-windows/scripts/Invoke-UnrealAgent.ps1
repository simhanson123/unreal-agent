#Requires -Version 7.2
<#
.SYNOPSIS
Delegates one task to unreal-agent-runner with a non-Claude-Code provider and summarizes the result.

.DESCRIPTION
Claude Code stays the orchestrator. The harness authenticates only with its own provider
credentials (OPENAI_API_KEY, OPENROUTER_API_KEY, FIREWORKS_API_KEY, local Ollama, ...).
Claude Code credential variables are removed from the runner's environment.

Each run writes request.json, output.jsonl, stderr.log, and run.json to a run directory under
<state>/unreal-agent/claude-code-runs, then prints a summary (see Get-UnrealAgentRun.ps1).

.EXAMPLE
pwsh -NoProfile -File Invoke-UnrealAgent.ps1 -Check -Provider openrouter
pwsh -NoProfile -File Invoke-UnrealAgent.ps1 -Workspace D:\Proj -Provider openai -PromptFile task.md
pwsh -NoProfile -File Invoke-UnrealAgent.ps1 -Workspace D:\Proj -SessionId <id> -Prompt "Now add tests."
#>
[CmdletBinding()]
param(
    [string]$Prompt,
    # UTF-8 file containing the prompt; preferred for long or multi-line tasks.
    [string]$PromptFile,
    [string]$Workspace = (Get-Location).ProviderPath,
    [ValidateSet('openai', 'openai-codex', 'openrouter', 'fireworks', 'ollama')]
    [string]$Provider,
    [string]$Model,
    [ValidateSet('low', 'medium', 'high', 'xhigh', 'max')]
    [string]$ThinkingLevel,
    # Resume a persisted harness session. A new session ID is generated when omitted.
    [string]$SessionId,
    [string]$SystemPromptFile,
    # Withhold the Shell/Bash tool so the delegate can only answer from the prompt (and images).
    [switch]$NoShell,
    [int]$MaxAttempts,
    [int]$TimeoutMinutes = 0,
    [string]$RunnerPath,
    [string]$SessionDirectory,
    # Validate runner, provider, and credentials presence without calling a model.
    [switch]$Check
)

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'UnrealAgentCommon.ps1')

$runner = Resolve-UnrealAgentRunner -RunnerPath $RunnerPath
$effectiveProvider = if ($Provider) { $Provider } elseif ($env:UNREAL_HARNESS_LLM_PROVIDER) { $env:UNREAL_HARNESS_LLM_PROVIDER } else { 'openai' }
$effectiveModel = if ($Model) { $Model } elseif ($env:UNREAL_HARNESS_LLM_MODEL) { $env:UNREAL_HARNESS_LLM_MODEL } else { '' }
$workspacePath = (Resolve-Path -LiteralPath $Workspace).ProviderPath
if (-not (Test-Path -LiteralPath $workspacePath -PathType Container)) {
    throw "Workspace '$workspacePath' is not a directory."
}

function Test-ProviderCredential {
    if ($env:UNREAL_HARNESS_LLM_API_KEY) { return 'UNREAL_HARNESS_LLM_API_KEY is set' }
    if (-not $script:ProviderKeyEnvironment.Contains($effectiveProvider)) { return "unknown provider '$effectiveProvider'" }
    $name = $script:ProviderKeyEnvironment[$effectiveProvider]
    if ($effectiveProvider -eq 'openai-codex') {
        if ($env:OPENAI_CODEX_ACCESS_TOKEN) { return 'OPENAI_CODEX_ACCESS_TOKEN is set' }
        if ($env:OPENAI_CODEX_AUTH_FILE) { return "uses OPENAI_CODEX_AUTH_FILE (no automatic refresh)" }
        # The default Codex login is owned by the Codex CLI, which also refreshes it for the runner.
        $codex = if ($env:OPENAI_CODEX_CLI) { $env:OPENAI_CODEX_CLI } else { 'codex' }
        if (-not (Get-Command $codex -ErrorAction SilentlyContinue)) {
            return 'MISSING: install the Codex CLI and run `codex login` (ChatGPT sign-in)'
        }
        $status = (& $codex login status 2>&1 | Out-String).Trim()
        if ($status -match 'ChatGPT') { return "Codex ChatGPT login ($status)" }
        return "MISSING: run ``codex login`` and sign in with ChatGPT (codex login status: $status)"
    }
    if (-not $name) { return 'no credential required' }
    if ([Environment]::GetEnvironmentVariable($name)) { return "$name is set" }
    $dotEnv = Join-Path $workspacePath '.env'
    if ((Test-Path -LiteralPath $dotEnv) -and (Select-String -LiteralPath $dotEnv -Pattern "^\s*$([regex]::Escape($name))\s*=" -Quiet)) {
        return "$name comes from $dotEnv"
    }
    return "MISSING: set $name (or UNREAL_HARNESS_LLM_API_KEY)"
}

$credential = Test-ProviderCredential
if ($Check) {
    $shell = if ($env:HARNESS_SHELL) { $env:HARNESS_SHELL } elseif ($IsWindows) { 'pwsh' } elseif ($env:SHELL) { $env:SHELL } else { '/bin/sh' }
    [ordered]@{
        Runner     = if ($runner) { $runner } else { 'MISSING: run Install-UnrealAgentRunner.ps1 or set UNREAL_AGENT_RUNNER' }
        Provider   = $effectiveProvider
        Model      = if ($effectiveModel) { $effectiveModel } elseif ($effectiveProvider -eq 'openai') { '(provider default)' } else { 'MISSING: pass -Model or set UNREAL_HARNESS_LLM_MODEL' }
        Credential = $credential
        Shell      = "$shell -> $((Get-Command $shell -ErrorAction SilentlyContinue).Source ?? 'NOT FOUND')"
        Workspace  = $workspacePath
        Skills     = @(Get-ChildItem -Path (Join-Path $workspacePath '.harness' 'skills' '*' 'SKILL.md') -ErrorAction SilentlyContinue).Count
    } | Format-List | Out-String -Width 300
    return
}

if (-not $runner) {
    throw 'unreal-agent-runner not found. Run Install-UnrealAgentRunner.ps1, add it to PATH, or set UNREAL_AGENT_RUNNER.'
}
if ($credential -like 'MISSING*') {
    throw "Provider '$effectiveProvider' credential check failed: $credential"
}
if (-not $effectiveModel -and $effectiveProvider -ne 'openai') {
    throw "Provider '$effectiveProvider' has no default model; pass -Model or set UNREAL_HARNESS_LLM_MODEL."
}
if ([bool]$Prompt -eq [bool]$PromptFile) {
    throw 'Pass exactly one of -Prompt or -PromptFile.'
}
$promptText = if ($PromptFile) { Get-Content -LiteralPath $PromptFile -Raw -Encoding utf8 } else { $Prompt }
if (-not $promptText.Trim()) {
    throw 'Prompt is empty.'
}

if (-not $SessionId) {
    $SessionId = [guid]::NewGuid().ToString()
}
$request = [ordered]@{ prompt = $promptText; session_id = $SessionId }
if ($Model) { $request.model = $Model }
if ($ThinkingLevel) { $request.thinking_level = $ThinkingLevel }
if ($MaxAttempts -gt 0) { $request.max_attempts = $MaxAttempts }
if ($SystemPromptFile) { $request.system_prompt = Get-Content -LiteralPath $SystemPromptFile -Raw -Encoding utf8 }
if ($NoShell) { $request.disallowed_tools = @('Shell', 'Bash') }

$runsRoot = Join-Path (Get-UnrealAgentStateHome) 'claude-code-runs'
$runDirectory = Join-Path $runsRoot ((Get-Date).ToUniversalTime().ToString('yyyyMMdd-HHmmss') + '-' + $SessionId.Substring(0, [Math]::Min(8, $SessionId.Length)))
New-Item -ItemType Directory -Path $runDirectory -Force | Out-Null
$requestPath = Join-Path $runDirectory 'request.json'
$outputPath = Join-Path $runDirectory 'output.jsonl'
$stderrPath = Join-Path $runDirectory 'stderr.log'
$metaPath = Join-Path $runDirectory 'run.json'
[IO.File]::WriteAllText($requestPath, ($request | ConvertTo-Json -Depth 5), [Text.UTF8Encoding]::new($false))

$meta = [ordered]@{
    status = 'running'; exit_code = $null; session_id = $SessionId; provider = $effectiveProvider
    model = $effectiveModel; workspace = $workspacePath; runner = $runner
    started_at = (Get-Date).ToUniversalTime().ToString('o'); finished_at = $null
}
function Save-RunMeta { [IO.File]::WriteAllText($metaPath, ($meta | ConvertTo-Json), [Text.UTF8Encoding]::new($false)) }
Save-RunMeta

# The runner inherits this process environment; scope provider selection and scrub Claude Code credentials.
if ($Provider) { $env:UNREAL_HARNESS_LLM_PROVIDER = $Provider }
if ($Model) { $env:UNREAL_HARNESS_LLM_MODEL = $Model }
Get-ChildItem Env: | Where-Object Name -Match $script:ScrubbedEnvironmentPattern | ForEach-Object {
    Remove-Item -LiteralPath "Env:$($_.Name)"
}

function ConvertTo-ProcessArgument([string]$Value) {
    # Windows command-line quoting: escape quotes and the backslashes that precede them.
    '"' + ($Value -replace '(\\*)"', '$1$1\"' -replace '(\\+)$', '$1$1') + '"'
}
$arguments = @('-workspace', (ConvertTo-ProcessArgument $workspacePath))
if ($SessionDirectory) { $arguments += @('-session-directory', (ConvertTo-ProcessArgument ([IO.Path]::GetFullPath($SessionDirectory)))) }
Write-Host "unreal-agent: provider=$effectiveProvider model=$(if ($effectiveModel) { $effectiveModel } else { 'default' }) session=$SessionId"
Write-Host "unreal-agent: run directory $runDirectory"

$process = Start-Process -FilePath $runner -ArgumentList $arguments -WorkingDirectory $workspacePath `
    -RedirectStandardInput $requestPath -RedirectStandardOutput $outputPath -RedirectStandardError $stderrPath `
    -NoNewWindow -PassThru
$timedOut = $false
try {
    if ($TimeoutMinutes -gt 0) {
        if (-not $process.WaitForExit([int][TimeSpan]::FromMinutes($TimeoutMinutes).TotalMilliseconds)) {
            $timedOut = $true
            $process.Kill($true)
        }
    }
    $process.WaitForExit()
} finally {
    if (-not $process.HasExited) {
        # Interrupted (for example Ctrl+C or a stopped background task): do not leave the runner behind.
        $process.Kill($true)
        $meta.status = 'interrupted'
        $meta.finished_at = (Get-Date).ToUniversalTime().ToString('o')
        Save-RunMeta
    }
}

$meta.exit_code = $process.ExitCode
$meta.status = if ($timedOut) { 'timed_out' } elseif ($process.ExitCode -eq 0) { 'completed' } else { 'failed' }
$meta.finished_at = (Get-Date).ToUniversalTime().ToString('o')
Save-RunMeta

& (Join-Path $PSScriptRoot 'Get-UnrealAgentRun.ps1') -Path $runDirectory
exit $(if ($timedOut) { 124 } else { $process.ExitCode })
