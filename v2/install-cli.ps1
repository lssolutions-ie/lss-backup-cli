#Requires -RunAsAdministrator
$ErrorActionPreference = "Stop"

# Force TLS 1.2 — required for go.dev and GitHub on Windows Server 2016.
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

# Fallback versions used when winget is not available.
# Update these when newer releases are available.
$GoFallbackVersion     = "1.22.5"
$ResticFallbackVersion = "0.17.3"

function Test-CommandExists {
    param([string]$Name)
    return $null -ne (Get-Command $Name -ErrorAction SilentlyContinue)
}

function Ensure-Directory {
    param([string]$Path)
    New-Item -ItemType Directory -Path $Path -Force | Out-Null
}

# Reads the system and user PATH from the registry and applies it to the
# current session. Call this after any installer that modifies PATH.
function Refresh-Path {
    $machine = [System.Environment]::GetEnvironmentVariable("Path", "Machine")
    $user    = [System.Environment]::GetEnvironmentVariable("Path", "User")
    $env:Path = ($machine, $user | Where-Object { $_ }) -join ";"
}

function Get-Arch {
    if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { return "arm64" }
    return "amd64"
}

function Install-GoFallback {
    $arch = Get-Arch
    $url  = "https://go.dev/dl/go${GoFallbackVersion}.windows-${arch}.msi"
    $msi  = Join-Path $env:TEMP "go-installer.msi"

    Write-Host "Downloading Go ${GoFallbackVersion} (${arch}) from go.dev..."
    Invoke-WebRequest -Uri $url -OutFile $msi -UseBasicParsing
    Write-Host "Running Go installer (this may take a moment)..."
    Start-Process msiexec.exe -ArgumentList "/i", $msi, "/quiet", "/norestart" -Wait
    Remove-Item $msi -Force
}

function Install-ResticFallback {
    $arch    = Get-Arch
    $url     = "https://github.com/restic/restic/releases/download/v${ResticFallbackVersion}/restic_${ResticFallbackVersion}_windows_${arch}.zip"
    $zip     = Join-Path $env:TEMP "restic-install.zip"
    $dir     = "C:\Program Files\restic"

    Write-Host "Downloading restic ${ResticFallbackVersion} (${arch}) from GitHub..."
    Invoke-WebRequest -Uri $url -OutFile $zip -UseBasicParsing
    Ensure-Directory $dir
    Expand-Archive -Path $zip -DestinationPath $dir -Force
    Remove-Item $zip -Force

    # The zip contains a versioned exe; rename it to restic.exe.
    $versioned = Join-Path $dir "restic_${ResticFallbackVersion}_windows_${arch}.exe"
    $target    = Join-Path $dir "restic.exe"
    if (Test-Path $versioned) {
        Move-Item $versioned $target -Force
    }

    # Add restic dir to the system PATH if not already present.
    $machinePath = [System.Environment]::GetEnvironmentVariable("Path", "Machine")
    if ($machinePath -notlike "*restic*") {
        [System.Environment]::SetEnvironmentVariable("Path", "$machinePath;$dir", "Machine")
    }
}

function Ensure-Dependency {
    param(
        [string]$CommandName,
        [string]$WingetPackageId,
        [System.Collections.ArrayList]$ManifestDeps,
        [scriptblock]$FallbackInstall = $null
    )

    $preexisting       = Test-CommandExists $CommandName
    $installedByProgram = $false
    $manager           = "winget"

    if (-not $preexisting) {
        if (Test-CommandExists "winget") {
            Write-Host "Installing ${CommandName} via winget..."
            winget install --id $WingetPackageId --silent --accept-package-agreements --accept-source-agreements
            Refresh-Path
        } elseif ($null -ne $FallbackInstall) {
            Write-Host "winget not available; using direct download for ${CommandName}..."
            & $FallbackInstall
            Refresh-Path
            $manager = "direct"
        } else {
            throw "Cannot install ${CommandName}: winget is not available and no fallback is defined."
        }
        $installedByProgram = $true
    }

    $null = $ManifestDeps.Add([ordered]@{
        name                = $CommandName
        manager             = $manager
        package_id          = $WingetPackageId
        previously_present  = $preexisting
        installed_by_program = $installedByProgram
    })
}

$BinDir       = "C:\Program Files\LSS Backup"
$BinPath      = Join-Path $BinDir "lss-backup-cli.exe"
$ConfigDir    = "C:\ProgramData\LSS Backup"
$JobsDir      = Join-Path $ConfigDir "jobs"
$LogsDir      = Join-Path $ConfigDir "logs"
$StateDir     = Join-Path $ConfigDir "state"
$ManifestPath = Join-Path $StateDir "install-manifest.json"

$deps = [System.Collections.ArrayList]::new()

Ensure-Dependency "go"     "GoLang.Go"     $deps { Install-GoFallback }
Ensure-Dependency "restic" "restic.restic" $deps { Install-ResticFallback }


Ensure-Directory $BinDir
Ensure-Directory $ConfigDir
Ensure-Directory $JobsDir
Ensure-Directory $LogsDir
Ensure-Directory $StateDir

Push-Location $PSScriptRoot
try {
    $env:GOCACHE = Join-Path $PSScriptRoot ".gocache"
    Ensure-Directory $env:GOCACHE

    # Refresh PATH one final time before building in case Go was just installed.
    Refresh-Path

    # Build to a temp file first so we never overwrite the running binary directly.
    # Windows locks running executables but allows renaming them out of the way.
    $TempBin = Join-Path $BinDir "lss-backup-cli-new.exe"
    $OldBin  = Join-Path $BinDir "lss-backup-cli-old.exe"

    go build -o $TempBin .

    if (Test-Path $BinPath) {
        # Try to clear a pre-existing old binary. If it is locked (held by the
        # currently-running process), fall back to a unique name so the rename
        # of the live binary never collides with it.
        if (Test-Path $OldBin) { Remove-Item $OldBin -Force -ErrorAction SilentlyContinue }
        if (Test-Path $OldBin) {
            $OldBin = Join-Path $BinDir ("lss-backup-cli-old-" + [System.Guid]::NewGuid().ToString("N") + ".exe")
        }
        Rename-Item $BinPath $OldBin
    }

    Rename-Item $TempBin $BinPath

    if (Test-Path $OldBin) { Remove-Item $OldBin -Force -ErrorAction SilentlyContinue }
}
finally {
    Pop-Location
}

$manifest = [ordered]@{
    os              = "windows"
    installed_at    = [DateTime]::UtcNow.ToString("o")
    package_manager = "winget"
    binary_path     = $BinPath
    config_dir      = $ConfigDir
    jobs_dir        = $JobsDir
    logs_dir        = $LogsDir
    state_dir       = $StateDir
    dependencies    = $deps
}

$manifest | ConvertTo-Json -Depth 5 | Set-Content -Path $ManifestPath -Encoding UTF8

Write-Host "Installed lss-backup-cli to $BinPath"

# Add the binary directory to the system PATH if not already present.
$machinePath = [System.Environment]::GetEnvironmentVariable("Path", "Machine")
if ($machinePath -notlike "*$BinDir*") {
    [System.Environment]::SetEnvironmentVariable("Path", "$machinePath;$BinDir", "Machine")
    Write-Host "Added $BinDir to system PATH"
}

# Register and start the daemon as a Task Scheduler task running as SYSTEM.
# -Force overwrites any existing task (handles reinstall/update).
$TaskPath = "\LSS Backup\"
$TaskName = "LSS Backup Daemon"

$action    = New-ScheduledTaskAction -Execute $BinPath -Argument "daemon"
$trigger   = New-ScheduledTaskTrigger -AtStartup
$settings  = New-ScheduledTaskSettingsSet `
    -RestartCount 3 `
    -RestartInterval (New-TimeSpan -Minutes 1) `
    -StartWhenAvailable `
    -ExecutionTimeLimit ([System.TimeSpan]::Zero)
$principal = New-ScheduledTaskPrincipal -UserId "SYSTEM" -RunLevel Highest

Register-ScheduledTask `
    -TaskPath $TaskPath `
    -TaskName $TaskName `
    -Action $action `
    -Trigger $trigger `
    -Settings $settings `
    -Principal $principal `
    -Force | Out-Null

# Stop any existing instance before starting fresh (reinstall case).
Stop-ScheduledTask -TaskPath $TaskPath -TaskName $TaskName -ErrorAction SilentlyContinue
Start-ScheduledTask -TaskPath $TaskPath -TaskName $TaskName
Write-Host "Daemon task registered and started (Task Scheduler)"

Write-Host "Install manifest written to $ManifestPath"
