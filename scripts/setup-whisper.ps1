<#
.SYNOPSIS
  Fetches the bundled transcription engine (whisper.cpp server + a model) into
  resources/whisper, which Parley needs for local transcription.

.DESCRIPTION
  Downloads an official whisper.cpp Windows release and a ggml model, then lays
  them out where Parley looks:
      resources/whisper/bin/Release/whisper-server.exe       (CPU + DLLs)
      resources/whisper/models/<model>.bin

  These artifacts are large and third-party, so they are NOT committed to the
  repo (.gitignore) and not auto-downloaded by the app — you run this explicitly.

  If your network blocks the download hosts (e.g. a corporate proxy in front of
  huggingface.co), the script tells you exactly which file to place where so you
  can copy it manually, or you can skip the bundled engine entirely and set a
  remote transcription URL in Parley's Settings instead.

.PARAMETER Model
  ggml model filename to download. Default: ggml-small.en-q5_1.bin — the best
  accuracy/footprint balance for a capable laptop that needs to stay responsive
  for other work (quantized, ~182 MB, runs well faster than real time on CPU).
  Other choices: ggml-base.en.bin (lighter, lower accuracy),
  ggml-large-v3-turbo-q5_0.bin (most accurate, heavier on CPU).

.PARAMETER Variant
  Which prebuilt binary to fetch: cpu (default, and the installer payload), all,
  blas (faster CPU), or cublas-12.4.0 / cublas-11.8.0 (developer options).

.PARAMETER Version
  whisper.cpp release tag. Default: v1.8.6.

.EXAMPLE
  pwsh ./scripts/setup-whisper.ps1
.EXAMPLE
  pwsh ./scripts/setup-whisper.ps1 -Model ggml-base.en.bin -Variant blas
#>
[CmdletBinding()]
param(
    [string]$Model = "ggml-small.en-q5_1.bin",
    [ValidateSet("all", "cpu", "blas", "cublas-12.4.0", "cublas-11.8.0")]
    [string]$Variant = "cpu",
    [string]$Version = "v1.8.6"
)

$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"  # faster Invoke-WebRequest

# Resolve repo root (this script lives in <root>/scripts).
$root = Split-Path -Parent $PSScriptRoot
$cpuBinDir = Join-Path $root "resources/whisper/bin/Release"
$cudaBinDir = Join-Path $root "resources/whisper/bin/cuda/Release"
$modelDir = Join-Path $root "resources/whisper/models"
New-Item -ItemType Directory -Force -Path $cpuBinDir, $cudaBinDir, $modelDir | Out-Null
# NOTE: the GitHub org is "ggml-org", but the Hugging Face model repo is hosted
# under the original author's namespace, "ggerganov/whisper.cpp". Using ggml-org
# on Hugging Face yields a 401 (HF returns 401, not 404, for repos that don't
# exist), which is the classic "looks like a proxy block but is really a bad URL".
$modelURL = "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/$Model"

function Get-File($url, $dest) {
    Write-Host "  GET $url"
    Invoke-WebRequest -Uri $url -OutFile $dest -UseBasicParsing -MaximumRedirection 5
}

function Install-WhisperVariant($selectedVariant, $destination) {
    $asset = switch ($selectedVariant) {
        "cpu" { "whisper-bin-x64.zip" }
        "blas" { "whisper-blas-bin-x64.zip" }
        default { "whisper-$selectedVariant-bin-x64.zip" }
    }
    $releaseURL = "https://github.com/ggml-org/whisper.cpp/releases/download/$Version/$asset"
    $serverExe = Join-Path $destination "whisper-server.exe"
    if (Test-Path $serverExe) {
        Write-Host "[1/2] whisper.cpp $selectedVariant already present — skipping binary." -ForegroundColor Green
        return
    }

    Write-Host "[1/2] Downloading whisper.cpp $Version ($selectedVariant)…" -ForegroundColor Cyan
    $tmp = Join-Path $env:TEMP ("parley-whisper-" + $selectedVariant + "-" + [guid]::NewGuid().ToString("N"))
    New-Item -ItemType Directory -Force -Path $tmp | Out-Null
    $zip = Join-Path $tmp $asset
    try {
        try {
            Get-File $releaseURL $zip
        }
        catch {
            throw @"
Could not download $asset from GitHub.
  $releaseURL
$($_.Exception.Message)

Manual option: download that zip yourself, extract it, and copy whisper-server.exe
and all its .dll files into:
  $destination
"@
        }
        Expand-Archive -Path $zip -DestinationPath $tmp -Force
        # The zip layout varies by release; locate the server exe wherever it landed.
        $found = Get-ChildItem -Path $tmp -Recurse -Include "whisper-server.exe", "server.exe" |
            Select-Object -First 1
        if (-not $found) {
            throw "Extracted $asset but found no whisper-server.exe/server.exe."
        }
        New-Item -ItemType Directory -Force -Path $destination | Out-Null
        Copy-Item $found.FullName (Join-Path $destination "whisper-server.exe") -Force
        Get-ChildItem -Path $found.DirectoryName -Filter *.dll | ForEach-Object {
            Copy-Item $_.FullName (Join-Path $destination $_.Name) -Force
        }
        Write-Host "      -> $serverExe" -ForegroundColor Green
    }
    finally {
        Remove-Item $tmp -Recurse -Force -ErrorAction SilentlyContinue
    }
}

# ---- 1. Binaries ----------------------------------------------------------
if ($Variant -eq "all") {
    Install-WhisperVariant "cpu" $cpuBinDir
    Install-WhisperVariant "cublas-12.4.0" $cudaBinDir
}
elseif ($Variant.StartsWith("cublas-")) {
    Install-WhisperVariant $Variant $cudaBinDir
}
else {
    Install-WhisperVariant $Variant $cpuBinDir
}

# ---- 2. Model -------------------------------------------------------------
$modelPath = Join-Path $modelDir $Model
if (Test-Path $modelPath) {
    Write-Host "[2/2] $Model already present — skipping model." -ForegroundColor Green
}
else {
    Write-Host "[2/2] Downloading model $Model…" -ForegroundColor Cyan
    try {
        Get-File $modelURL $modelPath
        Write-Host "      -> $modelPath" -ForegroundColor Green
    }
    catch {
        if (Test-Path $modelPath) { Remove-Item $modelPath -Force }
        Write-Error @"
Could not download the model from Hugging Face (a proxy may be blocking it):
  $modelURL
$($_.Exception.Message)

Manual option: download $Model from
  https://huggingface.co/ggerganov/whisper.cpp/tree/main
and save it to:
  $modelPath
"@
        exit 1
    }
}

Write-Host ""
Write-Host "Done. Parley will use the bundled engine on the next Start." -ForegroundColor Green
Write-Host "If you set this model name in Settings, use: $Model"
