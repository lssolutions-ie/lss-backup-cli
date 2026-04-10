#Requires -RunAsAdministrator
$ErrorActionPreference = "Stop"

# Force TLS 1.2 - required for go.dev and GitHub on Windows Server 2016.
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

$daemonAccount = if ($RunAsSystem) { "SYSTEM" } else { "$env:USERDOMAIN\$env:USERNAME" }

$manifest = [ordered]@{
    os              = "windows"
    installed_at    = [DateTime]::UtcNow.ToString("o")
    package_manager = "winget"
    binary_path     = $BinPath
    config_dir      = $ConfigDir
    jobs_dir        = $JobsDir
    logs_dir        = $LogsDir
    state_dir       = $StateDir
    daemon_account  = $daemonAccount
    dependencies    = $deps
}

# Write without BOM - PowerShell 5.x Set-Content -Encoding UTF8 writes a BOM
# which breaks Go's JSON parser. Use .NET directly for BOM-free UTF-8.
$enc = New-Object System.Text.UTF8Encoding $false
[System.IO.File]::WriteAllText($ManifestPath, ($manifest | ConvertTo-Json -Depth 5), $enc)

Write-Host "Installed lss-backup-cli to $BinPath"

# Add the binary directory to the system PATH if not already present,
# then refresh the current session so lss-backup-cli is usable immediately.
$machinePath = [System.Environment]::GetEnvironmentVariable("Path", "Machine")
if ($machinePath -notlike "*$BinDir*") {
    [System.Environment]::SetEnvironmentVariable("Path", "$machinePath;$BinDir", "Machine")
    Write-Host "Added $BinDir to system PATH"
}
Refresh-Path

# Ask whether to run the daemon as SYSTEM or the current user.
Write-Host ""
Write-Host "Daemon account:"
Write-Host "  1. SYSTEM (recommended for servers/production) - runs at startup regardless"
Write-Host "     of who is logged in, full privilege, no PATH issues from user installs."
Write-Host "  2. Current user ($env:USERNAME) - inherits your PATH, easier for development"
Write-Host "     and testing. Requires you to be logged in for the daemon to run."
Write-Host ""
$modeChoice = Read-Host "Select mode [1/2] (default: 1)"
$RunAsSystem = ($modeChoice.Trim() -ne "2")

if ($RunAsSystem) {
    Write-Host "Installing daemon as SYSTEM."
} else {
    Write-Host "Installing daemon as $env:USERNAME."
}

# Register and start the daemon as a Task Scheduler task.
# -Force overwrites any existing task (handles reinstall/update).
$TaskPath = "\LSS Backup\"
$TaskName = "LSS Backup Daemon"

# Run via a hidden PowerShell window so Windows does not allocate a console
# for the Go binary. When Task Scheduler launches a console application as
# SYSTEM it immediately closes the allocated console, sending CTRL_CLOSE_EVENT
# to the process. Go's runtime catches that as os.Interrupt before our code
# can call FreeConsole(), causing the daemon to exit cleanly (exit code 0).
# A hidden PowerShell parent owns the console; the Go binary inherits it but
# never receives a close event.
$action   = New-ScheduledTaskAction `
    -Execute "powershell.exe" `
    -Argument "-NonInteractive -NoProfile -WindowStyle Hidden -Command `"& '$BinPath' daemon`""
$trigger  = New-ScheduledTaskTrigger -AtStartup
$settings = New-ScheduledTaskSettingsSet `
    -RestartCount 3 `
    -RestartInterval (New-TimeSpan -Minutes 1) `
    -StartWhenAvailable `
    -MultipleInstances IgnoreNew `
    -ExecutionTimeLimit ([System.TimeSpan]::Zero)

if ($RunAsSystem) {
    $principal = New-ScheduledTaskPrincipal -UserId "SYSTEM" -RunLevel Highest
} else {
    # S4U allows the task to run as the user without a stored password.
    # The task inherits the user's full environment including PATH.
    $principal = New-ScheduledTaskPrincipal `
        -UserId "$env:USERDOMAIN\$env:USERNAME" `
        -RunLevel Highest `
        -LogonType S4U
}

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
$modeLabel = if ($RunAsSystem) { "SYSTEM" } else { $env:USERNAME }
Write-Host "Daemon task registered and started as $modeLabel (Task Scheduler)"

Write-Host "Install manifest written to $ManifestPath"
