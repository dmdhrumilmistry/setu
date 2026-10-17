# setu installer for Windows (PowerShell 5.1 or 7+).
#
#   irm https://raw.githubusercontent.com/dmdhrumilmistry/setu/main/install.ps1 | iex
#
# Downloads the latest release for this machine, checks it against the
# release's SHA256SUMS, makes sure it runs, installs it to
# %LOCALAPPDATA%\Programs\setu and adds that folder to your PATH. No
# administrator rights needed, and setu can update itself later. (setu checks
# release signatures on every update; Windows PowerShell has no Ed25519 to
# check this first download's signature, so it relies on the checksum over https.)
#
# Settings (environment variables): SETU_VERSION (e.g. v0.1.0),
# SETU_INSTALL_DIR, SETU_BASE_URL (a release mirror with GitHub's layout).

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue' # Invoke-WebRequest is much faster without the progress bar
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

function Fail($msg) { Write-Host "setu install: $msg" -ForegroundColor Red; throw $msg }

$base = if ($env:SETU_BASE_URL) { $env:SETU_BASE_URL.TrimEnd('/') } else { 'https://github.com/dmdhrumilmistry/setu/releases' }
if (-not $base.StartsWith('https://')) { Fail 'SETU_BASE_URL must be an https:// URL' }

$cpu = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE }
$arch = switch ($cpu) { 'AMD64' { 'amd64' } 'ARM64' { 'arm64' } default { Fail "no release for $cpu" } }

$version = $env:SETU_VERSION
if (-not $version) {
    if ($env:SETU_BASE_URL) {
        $version = (Invoke-WebRequest -UseBasicParsing "$base/latest").Content.Trim()
    } else {
        $version = (Invoke-RestMethod -UseBasicParsing 'https://api.github.com/repos/dmdhrumilmistry/setu/releases/latest').tag_name
    }
}
if ($version -notmatch '^v\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$') { Fail "could not find the latest release (got '$version')" }

$asset = "setu-$version-windows-$arch.exe"
$tmp = Join-Path ([IO.Path]::GetTempPath()) ("setu-install-" + [Guid]::NewGuid())
New-Item -ItemType Directory $tmp | Out-Null
try {
    Write-Host "Downloading setu $version for windows/$arch..."
    $exe = Join-Path $tmp 'setu.exe'
    Invoke-WebRequest -UseBasicParsing "$base/download/$version/$asset" -OutFile $exe
    $sums = (Invoke-WebRequest -UseBasicParsing "$base/download/$version/SHA256SUMS").Content
    if ($sums -is [byte[]]) { $sums = [Text.Encoding]::UTF8.GetString($sums) }

    $want = $null
    foreach ($line in $sums -split "`n") {
        $f = $line.Trim() -split '\s+'
        if ($f.Count -eq 2 -and $f[1].TrimStart('*') -eq $asset) { $want = $f[0].ToLower() }
    }
    if (-not $want) { Fail "SHA256SUMS has no entry for $asset" }
    $got = (Get-FileHash -Algorithm SHA256 $exe).Hash.ToLower()
    if ($got -ne $want) { Fail "checksum mismatch for ${asset}: the download is corrupt or was tampered with" }

    $out = & $exe version 2>&1
    if ($LASTEXITCODE -ne 0 -or "$out" -notmatch [regex]::Escape($version)) { Fail 'the downloaded setu does not run on this machine' }

    $dir = if ($env:SETU_INSTALL_DIR) { $env:SETU_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'Programs\setu' }
    New-Item -ItemType Directory -Force $dir | Out-Null
    $dest = Join-Path $dir 'setu.exe'
    # A running setu.exe cannot be overwritten, but it can be renamed.
    if (Test-Path $dest) {
        Remove-Item -Force "$dest.old" -ErrorAction SilentlyContinue
        Move-Item -Force $dest "$dest.old"
    }
    Move-Item -Force $exe $dest
    Remove-Item -Force "$dest.old" -ErrorAction SilentlyContinue

    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    if (-not (($userPath -split ';') -contains $dir)) {
        [Environment]::SetEnvironmentVariable('Path', ((@($userPath, $dir) | Where-Object { $_ }) -join ';'), 'User')
        Write-Host "Added $dir to your PATH (open a new terminal to use it)."
    }
    if (-not (($env:Path -split ';') -contains $dir)) { $env:Path = "$env:Path;$dir" }

    Write-Host ''
    Write-Host "Installed $(& $dest version) to $dest" -ForegroundColor Green
    Write-Host ''
    Write-Host 'Next:'
    Write-Host '  setu share -- claude              share Claude Code (or any command); scan the QR code with your phone'
    Write-Host '  setu service add claude -- claude always-on access from your phone'
    Write-Host '  setu doctor                       check your setup'
} finally {
    Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}
