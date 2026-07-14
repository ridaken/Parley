<#
.SYNOPSIS
  Provisions Parley's optional NVIDIA Nemotron 3.5 ASR runtime.

.DESCRIPTION
  This script is intentionally safe to run repeatedly. It downloads a private
  Python 3.11 runtime, CUDA-enabled PyTorch, Transformers, and only the 2.55 GB
  Transformers checkpoint (not the duplicate 2.37 GB .nemo archive). A .ready
  marker is written only after CUDA and model configuration validation succeeds.
#>
[CmdletBinding()]
param(
    [string]$InstallRoot = $PSScriptRoot,
    [string]$UvVersion = "0.11.28"
)

$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"

function Invoke-Checked {
    param(
        [Parameter(Mandatory = $true)][string]$Program,
        [Parameter(ValueFromRemainingArguments = $true)][string[]]$Arguments
    )
    & $Program @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "$Program exited with code $LASTEXITCODE"
    }
}

$InstallRoot = [System.IO.Path]::GetFullPath($InstallRoot)
$readyMarker = Join-Path $InstallRoot ".ready"
$venvDir = Join-Path $InstallRoot "runtime"
$pythonExe = Join-Path $venvDir "Scripts/python.exe"
$modelDir = Join-Path $InstallRoot "model"
$toolsDir = Join-Path $InstallRoot "tools"
$uvExe = Join-Path $toolsDir "uv.exe"

if ((Test-Path $readyMarker) -and (Test-Path $pythonExe) -and (Test-Path (Join-Path $modelDir "model.safetensors"))) {
    Write-Host "Nemotron 3.5 ASR is already provisioned; keeping the existing installation."
    exit 0
}

$gpu = & nvidia-smi -L 2>$null
if ($LASTEXITCODE -ne 0 -or -not ($gpu -match "GPU ")) {
    Write-Host "No usable NVIDIA GPU detected; Parley will use bundled CPU Whisper."
    exit 0
}

# Each checkpoint instance is about 1.3 GB in FP16 and Parley's two concurrent
# audio sources need independent decoder state, plus activation/kernel memory.
# NVIDIA lists Volta (compute capability 7.0) and newer as supported, so skip the
# multi-GB download when no adapter meets both conservative gates.
$gpuRows = & nvidia-smi --query-gpu=name,memory.total,compute_cap --format=csv,noheader,nounits 2>$null
if ($LASTEXITCODE -ne 0) {
    throw "nvidia-smi could not report GPU memory and compute capability"
}
$eligibleGPU = $null
foreach ($row in $gpuRows) {
    $parts = $row -split ","
    if ($parts.Count -lt 3) { continue }
    $memoryMiB = 0
    $computeCapability = 0.0
    if (-not [int]::TryParse($parts[1].Trim(), [ref]$memoryMiB)) { continue }
    if (-not [double]::TryParse(
        $parts[2].Trim(),
        [System.Globalization.NumberStyles]::Float,
        [System.Globalization.CultureInfo]::InvariantCulture,
        [ref]$computeCapability
    )) { continue }
    if ($memoryMiB -ge 6144 -and $computeCapability -ge 7.0) {
        $eligibleGPU = "$($parts[0].Trim()) ($memoryMiB MiB, compute $computeCapability)"
        break
    }
}
if (-not $eligibleGPU) {
    Write-Host "No NVIDIA GPU with at least 6 GiB VRAM and compute capability 7.0+ was found; using CPU Whisper."
    exit 0
}
Write-Host "Eligible NVIDIA GPU detected: $eligibleGPU"

New-Item -ItemType Directory -Force -Path $InstallRoot, $toolsDir, $modelDir | Out-Null
Remove-Item $readyMarker -Force -ErrorAction SilentlyContinue

if (-not (Test-Path $uvExe)) {
    $asset = "uv-x86_64-pc-windows-msvc.zip"
    $url = "https://github.com/astral-sh/uv/releases/download/$UvVersion/$asset"
    $tempDir = Join-Path $env:TEMP ("parley-uv-" + [guid]::NewGuid().ToString("N"))
    $zip = Join-Path $tempDir $asset
    New-Item -ItemType Directory -Force -Path $tempDir | Out-Null
    try {
        Write-Host "Downloading uv $UvVersion..."
        Invoke-WebRequest -Uri $url -OutFile $zip -UseBasicParsing -MaximumRedirection 5
        Expand-Archive -Path $zip -DestinationPath $tempDir -Force
        $downloadedUv = Get-ChildItem $tempDir -Recurse -Filter "uv.exe" | Select-Object -First 1
        if (-not $downloadedUv) { throw "The uv archive did not contain uv.exe" }
        Copy-Item $downloadedUv.FullName $uvExe -Force
    }
    finally {
        Remove-Item $tempDir -Recurse -Force -ErrorAction SilentlyContinue
    }
}

$env:UV_PYTHON_INSTALL_DIR = Join-Path $InstallRoot "python"
$env:UV_CACHE_DIR = Join-Path $InstallRoot "cache/uv"
$env:UV_LINK_MODE = "copy"
$env:HF_HOME = Join-Path $InstallRoot "cache/huggingface"
$env:HF_HUB_DISABLE_SYMLINKS_WARNING = "1"

if (-not (Test-Path $pythonExe)) {
    Write-Host "Installing private Python 3.11 runtime..."
    Invoke-Checked $uvExe python install 3.11
    Invoke-Checked $uvExe venv $venvDir --python 3.11 --managed-python
}

Write-Host "Installing CUDA PyTorch..."
Invoke-Checked $uvExe pip install --python $pythonExe torch --index-url "https://download.pytorch.org/whl/cu128"
Write-Host "Installing Nemotron runtime dependencies..."
Invoke-Checked $uvExe pip install --python $pythonExe "transformers>=5.13.0" accelerate huggingface-hub librosa numpy safetensors

Write-Host "Downloading nvidia/nemotron-3.5-asr-streaming-0.6b Transformers checkpoint..."
Invoke-Checked $pythonExe (Join-Path $InstallRoot "download_model.py") $modelDir

Write-Host "Validating CUDA and model configuration..."
Invoke-Checked $pythonExe (Join-Path $InstallRoot "validate_install.py") $modelDir

$marker = @{
    model = "nvidia/nemotron-3.5-asr-streaming-0.6b"
    provisionedAt = [DateTime]::UtcNow.ToString("o")
} | ConvertTo-Json
Set-Content -Path $readyMarker -Value $marker -Encoding UTF8
Write-Host "Nemotron 3.5 ASR provisioning complete."
