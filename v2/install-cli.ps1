$ErrorActionPreference = "Stop"

function Ensure-Directory {
    param([string]$Path)
    New-Item -ItemType Directory -Path $Path -Force | Out-Null
}

function Test-CommandExists {
    param([string]$Name)
    return $null -ne (Get-Command $Name -ErrorAction SilentlyContinue)
}

function Ensure-Dependency {
    param(
        [string]$CommandName,
        [string]$PackageId,
        [System.Collections.ArrayList]$ManifestDeps
    )

    $preexisting = Test-CommandExists $CommandName
    $installedByProgram = $false

    if (-not $preexisting) {
        if (-not (Test-CommandExists "winget")) {
            throw "winget is required to install missing dependency: $CommandName"
        }

        winget install --id $PackageId --silent --accept-package-agreements --accept-source-agreements
        $installedByProgram = $true
    }

    $null = $ManifestDeps.Add([ordered]@{
        name = $CommandName
        manager = "winget"
        package_id = $PackageId
        previously_present = $preexisting
        installed_by_program = $installedByProgram
    })
}

$BinDir = "C:\Program Files\LSS Backup"
$BinPath = Join-Path $BinDir "lss-backup-cli.exe"
$ConfigDir = "C:\ProgramData\LSS Backup"
$JobsDir = Join-Path $ConfigDir "jobs"
$LogsDir = Join-Path $ConfigDir "logs"
$StateDir = Join-Path $ConfigDir "state"
$ManifestPath = Join-Path $StateDir "install-manifest.json"

$deps = [System.Collections.ArrayList]::new()

Ensure-Dependency "go" "GoLang.Go" $deps
Ensure-Dependency "restic" "restic.restic" $deps

Ensure-Directory $BinDir
Ensure-Directory $ConfigDir
Ensure-Directory $JobsDir
Ensure-Directory $LogsDir
Ensure-Directory $StateDir

Push-Location $PSScriptRoot
try {
    $env:GOCACHE = Join-Path $PSScriptRoot ".gocache"
    Ensure-Directory $env:GOCACHE

    # Build to a temp file first so we never overwrite the running binary directly.
    # Windows locks running executables, but allows renaming them out of the way.
    $TempBin = Join-Path $BinDir "lss-backup-cli-new.exe"
    $OldBin  = Join-Path $BinDir "lss-backup-cli-old.exe"

    go build -o $TempBin .

    # Rename existing binary out of the way (allowed even while the process runs).
    if (Test-Path $BinPath) {
        if (Test-Path $OldBin) { Remove-Item $OldBin -Force }
        Rename-Item $BinPath $OldBin
    }

    # Move the new binary into place.
    Rename-Item $TempBin $BinPath

    # Best-effort cleanup of the old binary.
    if (Test-Path $OldBin) { Remove-Item $OldBin -Force -ErrorAction SilentlyContinue }
}
finally {
    Pop-Location
}

$manifest = [ordered]@{
    os = "windows"
    installed_at = [DateTime]::UtcNow.ToString("o")
    package_manager = "winget"
    binary_path = $BinPath
    config_dir = $ConfigDir
    jobs_dir = $JobsDir
    logs_dir = $LogsDir
    state_dir = $StateDir
    dependencies = $deps
}

$manifest | ConvertTo-Json -Depth 5 | Set-Content -Path $ManifestPath -Encoding UTF8

Write-Host "Installed lss-backup-cli to $BinPath"
Write-Host "Install manifest written to $ManifestPath"
