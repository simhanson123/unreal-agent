#Requires -Version 7.2
<#
.SYNOPSIS
Summarizes an unreal-agent-runner JSONL output: final answer, tool calls, failures, and token usage.

.EXAMPLE
pwsh -NoProfile -File Get-UnrealAgentRun.ps1 -List
pwsh -NoProfile -File Get-UnrealAgentRun.ps1 -Path <run directory or output.jsonl>
pwsh -NoProfile -File Get-UnrealAgentRun.ps1 -Path <run directory> -Full
#>
[CmdletBinding(DefaultParameterSetName = 'Summary')]
param(
    [Parameter(ParameterSetName = 'Summary', Mandatory, Position = 0)]
    [string]$Path,
    # Include every assistant message and untruncated tool output.
    [Parameter(ParameterSetName = 'Summary')]
    [switch]$Full,
    [Parameter(ParameterSetName = 'Summary')]
    [int]$MaxOutputChars = 1500,
    [Parameter(ParameterSetName = 'Summary')]
    [switch]$AsJson,
    [Parameter(ParameterSetName = 'List', Mandatory)]
    [switch]$List,
    [Parameter(ParameterSetName = 'List')]
    [int]$Last = 10
)

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'UnrealAgentCommon.ps1')

function Limit-Text([string]$Text, [int]$Limit) {
    if ($null -eq $Text) { return '' }
    if ($Full -or $Text.Length -le $Limit) { return $Text }
    $half = [int]($Limit / 2)
    return $Text.Substring(0, $half) + "`n... [$($Text.Length - $Limit) chars omitted; rerun with -Full] ...`n" + $Text.Substring($Text.Length - $half)
}

if ($List) {
    $runs = Join-Path (Get-UnrealAgentStateHome) 'claude-code-runs'
    if (-not (Test-Path -LiteralPath $runs)) {
        Write-Output "No runs yet under $runs"
        return
    }
    Get-ChildItem -LiteralPath $runs -Directory | Sort-Object Name -Descending | Select-Object -First $Last | ForEach-Object {
        $meta = Join-Path $_.FullName 'run.json'
        if (Test-Path -LiteralPath $meta) {
            $run = Get-Content -LiteralPath $meta -Raw | ConvertFrom-Json
            [pscustomobject]@{
                Run       = $_.Name
                Status    = $run.status
                Provider  = $run.provider
                Model     = $run.model
                SessionId = $run.session_id
                Workspace = $run.workspace
            }
        }
    } | Format-Table -AutoSize | Out-String -Width 400
    return
}

$outputPath = if (Test-Path -LiteralPath $Path -PathType Container) { Join-Path $Path 'output.jsonl' } else { $Path }
if (-not (Test-Path -LiteralPath $outputPath -PathType Leaf)) {
    throw "No runner output at '$outputPath'."
}
$runMeta = $null
$metaPath = Join-Path (Split-Path -LiteralPath $outputPath) 'run.json'
if (Test-Path -LiteralPath $metaPath) {
    $runMeta = Get-Content -LiteralPath $metaPath -Raw | ConvertFrom-Json
}

$messages = [Collections.Generic.List[string]]::new()
$errors = [Collections.Generic.List[string]]::new()
$calls = [ordered]@{}
$usage = [ordered]@{ InputTokens = 0; CachedInputTokens = 0; OutputTokens = 0; ReasoningTokens = 0 }
$turns = 0
$lastResponseText = $null

foreach ($line in [IO.File]::ReadLines($outputPath)) {
    if (-not $line.Trim()) { continue }
    try {
        $record = $line | ConvertFrom-Json -Depth 64
    } catch {
        $errors.Add("Unparsable output line: $(Limit-Text $line 300)")
        continue
    }
    if ($record.PSObject.Properties['type'] -and $record.type -eq 'error') {
        $errors.Add("Runner error: $($record.message)")
        continue
    }
    if (-not $record.PSObject.Properties['Kind']) { continue }
    switch ($record.Kind) {
        'turn' { $turns++ }
        'model_response' {
            $response = $record.Data.Response
            if ($response.Failure) {
                $errors.Add("Model failure: $($response.Failure | ConvertTo-Json -Compress -Depth 10)")
            }
            if ($response.Usage) {
                foreach ($name in @($usage.Keys)) {
                    if ($response.Usage.PSObject.Properties[$name]) { $usage[$name] += [long]$response.Usage.$name }
                }
            }
            $texts = @()
            foreach ($item in @($response.Output)) {
                switch ($item.Type) {
                    'message' {
                        if ($item.Data.Role -eq 'assistant' -and $item.Data.Text) {
                            $texts += $item.Data.Text
                            $messages.Add($item.Data.Text)
                        }
                    }
                    'tool_call' {
                        $calls[$item.Data.CallID] = [ordered]@{
                            Tool = $item.Data.Name; Arguments = $item.Data.Arguments; Status = 'requested'
                        }
                    }
                }
            }
            if ($texts.Count) { $lastResponseText = $texts -join "`n" }
        }
        'tool_call_status' {
            $data = $record.Data
            $call = $calls[$data.CallID]
            if (-not $call) {
                $call = [ordered]@{ Tool = '?'; Arguments = ''; Status = 'requested' }
                $calls[$data.CallID] = $call
            }
            if ($data.Status.Error) {
                $call.Status = 'rejected'
                $call.Error = $data.Status.Error
            }
            foreach ($operation in @($data.PSObject.Properties['Operations'] ? $data.Operations : @())) {
                $call.Status = $operation.Status
                $state = $operation.State
                if ($operation.Type -eq 'shell' -and $state) {
                    $call.Command = $state.Input.Command
                    if ($state.Result) {
                        $call.ExitCode = $state.Result.ExitCode
                        $call.Stdout = $state.Result.Out
                        $call.Stderr = $state.Result.Err
                    }
                    $call.OutPath = $state.OutPath
                    $call.ErrPath = $state.ErrPath
                    if ($state.TerminalError) { $call.Error = $state.TerminalError }
                } elseif ($state -and $state.PSObject.Properties['TerminalError'] -and $state.TerminalError) {
                    $call.Error = $state.TerminalError
                }
            }
        }
    }
}

$summary = [ordered]@{
    Output      = (Resolve-Path -LiteralPath $outputPath).ProviderPath
    Status      = if ($runMeta) { $runMeta.status } else { $null }
    ExitCode    = if ($runMeta) { $runMeta.exit_code } else { $null }
    SessionId   = if ($runMeta) { $runMeta.session_id } else { $null }
    Provider    = if ($runMeta) { $runMeta.provider } else { $null }
    Model       = if ($runMeta) { $runMeta.model } else { $null }
    Turns       = $turns
    Usage       = $usage
    FinalAnswer = $lastResponseText
    ToolCalls   = @($calls.Values)
    Errors      = @($errors)
}
if ($runMeta) {
    $stderrPath = Join-Path (Split-Path -LiteralPath $outputPath) 'stderr.log'
    if ((Test-Path -LiteralPath $stderrPath) -and (Get-Item -LiteralPath $stderrPath).Length -gt 0) {
        $summary.Errors += "stderr: " + (Limit-Text (Get-Content -LiteralPath $stderrPath -Raw).Trim() $MaxOutputChars)
    }
}

if ($AsJson) {
    $summary | ConvertTo-Json -Depth 10
    return
}

$lines = [Collections.Generic.List[string]]::new()
$lines.Add("## unreal-agent run")
foreach ($name in 'Status', 'ExitCode', 'SessionId', 'Provider', 'Model', 'Turns') {
    if ($null -ne $summary[$name] -and "$($summary[$name])" -ne '') { $lines.Add("- ${name}: $($summary[$name])") }
}
$lines.Add("- Tokens: in $($usage.InputTokens) (cached $($usage.CachedInputTokens)), out $($usage.OutputTokens) (reasoning $($usage.ReasoningTokens))")
$lines.Add("- Output: $($summary.Output)")

if ($summary.Errors.Count) {
    $lines.Add("`n### Errors")
    foreach ($errorText in $summary.Errors) { $lines.Add("- $errorText") }
}
if ($calls.Count) {
    $lines.Add("`n### Tool calls ($($calls.Count))")
    $index = 0
    foreach ($call in $calls.Values) {
        $index++
        $label = if ($call.Contains('Command')) { $call.Command } else { $call.Arguments }
        $exit = if ($call.Contains('ExitCode')) { ", exit $($call.ExitCode)" } else { '' }
        $lines.Add("$index. [$($call.Tool) $($call.Status)$exit] $(Limit-Text $label 300)")
        if ($call.Contains('Error') -and $call.Error) { $lines.Add("   error: $($call.Error)") }
        if ($Full -or ($call.Contains('ExitCode') -and $call.ExitCode -ne 0)) {
            if ($call.Contains('Stdout') -and $call.Stdout) { $lines.Add("   stdout:`n" + (Limit-Text $call.Stdout $MaxOutputChars)) }
            if ($call.Contains('Stderr') -and $call.Stderr) { $lines.Add("   stderr:`n" + (Limit-Text $call.Stderr $MaxOutputChars)) }
        }
    }
}
if ($Full -and $messages.Count -gt 1) {
    $lines.Add("`n### Assistant messages")
    foreach ($message in $messages) { $lines.Add("- " + $message) }
}
$lines.Add("`n### Final answer")
$lines.Add($(if ($lastResponseText) { Limit-Text $lastResponseText ([Math]::Max($MaxOutputChars, 6000)) } else { '(none)' }))
$lines -join "`n"
