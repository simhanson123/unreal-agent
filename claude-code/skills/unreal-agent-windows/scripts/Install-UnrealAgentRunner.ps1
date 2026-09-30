#Requires -Version 7.2
<#
.SYNOPSIS
Downloads a released unreal-agent-runner, verifies it against SHA256SUMS, and installs it per user.

.EXAMPLE
pwsh -NoProfile -File Install-UnrealAgentRunner.ps1
pwsh -NoProfile -File Install-UnrealAgentRunner.ps1 -Version v0.2.3
#>
[CmdletBinding()]
param(
    # Release tag such as v0.2.3. Defaults to the latest published release.
    [string]$Version,
    [switch]$Force
)

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'UnrealAgentCommon.ps1')

$headers = @{ 'User-Agent' = 'unreal-agent-plus-windows-skill'; 'Accept' = 'application/vnd.github+json' }
$releaseUrl = if ($Version) {
    "https://api.github.com/repos/$script:RepositorySlug/releases/tags/$Version"
} else {
    "https://api.github.com/repos/$script:RepositorySlug/releases/latest"
}
$release = Invoke-RestMethod -Uri $releaseUrl -Headers $headers
$tag = $release.tag_name
$number = $tag.TrimStart('v')
if (-not ($number -as [version])) {
    throw "Release tag '$tag' is not a version."
}

$platform = Get-UnrealAgentPlatform
$extension = if ($platform.OS -eq 'windows') { 'zip' } else { 'tar.gz' }
$archiveName = "unreal-agent-runner_${number}_$($platform.OS)_$($platform.Arch).$extension"
$archiveAsset = $release.assets | Where-Object name -EQ $archiveName
$sumsAsset = $release.assets | Where-Object name -EQ 'SHA256SUMS'
if (-not $archiveAsset -or -not $sumsAsset) {
    throw "Release $tag has no $archiveName or SHA256SUMS asset."
}

$destination = Join-Path (Get-UnrealAgentInstallRoot) $number
$runner = Join-Path $destination $script:RunnerName
if ((Test-Path -LiteralPath $runner) -and -not $Force) {
    Write-Output "Already installed: $runner"
    return
}

$staging = Join-Path ([IO.Path]::GetTempPath()) ("unreal-agent-install-" + [guid]::NewGuid())
New-Item -ItemType Directory -Path $staging | Out-Null
try {
    $archivePath = Join-Path $staging $archiveName
    Invoke-WebRequest -Uri $archiveAsset.browser_download_url -Headers $headers -OutFile $archivePath
    $sums = (Invoke-WebRequest -Uri $sumsAsset.browser_download_url -Headers $headers).Content
    if ($sums -is [byte[]]) {
        $sums = [Text.Encoding]::UTF8.GetString($sums)
    }
    $expected = $sums -split "`n" |
        Where-Object { $_ -match "^([0-9a-fA-F]{64})\s+\*?(\./)?$([regex]::Escape($archiveName))\s*$" } |
        ForEach-Object { $Matches[1].ToLowerInvariant() } |
        Select-Object -First 1
    if (-not $expected) {
        throw "SHA256SUMS has no entry for $archiveName."
    }
    $actual = (Get-FileHash -LiteralPath $archivePath -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($actual -ne $expected) {
        throw "Checksum mismatch for $archiveName (expected $expected, got $actual)."
    }

    $extracted = Join-Path $staging 'extract'
    New-Item -ItemType Directory -Path $extracted | Out-Null
    if ($extension -eq 'zip') {
        Expand-Archive -LiteralPath $archivePath -DestinationPath $extracted
    } else {
        tar -xzf $archivePath -C $extracted
        if ($LASTEXITCODE -ne 0) { throw "tar failed with exit code $LASTEXITCODE." }
    }
    if (-not (Test-Path -LiteralPath (Join-Path $extracted $script:RunnerName))) {
        throw "Archive does not contain $script:RunnerName."
    }
    if (Test-Path -LiteralPath $destination) {
        Remove-Item -LiteralPath $destination -Recurse -Force
    }
    New-Item -ItemType Directory -Path (Split-Path $destination) -Force | Out-Null
    Move-Item -LiteralPath $extracted -Destination $destination
    if (-not $IsWindows) {
        chmod +x $runner
    }
} finally {
    Remove-Item -LiteralPath $staging -Recurse -Force -ErrorAction SilentlyContinue
}
Write-Output "Installed $tag ($actual): $runner"
