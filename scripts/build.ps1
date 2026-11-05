# Builds savesync for Windows amd64 and Linux amd64/arm64 into dist/.
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
$targets = @(
    @{ os = "windows"; arch = "amd64"; out = "dist\savesync-windows-amd64.exe"; extra = @("-H=windowsgui") },
    @{ os = "linux"; arch = "amd64"; out = "dist\savesync-linux-amd64"; extra = @() },
    @{ os = "linux"; arch = "arm64"; out = "dist\savesync-linux-arm64"; extra = @() }
)
$savedEnv = @{}
foreach ($name in @("GOOS", "GOARCH", "CGO_ENABLED")) {
    $savedEnv[$name] = [Environment]::GetEnvironmentVariable($name, "Process")
}
try {
    $env:CGO_ENABLED = "0"
    foreach ($t in $targets) {
        $env:GOOS = $t.os
        $env:GOARCH = $t.arch
        $flags = ($Ldflags + $t.extra) -join " "
        & $Go build -trimpath -ldflags $flags -o $t.out ./cmd/savesync
        if ($LASTEXITCODE -ne 0) { throw "Build pro $($t.os)-$($t.arch) selhal (kod $LASTEXITCODE)." }
        Write-Host "built $($t.out)"
    }
    $checksums = foreach ($t in $targets) {
        $hash = (Get-FileHash -LiteralPath $t.out -Algorithm SHA256).Hash.ToLowerInvariant()
        "$hash  $(Split-Path -Leaf $t.out)"
    }
    $checksums | Set-Content -LiteralPath "dist\SHA256SUMS" -Encoding ascii
} finally {
    foreach ($name in $savedEnv.Keys) {
        [Environment]::SetEnvironmentVariable($name, $savedEnv[$name], "Process")
    }
}

Write-Host "SaveSync Ally $Version"
