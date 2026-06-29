# SPDX-License-Identifier: Apache-2.0
#
# suctl installer (Windows). Fetches the latest release, verifies its checksum,
# and delegates to `suctl install` — which owns the on-disk layout, so this
# script never hardcodes install paths.
#
#   irm https://suctl.com/install.ps1 | iex
#
# Pin a version:   $env:SUCTL_VERSION = 'v0.5.0'   (default: latest release)
# Use a fork:      $env:SUCTL_REPO    = 'owner/name'
#Requires -Version 5.1
$ErrorActionPreference = 'Stop'
$ProgressPreference    = 'SilentlyContinue'

$repo = if ($env:SUCTL_REPO) { $env:SUCTL_REPO } else { 'solutionsunity/suctl' }
$gh   = "https://github.com/$repo"

function Fail($msg) { Write-Host "`n  error: $msg`n" -ForegroundColor Red; exit 1 }

# --- platform detection -----------------------------------------------------
$os = 'windows'
$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
  'AMD64' { 'amd64' }
  'ARM64' { 'arm64' }
  default { Fail "unsupported architecture '$($env:PROCESSOR_ARCHITECTURE)'" }
}
if ($arch -ne 'amd64') { Fail "no suctl release is published for windows/$arch yet" }

# --- resolve version --------------------------------------------------------
# Honour an explicit pin; otherwise read the releases/latest 302 Location.
# No API call, no rate limit — and works identically on PowerShell 5.1 and 7.
if ($env:SUCTL_VERSION) {
  $tag = $env:SUCTL_VERSION
} else {
  Add-Type -AssemblyName System.Net.Http
  $handler = [System.Net.Http.HttpClientHandler]::new()
  $handler.AllowAutoRedirect = $false
  $client = [System.Net.Http.HttpClient]::new($handler)
  try {
    $resp = $client.GetAsync("$gh/releases/latest").GetAwaiter().GetResult()
    $loc  = $resp.Headers.Location
    if (-not $loc) { Fail 'could not resolve the latest version' }
    $tag = ($loc.ToString() -split '/')[-1]
  } finally { $client.Dispose() }
}
if ($tag -notmatch '^v') { Fail "could not determine a valid version (got '$tag')" }

$name = "suctl-$tag-$os-$arch.zip"
$url  = "$gh/releases/download/$tag/$name"

Write-Host ''
Write-Host "  ==> Installing suctl $tag ($os/$arch)"

# --- require elevation (suctl install writes under %ProgramData%) ------------
$isAdmin = ([Security.Principal.WindowsPrincipal] `
  [Security.Principal.WindowsIdentity]::GetCurrent()
).IsInRole([Security.Principal.WindowsBuiltinRole]::Administrator)
if (-not $isAdmin) {
  Fail 'must run as Administrator — re-run this command from an elevated PowerShell'
}

# --- download + verify ------------------------------------------------------
$tmp = New-Item -ItemType Directory -Path (Join-Path $env:TEMP ("suctl-" + [guid]::NewGuid()))
try {
  $zip = Join-Path $tmp $name
  $sha = "$zip.sha256"
  Invoke-WebRequest -Uri $url          -OutFile $zip -UseBasicParsing
  Invoke-WebRequest -Uri "$url.sha256" -OutFile $sha -UseBasicParsing

  $expected = ((Get-Content $sha -Raw).Trim() -split '\s+')[0]
  $actual   = (Get-FileHash -Algorithm SHA256 -Path $zip).Hash
  if ($actual.ToLower() -ne $expected.ToLower()) {
    Fail "checksum verification failed for $name"
  }
  Write-Host '      checksum ok'

  # --- extract --------------------------------------------------------------
  Expand-Archive -Path $zip -DestinationPath $tmp -Force
  $dir = Join-Path $tmp "suctl-$tag-$os-$arch"
  $exe = Join-Path $dir 'suctl.exe'
  if (-not (Test-Path $exe)) { Fail 'extracted archive is missing suctl.exe' }

  # --- delegate to `suctl install` ------------------------------------------
  & $exe install
  if ($LASTEXITCODE -ne 0) { Fail "suctl install exited with code $LASTEXITCODE" }
} finally {
  Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}
