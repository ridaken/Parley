<#
.SYNOPSIS
  Runs Nemotron provisioning in a visible, cancellable progress window.
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$InstallRoot
)

$ErrorActionPreference = "Stop"
$InstallRoot = [System.IO.Path]::GetFullPath($InstallRoot)
New-Item -ItemType Directory -Force -Path $InstallRoot | Out-Null
$logPath = Join-Path $InstallRoot "provision.log"
$transcribing = $false
$succeeded = $false

try {
    try {
        $Host.UI.RawUI.WindowTitle = "Parley - Nemotron 3.5 ASR Setup"
    }
    catch {}

    Write-Host "Parley - Nemotron 3.5 ASR Setup" -ForegroundColor Cyan
    Write-Host ""
    Write-Host "This window searches for an existing model before downloading anything."
    Write-Host "Close this window at any time to cancel. A later attempt will resume cached and partial downloads."
    Write-Host "Progress is also saved to $logPath"
    Write-Host ""

    try {
        Start-Transcript -Path $logPath -Append | Out-Null
        $transcribing = $true
    }
    catch {
        Write-Warning "Could not start the provisioning transcript: $_"
    }

    & (Join-Path $PSScriptRoot "setup.ps1") -InstallRoot $InstallRoot -DiscoverExisting
    if (-not ((Test-Path (Join-Path $InstallRoot ".ready")) -or (Test-Path (Join-Path $InstallRoot ".source-root")))) {
        throw "Nemotron setup ended without a complete installation. CPU Whisper remains available."
    }
    $succeeded = $true
    Write-Host ""
    Write-Host "Nemotron setup completed. Parley will select it the next time the app starts." -ForegroundColor Green
}
catch {
    Write-Host ""
    Write-Host "Nemotron setup did not complete: $_" -ForegroundColor Red
    Write-Host "CPU Whisper remains available. Run the Parley installer again later to resume."
}
finally {
    if ($transcribing) {
        try { Stop-Transcript | Out-Null } catch {}
    }
}

if ($succeeded) {
    Write-Host "This window will close in 5 seconds."
    Start-Sleep -Seconds 5
    exit 0
}

Read-Host "Press Enter to close"
exit 1
