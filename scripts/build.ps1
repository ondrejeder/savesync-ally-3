# Builds savesync for Windows and Linux (amd64) into dist/.
# Pure Go (CGO disabled), so no MSYS2/WSL is needed.
param(
    [string]$Version = ""
)

$ErrorActionPreference = "Stop"
$Root = Split-Path -Parent $PSScriptRoot
Set-Location $Root

$Go = "go"
$LocalGo = Join-Path (Split-Path -Parent $Root) "tools\go1.26.2\bin\go.exe"
if (-not (Get-Command go -ErrorAction SilentlyContinue) -and (Test-Path $LocalGo)) {
    $Go = $LocalGo
}

if (-not $Version) {
    $Version = & { $ErrorActionPreference = "Continue"; git describe --tags --always --dirty 2>$null }
    if (-not $Version) { $Version = "0.0.0-dev" }
    $Version = $Version.TrimStart("v")
}

$Ldflags = @("-s", "-w", "-X", "main.version=$Version")
$EnvFile = Join-Path $Root ".env.local"
if (Test-Path $EnvFile) {
    foreach ($line in Get-Content -LiteralPath $EnvFile) {
        if ($line -match '^SAVESYNC_GOOGLE_CLIENT_ID=(.+)$') {
            $Ldflags += @("-X", "github.com/ondrejeder/savesync-ally-3/internal/drive.clientID=$($Matches[1].Trim())")
        }
        if ($line -match '^SAVESYNC_GOOGLE_CLIENT_SECRET=(.+)$') {
            $Ldflags += @("-X", "github.com/ondrejeder/savesync-ally-3/internal/drive.clientSecret=$($Matches[1].Trim())")
        }
    }
}

New-Item -ItemType Directory -Force -Path "dist" | Out-Null
$env:CGO_ENABLED = "0"
$env:GOARCH = "amd64"

$targets = @(
    @{ os = "windows"; out = "dist\savesync-windows-amd64.exe"; extra = @("-H=windowsgui") },
    @{ os = "linux";   out = "dist\savesync-linux-amd64";       extra = @() }
)
foreach ($t in $targets) {
    $env:GOOS = $t.os
    $flags = ($Ldflags + $t.extra) -join " "
    & $Go build -trimpath -ldflags $flags -o $t.out ./cmd/savesync
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    Write-Host "built $($t.out)"
}
Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED

Write-Host "SaveSync Ally $Version"
