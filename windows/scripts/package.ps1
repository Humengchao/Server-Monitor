<#
.SYNOPSIS
    Publishes Server Monitor and packages it as a portable zip plus an
    Inno Setup installer.

.DESCRIPTION
    Self-contained single-file publish (D2), so the user's machine needs no
    .NET runtime. Trimming is deliberately off: WPF is not trim-safe and the
    XAML loader's reflection breaks in ways that only show at runtime.

    Not code-signed (D7). SmartScreen warns on first run and some antivirus
    products flag unsigned self-contained executables (R7); the README and the
    release notes say so.

.PARAMETER Version
    The version to stamp and to name the artifacts with.

.PARAMETER Runtimes
    Which runtimes to publish. Both by default.

.PARAMETER SkipInstaller
    Produce only the portable zips. Useful locally, where Inno Setup may not be
    installed; CI always has it (F8).

.PARAMETER InstallerOnly
    Build only the installer, from a publish that is already in dist/. CI runs
    the two halves as separate steps so a failure names which one broke — the
    step list is readable without downloading a log, and the first packaging
    run failed in a way that took a local repro to find.
#>
[CmdletBinding()]
param(
    [string] $Version = '0.1.0-dev',
    [string[]] $Runtimes = @('win-x64', 'win-arm64'),
    [switch] $SkipInstaller,
    [switch] $InstallerOnly
)

$ErrorActionPreference = 'Stop'

$root = Split-Path -Parent $PSScriptRoot
$dist = Join-Path $root 'dist'
$app = Join-Path $root 'src/ServerMonitor.App/ServerMonitor.App.csproj'

# The version that goes into the assembly cannot carry the +sha suffix: the
# Win32 file version fields are four numbers and nothing else.
$assemblyVersion = ($Version -split '[-+]')[0]
if ($assemblyVersion -notmatch '^\d+\.\d+\.\d+$') {
    throw "Version '$Version' does not start with a three-part number"
}

if (-not $InstallerOnly) {
    if (Test-Path $dist) { Remove-Item -Recurse -Force $dist }
}
New-Item -ItemType Directory -Force -Path $dist | Out-Null

$toPublish = if ($InstallerOnly) { @() } else { $Runtimes }
foreach ($runtime in $toPublish) {
    Write-Host "== publishing $runtime" -ForegroundColor Cyan
    $out = Join-Path $dist $runtime

    # Bundle compression is off, measured (artifacts/singlefile-compression.csv):
    # it halved the exe (77 vs 182 MB) but barely changed what anyone
    # downloads — the zip re-compresses (74.9 vs 75.4 MB) and so does the
    # installer's lzma2 — while costing ~85 MB of resident memory for the
    # life of the process, because the compressed image is decompressed
    # into memory at startup and stays there.
    dotnet publish $app `
        -c Release `
        -r $runtime `
        --self-contained true `
        -p:PublishSingleFile=true `
        -p:IncludeNativeLibrariesForSelfExtract=true `
        -p:EnableCompressionInSingleFile=false `
        -p:PublishTrimmed=false `
        -p:DebugType=none `
        -p:Version=$assemblyVersion `
        -p:InformationalVersion=$Version `
        -o $out
    if ($LASTEXITCODE -ne 0) { throw "publish failed for $runtime" }

    # The single-file host still drops a couple of loose files next to itself
    # (the runtime config, and the WebView2 loader if it ever comes in). Ship
    # the directory rather than only the exe.
    $arch = $runtime -replace '^win-', ''
    $zip = Join-Path $dist "Server-Monitor-$Version-$arch-portable.zip"
    Compress-Archive -Path (Join-Path $out '*') -DestinationPath $zip -Force
    Write-Host "   -> $zip"
}

if ($SkipInstaller) {
    Write-Host 'skipping the installer (-SkipInstaller)' -ForegroundColor Yellow
    Get-ChildItem $dist -File | Select-Object Name, Length
    return
}

# Inno Setup 6, preinstalled on the CI image (F8). Looked up rather than
# assumed on PATH, which is how it is installed locally.
$iscc = @(
    "${env:ProgramFiles(x86)}\Inno Setup 6\ISCC.exe",
    "$env:ProgramFiles\Inno Setup 6\ISCC.exe"
) | Where-Object { Test-Path $_ } | Select-Object -First 1

if (-not $iscc) {
    $onPath = Get-Command ISCC.exe -ErrorAction SilentlyContinue
    if ($onPath) { $iscc = $onPath.Source }
}

if (-not $iscc) {
    Write-Warning 'Inno Setup 6 not found; only the portable zips were built.'
    Write-Warning 'Install it with: winget install JRSoftware.InnoSetup'
    Get-ChildItem $dist -File | Select-Object Name, Length
    return
}

# One installer, for x64 only. An ARM64 machine runs the x64 build under
# emulation perfectly well, and the ARM64 portable zip is there for anyone who
# wants native; a second installer for a rare architecture is not worth the
# extra release artifact until somebody asks.
$x64 = Join-Path $dist 'win-x64'
if (-not (Test-Path $x64)) {
    Write-Warning 'no win-x64 publish to install; skipping the installer'
    return
}

$isccVersion = (Get-Item $iscc).VersionInfo.FileVersion
# The version decides whether x64compatible is available (see installer.iss),
# so it belongs in the log rather than in a guess after the fact.
Write-Host "== building the installer with $iscc ($isccVersion)" -ForegroundColor Cyan
& $iscc `
    "/DMyAppVersion=$assemblyVersion" `
    "/DMyAppFullVersion=$Version" `
    "/DMySourceDir=$x64" `
    "/DMyOutputDir=$dist" `
    (Join-Path $PSScriptRoot 'installer.iss')
if ($LASTEXITCODE -ne 0) { throw 'ISCC failed' }

Get-ChildItem $dist -File | Select-Object Name, Length
