#Requires -RunAsAdministrator
$ErrorActionPreference = "Stop"

# Force TLS 1.2 - required for go.dev and GitHub on Windows Server 2016.
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

# Fallback versions used when winget is not available.
# Update these when newer releases are available.
$GoFallbackVersion     = "1.22.5"
$ResticFallbackVersion = "0.18.1"

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

function Get-ResticArch {
    # Restic doesn't publish windows/arm64 builds. Windows on ARM runs
    # amd64 binaries via emulation, so fall back to amd64.
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
    $arch    = Get-ResticArch
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
        $newResticPath = $machinePath + ';' + $dir
        [System.Environment]::SetEnvironmentVariable('Path', $newResticPath, [System.EnvironmentVariableTarget]::Machine)
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

# Only install Go when building from source (go.mod present).
if (Test-Path (Join-Path $PSScriptRoot "go.mod")) {
    Ensure-Dependency "go" "GoLang.Go" $deps { Install-GoFallback }
}
Ensure-Dependency "restic" "restic.restic" $deps { Install-ResticFallback }


# SSH server - required for management server terminal access.
$sshCapability = Get-WindowsCapability -Online | Where-Object { $_.Name -like 'OpenSSH.Server*' }
if ($sshCapability.State -ne 'Installed') {
    Write-Host "Installing OpenSSH Server..."
    Add-WindowsCapability -Online -Name OpenSSH.Server~~~~0.0.1.0 | Out-Null
}
$sshdService = Get-Service -Name sshd -ErrorAction SilentlyContinue
if ($sshdService) {
    if ($sshdService.Status -ne 'Running') {
        Start-Service sshd
    }
    Set-Service -Name sshd -StartupType Automatic
} else {
    Write-Host "[WARN] sshd service not found after install - check Windows version."
}
# Ensure firewall rule for SSH.
$sshRule = Get-NetFirewallRule -Name 'sshd' -ErrorAction SilentlyContinue
if (-not $sshRule) {
    New-NetFirewallRule -Name sshd -DisplayName 'OpenSSH Server (LSS Backup)' `
        -Enabled True -Direction Inbound -Protocol TCP -Action Allow -LocalPort 22 | Out-Null
    Write-Host "SSH firewall rule added (port 22)"
}
Write-Host "SSH server ready"

Ensure-Directory $BinDir
Ensure-Directory $ConfigDir
Ensure-Directory $JobsDir
Ensure-Directory $LogsDir
Ensure-Directory $StateDir

# Detect build mode: source build when go.mod is present, binary download otherwise.
$SourceBuild = Test-Path (Join-Path $PSScriptRoot "go.mod")

if ($SourceBuild) {
    Write-Host "Building from source..."
    Push-Location $PSScriptRoot
    try {
        $env:GOCACHE = Join-Path $PSScriptRoot ".gocache"
        Ensure-Directory $env:GOCACHE
        Refresh-Path

        $TempBin = Join-Path $BinDir "lss-backup-cli-new.exe"
        $OldBin  = Join-Path $BinDir "lss-backup-cli-old.exe"

        go build -o $TempBin .

        if (Test-Path $BinPath) {
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
} else {
    Write-Host "Downloading pre-built binary from GitHub Releases..."
    $arch = Get-Arch
    $tag = (Invoke-RestMethod "https://api.github.com/repos/lssolutions-ie/lss-backup-cli/releases/latest").tag_name
    $assetName = "lss-backup-cli-windows-${arch}.exe"
    $url = "https://github.com/lssolutions-ie/lss-backup-cli/releases/download/${tag}/${assetName}"
    $TempBin = Join-Path $BinDir "lss-backup-cli-new.exe"

    Invoke-WebRequest -Uri $url -OutFile $TempBin -UseBasicParsing

    if (Test-Path $BinPath) {
        $OldBin = Join-Path $BinDir ("lss-backup-cli-old-" + (Get-Date -Format 'yyyyMMddHHmmss') + ".exe")
        Rename-Item $BinPath $OldBin -ErrorAction SilentlyContinue
    }

    Rename-Item $TempBin $BinPath
    Write-Host "Installed lss-backup-cli ${tag} to ${BinPath}"
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
    $newPath = $machinePath + ';' + $BinDir
    [System.Environment]::SetEnvironmentVariable('Path', $newPath, [System.EnvironmentVariableTarget]::Machine)
    Write-Host "Added $BinDir to system PATH"
}
Refresh-Path

# Ask whether to run the daemon as SYSTEM or the current user.
Write-Host ""
# Non-interactive (piped via irm | iex or SSH): default to SYSTEM.
# Interactive: prompt the operator.
$isInteractive = [Environment]::UserInteractive -and -not [Console]::IsInputRedirected
if ($isInteractive) {
    Write-Host "Daemon account:"
    Write-Host "  1. SYSTEM (recommended for servers/production) - runs at startup regardless"
    Write-Host "     of who is logged in, full privilege, no PATH issues from user installs."
    Write-Host "  2. Current user ($env:USERNAME) - inherits your PATH, easier for development"
    Write-Host "     and testing. Requires you to be logged in for the daemon to run."
    Write-Host ""
    $modeChoice = Read-Host "Select mode [1/2] (default: 1)"
    if ([string]::IsNullOrWhiteSpace($modeChoice)) { $modeChoice = "1" }
} else {
    $modeChoice = "1"
    Write-Host "Installing daemon as SYSTEM (non-interactive mode)."
}
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
$daemonArg = '-NonInteractive -NoProfile -WindowStyle Hidden -Command ' + "'" + $BinPath + ' daemon' + "'"
$action   = New-ScheduledTaskAction `
    -Execute 'powershell.exe' `
    -Argument $daemonArg
$trigger  = New-ScheduledTaskTrigger -AtStartup
$settings = New-ScheduledTaskSettingsSet `
    -RestartCount 999 `
    -RestartInterval (New-TimeSpan -Minutes 1) `
    -StartWhenAvailable `
    -AllowStartIfOnBatteries `
    -DontStopIfGoingOnBatteries `
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

# Stop any existing instance (reinstall case).
Stop-ScheduledTask -TaskPath $TaskPath -TaskName $TaskName -ErrorAction SilentlyContinue

$modeLabel = if ($RunAsSystem) { "SYSTEM" } else { $env:USERNAME }
Write-Host "Daemon task registered and started as $modeLabel (Task Scheduler)"

Write-Host "Install manifest written to $ManifestPath"

# Server-assisted auto-configure BEFORE starting the daemon so config.toml
# exists when the daemon starts. Otherwise the daemon loads empty config
# and doesn't report to the server.
if ($env:LSS_SERVER_URL -and $env:LSS_NODE_UID -and $env:LSS_PSK_KEY) {
    if ($env:LSS_RECOVERY_MODE -eq "true") {
        Write-Host ""
        Write-Host "Recovery mode detected - restoring from DR backup..."
        Write-Host ""
        & $BinPath --setup-recover
        Write-Host ""
        Write-Host "Node recovered. Daemon starting."
    } else {
        Write-Host ""
        Write-Host "Server-assisted setup detected - auto-configuring..."
        Write-Host ""
        & $BinPath --setup-auto
        Write-Host ""
        Write-Host "Node will register with server on first heartbeat."
    }
} else {
    # Manual path - interactive SSH credential setup.
    Write-Host ""
    Write-Host "Setting up SSH credentials for remote management..."
    Write-Host ""
    & $BinPath --setup-ssh
}

# Start the daemon AFTER config is written.
Start-ScheduledTask -TaskPath $TaskPath -TaskName $TaskName
