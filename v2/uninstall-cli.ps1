#Requires -RunAsAdministrator
$ErrorActionPreference = "Stop"

function Prompt-YesNo {
    param([string]$Question)

    while ($true) {
        $answer = Read-Host "$Question (y/n)"
        switch ($answer.ToLowerInvariant()) {
            "y" { return $true }
            "n" { return $false }
            default { Write-Host "Please answer y or n." }
        }
    }
}

function Prompt-ZipPath {
    while ($true) {
        $path = Read-Host "Where should the backup zip be created? Example: C:\Temp\lss-backup-recovery.zip"
        if (-not $path.EndsWith(".zip")) {
            Write-Host "Backup file must end with .zip"
            continue
        }

        $parent = Split-Path -Parent $path
        if (-not (Test-Path $parent)) {
            Write-Host "Parent directory does not exist: $parent"
            continue
        }

        return $path
    }
}

function Safe-Remove {
    param([string]$Target)

    if ([string]::IsNullOrWhiteSpace($Target) -or $Target -eq "\" -or $Target -eq "/") {
        Write-Warning "Refusing to remove unsafe path: $Target"
        return
    }

    if (Test-Path $Target) {
        try {
            Remove-Item -Recurse -Force $Target
            Write-Host "Removed: $Target"
        } catch {
            Write-Warning "Could not remove ${Target}: $_"
        }
    }
    else {
        Write-Host "Not present, skipping: $Target"
    }
}

$BinDir  = "C:\Program Files\LSS Backup"
$BinPath = Join-Path $BinDir "lss-backup-cli.exe"
$ConfigDir = "C:\ProgramData\LSS Backup"
$LogsDir = "C:\ProgramData\LSS Backup\logs"
$StateDir = "C:\ProgramData\LSS Backup\state"

Write-Host "LSS Backup CLI Uninstall"
Write-Host "========================"
Write-Host "Binary: $BinPath"
Write-Host "Config: $ConfigDir"
Write-Host "Logs:   $LogsDir"
Write-Host "State:  $StateDir"
Write-Host ""

if (Prompt-YesNo "Do you want to back up LSS Backup data before uninstalling?") {
    $zipPath = Prompt-ZipPath
    $stageDir = Join-Path ([System.IO.Path]::GetTempPath()) ("lss-backup-uninstall-" + [System.Guid]::NewGuid().ToString())
    $payloadDir = Join-Path $stageDir "recovery"

    New-Item -ItemType Directory -Path $payloadDir -Force | Out-Null

    if (Test-Path $BinPath) {
        Copy-Item $BinPath (Join-Path $payloadDir "lss-backup-cli.exe") -Force
    }
    if (Test-Path $ConfigDir) {
        Copy-Item $ConfigDir (Join-Path $payloadDir "config") -Recurse -Force
    }
    if (Test-Path $LogsDir) {
        Copy-Item $LogsDir (Join-Path $payloadDir "logs") -Recurse -Force
    }
    if (Test-Path $StateDir) {
        Copy-Item $StateDir (Join-Path $payloadDir "state") -Recurse -Force
    }

    if (Test-Path $zipPath) {
        Remove-Item $zipPath -Force
    }

    Compress-Archive -Path $payloadDir -DestinationPath $zipPath
    Remove-Item $stageDir -Recurse -Force
    Write-Host "Backup created at: $zipPath"
}

# Stop and remove the daemon task before removing files.
$TaskPath = "\LSS Backup\"
$TaskName = "LSS Backup Daemon"
if (Get-ScheduledTask -TaskPath $TaskPath -TaskName $TaskName -ErrorAction SilentlyContinue) {
    Stop-ScheduledTask -TaskPath $TaskPath -TaskName $TaskName -ErrorAction SilentlyContinue
    Unregister-ScheduledTask -TaskPath $TaskPath -TaskName $TaskName -Confirm:$false
    Write-Host "Daemon task stopped and removed (Task Scheduler)"
}

# Kill any orphaned daemon processes that survived the task stop.
Get-Process -Name "lss-backup-cli" -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue

# Remove the binary directory and clean it from the system PATH.
$machinePath = [System.Environment]::GetEnvironmentVariable("Path", "Machine")
$cleanedPath = ($machinePath -split ";") | Where-Object { $_.TrimEnd("\") -ne $BinDir.TrimEnd("\") }
[System.Environment]::SetEnvironmentVariable("Path", ($cleanedPath -join ";"), "Machine")

Safe-Remove $BinDir
Safe-Remove $ConfigDir
Safe-Remove $LogsDir

if ($StateDir -ne $ConfigDir -and $StateDir -ne (Join-Path $ConfigDir "state")) {
    Safe-Remove $StateDir
}

Write-Host "LSS Backup CLI uninstall complete."
