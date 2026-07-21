param(
  [string]$Version = "latest",
  [string]$Repo = "Don-Works/mcplexer",
  [string]$BinDir = "$HOME\.mcplexer\bin",
  [switch]$NoSetup
)

$ErrorActionPreference = "Stop"

function Resolve-Arch {
  switch ($env:PROCESSOR_ARCHITECTURE.ToLowerInvariant()) {
    "amd64" { "amd64"; break }
    "arm64" { "arm64"; break }
    default { throw "unsupported architecture: $env:PROCESSOR_ARCHITECTURE" }
  }
}

if ($Version -eq "latest") {
  $latest = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases/latest"
  $Version = $latest.tag_name
}
if (-not $Version) {
  throw "could not determine release version"
}

$arch = Resolve-Arch
$asset = "mcplexer_${Version}_windows_${arch}.zip"
$base = "https://github.com/$Repo/releases/download/$Version"
$tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("mcplexer-install-" + [System.Guid]::NewGuid().ToString("n"))
New-Item -ItemType Directory -Path $tmp | Out-Null

try {
  $zipPath = Join-Path $tmp $asset
  $sumPath = Join-Path $tmp "checksums.txt"

  Write-Host "==> Downloading $asset"
  Invoke-WebRequest -Uri "$base/$asset" -OutFile $zipPath
  Invoke-WebRequest -Uri "$base/checksums.txt" -OutFile $sumPath

  Write-Host "==> Verifying checksum"
  $expected = (Select-String -Path $sumPath -Pattern "\./$asset$").Line.Split(" ")[0]
  $actual = (Get-FileHash -Algorithm SHA256 -Path $zipPath).Hash.ToLowerInvariant()
  if ($actual -ne $expected.ToLowerInvariant()) {
    throw "checksum mismatch for $asset"
  }

  Expand-Archive -Path $zipPath -DestinationPath $tmp -Force
  $src = Join-Path $tmp "mcplexer_${Version}_windows_${arch}\mcplexer.exe"
  New-Item -ItemType Directory -Path $BinDir -Force | Out-Null
  $dst = Join-Path $BinDir "mcplexer.exe"
  Copy-Item -Path $src -Destination $dst -Force

  Write-Host "==> Installed $(& $dst version)"
  if (-not $NoSetup) {
    & $dst setup
  }
} finally {
  Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}
