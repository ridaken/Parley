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
    [string]$InstallRoot = "",
    [string]$UvVersion = "0.11.28",
    [switch]$DiscoverExisting,
    [switch]$ReuseOnly
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

function Test-CompleteNemotronInstall {
    param([string]$Root)
    if ([string]::IsNullOrWhiteSpace($Root)) { return $false }
    foreach ($relativePath in @(
        ".ready",
        "runtime/Scripts/python.exe",
        "model/config.json",
        "model/model.safetensors",
        "server.py"
    )) {
        if (-not (Test-Path (Join-Path $Root $relativePath))) { return $false }
    }
    return $true
}

function Write-SourceRoot {
    param(
        [Parameter(Mandatory = $true)][string]$TargetRoot,
        [Parameter(Mandatory = $true)][string]$SourceRoot
    )
    New-Item -ItemType Directory -Force -Path $TargetRoot | Out-Null
    $marker = Join-Path $TargetRoot ".source-root"
    [System.IO.File]::WriteAllText(
        $marker,
        [System.IO.Path]::GetFullPath($SourceRoot),
        [System.Text.UTF8Encoding]::new($false)
    )
}

function Find-ExistingNemotronInstall {
    param(
        [string]$TargetRoot,
        [switch]$SearchUserRepositories
    )

    $directCandidates = @($env:PARLEY_NEMOTRON_HOME, $PSScriptRoot)
    foreach ($candidate in $directCandidates) {
        if ([string]::IsNullOrWhiteSpace($candidate)) { continue }
        $candidate = [System.IO.Path]::GetFullPath($candidate)
        if ($candidate -ne $TargetRoot -and (Test-CompleteNemotronInstall $candidate)) {
            return $candidate
        }
    }
    if (-not $SearchUserRepositories -or [string]::IsNullOrWhiteSpace($env:USERPROFILE)) {
        return $null
    }

    # Older development builds provisioned directly inside a checkout. Search
    # common repository locations once during interactive installer setup so a
    # valid multi-GB model can be reused in place instead of downloaded again.
    $searchRoots = [System.Collections.Generic.List[string]]::new()
    foreach ($path in @(
        (Join-Path $env:USERPROFILE "source/repos"),
        (Join-Path $env:USERPROFILE "Documents")
    )) {
        if (Test-Path $path) { $searchRoots.Add($path) }
    }
    Get-ChildItem -LiteralPath $env:USERPROFILE -Directory -Filter "OneDrive*" -ErrorAction SilentlyContinue |
        ForEach-Object {
            $documents = Join-Path $_.FullName "Documents"
            if (Test-Path $documents) { $searchRoots.Add($documents) }
        }

    $seen = @{}
    foreach ($searchRoot in $searchRoots) {
        $fullSearchRoot = [System.IO.Path]::GetFullPath($searchRoot)
        if ($seen.ContainsKey($fullSearchRoot)) { continue }
        $seen[$fullSearchRoot] = $true
        Write-Host "Checking $fullSearchRoot for an existing Nemotron installation..."
        foreach ($marker in Get-ChildItem -LiteralPath $fullSearchRoot -File -Filter ".ready" -Recurse -Force -ErrorAction SilentlyContinue) {
            $candidate = $marker.Directory.FullName
            if ($candidate -ne $TargetRoot -and (Test-CompleteNemotronInstall $candidate)) {
                return $candidate
            }
        }
    }
    return $null
}

if ([string]::IsNullOrWhiteSpace($InstallRoot)) {
    if (-not [string]::IsNullOrWhiteSpace($env:LOCALAPPDATA)) {
        $InstallRoot = Join-Path $env:LOCALAPPDATA "Parley/nemotron"
    }
    else {
        $InstallRoot = $PSScriptRoot
    }
}

$InstallRoot = [System.IO.Path]::GetFullPath($InstallRoot)
$readyMarker = Join-Path $InstallRoot ".ready"
$sourceRootMarker = Join-Path $InstallRoot ".source-root"
$venvDir = Join-Path $InstallRoot "runtime"
$pythonExe = Join-Path $venvDir "Scripts/python.exe"
$modelDir = Join-Path $InstallRoot "model"
$toolsDir = Join-Path $InstallRoot "tools"
$uvExe = Join-Path $toolsDir "uv.exe"

New-Item -ItemType Directory -Force -Path $InstallRoot | Out-Null
foreach ($supportFile in @("download_model.py", "server.py", "validate_install.py")) {
    $source = Join-Path $PSScriptRoot $supportFile
    $destination = Join-Path $InstallRoot $supportFile
    if ((Test-Path $source) -and ([System.IO.Path]::GetFullPath($source) -ne [System.IO.Path]::GetFullPath($destination))) {
        Copy-Item $source $destination -Force
    }
}

if (Test-CompleteNemotronInstall $InstallRoot) {
    Write-Host "Nemotron 3.5 ASR is already provisioned; keeping the existing installation."
    return
}

if (Test-Path $sourceRootMarker) {
    $sourceRoot = (Get-Content $sourceRootMarker -Raw).Trim().TrimStart([char]0xFEFF)
    if (Test-CompleteNemotronInstall $sourceRoot) {
        Write-Host "Nemotron 3.5 ASR is already provisioned at $sourceRoot; reusing it."
        return
    }
    Remove-Item $sourceRootMarker -Force -ErrorAction SilentlyContinue
}

$existingRoot = Find-ExistingNemotronInstall -TargetRoot $InstallRoot -SearchUserRepositories:$DiscoverExisting
if ($existingRoot) {
    Write-SourceRoot -TargetRoot $InstallRoot -SourceRoot $existingRoot
    Write-Host "Reusing existing Nemotron 3.5 ASR installation at $existingRoot."
    return
}
if ($ReuseOnly) {
    Write-Host "No reusable Nemotron installation was found."
    return
}

$gpu = & nvidia-smi -L 2>$null
if ($LASTEXITCODE -ne 0 -or -not ($gpu -match "GPU ")) {
    Write-Host "No usable NVIDIA GPU detected; Parley will use bundled CPU Whisper."
    return
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
    return
}
Write-Host "Eligible NVIDIA GPU detected: $eligibleGPU"

New-Item -ItemType Directory -Force -Path $InstallRoot, $toolsDir, $modelDir | Out-Null
Remove-Item $readyMarker -Force -ErrorAction SilentlyContinue
Remove-Item $sourceRootMarker -Force -ErrorAction SilentlyContinue

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

# Keep the large cache private to Parley without hiding a token created by a
# previous `hf auth login`. HF_TOKEN still takes precedence when explicitly set.
if ([string]::IsNullOrWhiteSpace($env:HF_TOKEN) -and [string]::IsNullOrWhiteSpace($env:HF_TOKEN_PATH)) {
    $userCache = if ([string]::IsNullOrWhiteSpace($env:XDG_CACHE_HOME)) {
        Join-Path $HOME ".cache"
    }
    else {
        $env:XDG_CACHE_HOME
    }
    $existingToken = Join-Path $userCache "huggingface/token"
    if (Test-Path $existingToken) {
        $env:HF_TOKEN_PATH = $existingToken
        Write-Host "Using the existing Hugging Face login for authenticated downloads."
    }
}
$env:HF_HOME = Join-Path $InstallRoot "cache/huggingface"
$env:HF_HUB_DISABLE_SYMLINKS_WARNING = "1"
if ([string]::IsNullOrWhiteSpace($env:HF_XET_HIGH_PERFORMANCE)) {
    $env:HF_XET_HIGH_PERFORMANCE = "1"
}
if ([string]::IsNullOrWhiteSpace($env:HF_HUB_DOWNLOAD_TIMEOUT)) {
    $env:HF_HUB_DOWNLOAD_TIMEOUT = "60"
}

if (-not (Test-Path $pythonExe)) {
    Write-Host "Installing private Python 3.11 runtime..."
    # The runtime is private to Parley; do not also create a user-profile Python
    # shim under ~/.local/bin. A pre-existing shim is what produced the alarming
    # but otherwise harmless "Executable already exists" warning in setup.
    Invoke-Checked $uvExe python install 3.11 --no-bin
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
