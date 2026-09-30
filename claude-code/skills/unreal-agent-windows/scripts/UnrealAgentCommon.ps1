#Requires -Version 7.2
# Shared helpers for the unreal-agent(+Windows) Claude Code skill scripts. Dot-source only.

Set-StrictMode -Version Latest

$script:RepositorySlug = 'simhanson123/unreal-agent-plus-windows'
$script:RunnerName = if ($IsWindows) { 'unreal-agent-runner.exe' } else { 'unreal-agent-runner' }

# Provider name -> environment variable holding its credential. Empty means no key is needed.
$script:ProviderKeyEnvironment = [ordered]@{
    'openai'       = 'OPENAI_API_KEY'
    'openai-codex' = ''
    'openrouter'   = 'OPENROUTER_API_KEY'
    'fireworks'    = 'FIREWORKS_API_KEY'
    'ollama'       = ''
}

# Claude Code's own credentials must never reach the harness or the commands its model runs.
$script:ScrubbedEnvironmentPattern = '^(CLAUDE_CODE_OAUTH_TOKEN|ANTHROPIC_AUTH_TOKEN|CLAUDE_CODE_.*(TOKEN|SECRET|KEY).*)$'

function Get-UnrealAgentStateHome {
    # Mirrors the runner's session-directory resolution so runs and sessions live side by side.
    $stateHome = $env:XDG_STATE_HOME
    if (-not $stateHome -or -not [IO.Path]::IsPathRooted($stateHome)) {
        $userHome = if ($env:HOME) { $env:HOME } else { [Environment]::GetFolderPath('UserProfile') }
        $stateHome = Join-Path $userHome '.local' 'state'
    }
    Join-Path $stateHome 'unreal-agent'
}

function Get-UnrealAgentInstallRoot {
    $dataHome = if ($IsWindows) {
        $env:LOCALAPPDATA
    } elseif ($env:XDG_DATA_HOME -and [IO.Path]::IsPathRooted($env:XDG_DATA_HOME)) {
        $env:XDG_DATA_HOME
    } else {
        Join-Path $HOME '.local' 'share'
    }
    Join-Path $dataHome 'unreal-agent-plus-windows' 'bin'
}

function Resolve-UnrealAgentRunner {
    param([string]$RunnerPath)

    $candidates = @($RunnerPath, $env:UNREAL_AGENT_RUNNER) | Where-Object { $_ }
    foreach ($candidate in $candidates) {
        if (Test-Path -LiteralPath $candidate -PathType Leaf) {
            return (Resolve-Path -LiteralPath $candidate).ProviderPath
        }
        throw "Runner not found at '$candidate' (from -RunnerPath or UNREAL_AGENT_RUNNER)."
    }
    $onPath = Get-Command 'unreal-agent-runner' -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($onPath) {
        return $onPath.Source
    }
    $installRoot = Get-UnrealAgentInstallRoot
    if (Test-Path -LiteralPath $installRoot) {
        $installed = Get-ChildItem -LiteralPath $installRoot -Directory |
            Where-Object { $_.Name -as [version] } |
            Sort-Object { [version]$_.Name } -Descending |
            ForEach-Object { Join-Path $_.FullName $script:RunnerName } |
            Where-Object { Test-Path -LiteralPath $_ -PathType Leaf } |
            Select-Object -First 1
        if ($installed) {
            return $installed
        }
    }
    return $null
}

function Get-UnrealAgentPlatform {
    $os = if ($IsWindows) { 'windows' } elseif ($IsMacOS) { 'darwin' } elseif ($IsLinux) { 'linux' } else { throw 'Unsupported operating system.' }
    $arch = switch ([Runtime.InteropServices.RuntimeInformation]::OSArchitecture) {
        'X64' { 'amd64' }
        'Arm64' { 'arm64' }
        default { throw "Unsupported architecture '$_'." }
    }
    if ($os -eq 'windows' -and $arch -ne 'amd64') {
        throw 'Release archives are published for windows_amd64 only; build from source for Windows ARM64.'
    }
    [pscustomobject]@{ OS = $os; Arch = $arch }
}
