# Tags, builds and publishes a release to Google Drive. Every installed
# SaveSync Ally then offers the update with one click.
#
#   powershell -ExecutionPolicy Bypass -File scripts\release.ps1 -Version 3.0.1 -Notes "Co je nového"
param(
    [Parameter(Mandatory = $true)][string]$Version,
    [string]$Notes = ""
)

$ErrorActionPreference = "Stop"
$Root = Split-Path -Parent $PSScriptRoot
Set-Location $Root

if ($Version -notmatch '^\d+\.\d+\.\d+$') { throw "Verze musí být ve tvaru X.Y.Z (např. 3.0.1)." }
$dirty = & { $ErrorActionPreference = "Continue"; git status --porcelain }
if ($dirty) { throw "V repozitáři jsou necommitnuté změny. Nejdřív je commitni." }
$existing = & { $ErrorActionPreference = "Continue"; git tag --list "v$Version" }
if ($existing) { throw "Tag v$Version už existuje." }

git tag -a "v$Version" -m "SaveSync Ally $Version"
if ($LASTEXITCODE -ne 0) { throw "git tag selhal." }

& (Join-Path $PSScriptRoot "build.ps1") -Version $Version
if ($LASTEXITCODE -ne 0) { git tag -d "v$Version" | Out-Null; throw "Build selhal." }

# The Windows binary is a GUI-subsystem program: wait for it explicitly.
$publishArgs = @("publish")
if ($Notes) { $publishArgs += @("--notes", "`"$Notes`"") }
$publishArgs += "dist"
$p = Start-Process -FilePath (Join-Path $Root "dist\savesync-windows-amd64.exe") -ArgumentList $publishArgs -NoNewWindow -Wait -PassThru
if ($p.ExitCode -ne 0) {
    git tag -d "v$Version" | Out-Null
    throw "Publikování selhalo (kód $($p.ExitCode))."
}

git push origin "v$Version"
Write-Host "SaveSync Ally $Version je venku."
