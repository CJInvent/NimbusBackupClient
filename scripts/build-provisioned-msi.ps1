<#
.SYNOPSIS
    Builds a preconfigured NimbusBackup MSI that enrols itself into an
    organisation on first service start.

.DESCRIPTION
    Implements the build half of docs/MSI-PROVISIONING.md. The profile is
    validated with the SAME parser the agent uses before anything is compiled,
    so a malformed profile fails here — on the machine building the installer —
    rather than at first boot on a customer endpoint, where the only symptom is
    a machine that never appears in the fleet.

    Without -Profile this produces the stock installer, byte-for-byte
    unaffected: the provisioning component is behind a WiX preprocessor guard
    and is simply not compiled in.

.PARAMETER Profile
    Path to the org install profile downloaded from NimbusControl.

.PARAMETER Version
    Product version for MSI metadata, e.g. 0.2.150.

.PARAMETER Out
    Output path. Defaults to dist\NimbusBackup-<org>.msi when a profile is
    supplied, otherwise dist\NimbusBackup.msi.

.EXAMPLE
    .\scripts\build-provisioned-msi.ps1 -Profile acme.json -Version 0.2.150

.NOTES
    The produced MSI carries a one-time org enrolment token. Treat it as a
    credential: distribute over a channel you trust, and note that until
    Authenticode signing lands (Phase 4) the installer's integrity is not
    verifiable by the endpoint.
#>
[CmdletBinding()]
param(
    [string]$Profile,
    [Parameter(Mandatory = $true)][string]$Version,
    [string]$Out
)

$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent $PSScriptRoot
$wix  = Join-Path $repo 'installer\wix'
$dist = Join-Path $repo 'dist'

if (-not (Test-Path (Join-Path $dist 'NimbusBackup.exe')) -or
    -not (Test-Path (Join-Path $dist 'NimbusBackupSVC.exe'))) {
    throw "dist\ does not contain NimbusBackup.exe and NimbusBackupSVC.exe — build the GUI and service first."
}

$candleArgs = @("-dProductVersion=$Version")

if ($Profile) {
    $profilePath = (Resolve-Path $Profile).Path

    # Validate BEFORE building, using the agent's own parser so build-time and
    # run-time cannot disagree about what a valid profile is.
    Write-Host "Validating $profilePath against the provisioning contract..."
    Push-Location (Join-Path $repo 'controlplane')
    try {
        $env:GOWORK = 'off'
        $result = & go run ./cmd/profilecheck $profilePath 2>&1
        $ok = $LASTEXITCODE -eq 0
    } finally {
        Pop-Location
    }
    $result | ForEach-Object { Write-Host "  $_" }
    if (-not $ok) {
        throw "Profile rejected. Nothing was built — fix the profile and re-run."
    }

    # Copy next to Product.wxs so the WiX source path is stable regardless of
    # where the caller keeps the profile.
    $staged = Join-Path $wix 'provisioning.json'
    Copy-Item $profilePath $staged -Force
    $candleArgs += "-dProvisioningProfile=$staged"

    if (-not $Out) {
        $org = (Get-Content $profilePath -Raw | ConvertFrom-Json).org_name
        $slug = if ($org) { ($org -replace '[^A-Za-z0-9]+', '-').Trim('-') } else { 'provisioned' }
        $Out = Join-Path $dist "NimbusBackup-$slug.msi"
    }
} elseif (-not $Out) {
    $Out = Join-Path $dist 'NimbusBackup.msi'
}

Push-Location $wix
try {
    Write-Host "candle.exe $($candleArgs -join ' ') Product.wxs"
    & candle.exe @candleArgs Product.wxs -ext WixUIExtension -ext WixUtilExtension
    if ($LASTEXITCODE -ne 0) { throw "candle.exe failed ($LASTEXITCODE)" }

    & light.exe Product.wixobj -ext WixUIExtension -ext WixUtilExtension -out $Out
    if ($LASTEXITCODE -ne 0) { throw "light.exe failed ($LASTEXITCODE)" }
} finally {
    # The staged profile holds a live enrolment token; it must not linger in
    # the source tree where it could be committed or picked up by a later build.
    $staged = Join-Path $wix 'provisioning.json'
    if (Test-Path $staged) {
        $len = (Get-Item $staged).Length
        if ($len -gt 0) {
            [System.IO.File]::WriteAllBytes($staged, (New-Object byte[] $len))
        }
        Remove-Item $staged -Force
    }
    Pop-Location
}

Write-Host ""
Write-Host "Built: $Out"
if ($Profile) {
    Write-Host "This installer contains a one-time org enrolment token. Distribute it as a credential."
}
