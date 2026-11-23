#requires -Version 5.1
# Helper for win-phone-mode. Each subcommand prints a small, stable, parseable
# result so the Go side never has to scrape human-readable tables.
#
# Usage: win.ps1 <command> [args]

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

function Get-Radios {
    Get-PnpDevice -Class Bluetooth -ErrorAction SilentlyContinue |
        ForEach-Object {
            [pscustomobject]@{
                instance_id = $_.InstanceId
                description = $_.FriendlyName
                disabled    = ($_.Status -eq 'Error' -or $_.Status -eq 'Unknown')
                status      = [string]$_.Status
            }
        } | ConvertTo-Json -Compress -AsArray
}

function Get-Displays {
    Get-PnpDevice -Class Display -ErrorAction SilentlyContinue |
        ForEach-Object {
            [pscustomobject]@{
                instance_id = $_.InstanceId
                description = $_.FriendlyName
                disabled    = ($_.Status -eq 'Error' -or $_.Status -eq 'Unknown')
                status      = [string]$_.Status
            }
        } | ConvertTo-Json -Compress -AsArray
}

function Get-Brightness {
    $m = Get-CimInstance -Namespace root/wmi -ClassName WmiMonitorBrightness -ErrorAction SilentlyContinue |
        Select-Object -First 1
    if (-not $m) { Write-Output 'unsupported'; exit 0 }
    Write-Output ([string]$m.CurrentBrightness)
}

function Set-Brightness([int]$Percent) {
    $methods = Get-CimInstance -Namespace root/wmi -ClassName WmiMonitorBrightnessMethods -ErrorAction SilentlyContinue
    if (-not $methods) { Write-Output 'unsupported'; exit 1 }
    foreach ($m in $methods) {
        Invoke-CimMethod -InputObject $m -MethodName WmiSetBrightness `
            -Arguments @{ Timeout = 0; Brightness = [byte]$Percent } | Out-Null
    }
    Write-Output 'ok'
}

# A single quote in a WQL literal must be doubled, exactly as in SQL. Without
# this a service name containing ' breaks the filter (making the service look
# missing) or can match a different service to stop or disable.
function Quote-WqlLiteral([string]$Value) {
    return $Value.Replace("'", "''")
}

function Get-ServiceState([string]$Name) {
    # Win32_Service is used instead of Get-Service because ServiceController
    # does not reliably expose the start mode; reading it from CIM gives both
    # the run state and the start mode in one call.
    $svc = Get-CimInstance -ClassName Win32_Service -Filter ("Name='" + (Quote-WqlLiteral $Name) + "'") -ErrorAction SilentlyContinue |
        Select-Object -First 1
    if (-not $svc) {
        Write-Output '{"missing":true}'
        return
    }
    $start = 'manual'
    switch ([string]$svc.StartMode) {
        'Auto'     { $start = 'auto' }
        'Automatic' { $start = 'auto' }
        'Disabled' { $start = 'disabled' }
    }
    [pscustomobject]@{
        running = ([string]$svc.State -eq 'Running')
        start   = $start
        missing = $false
    } | ConvertTo-Json -Compress
}

function Set-ServiceStart([string]$Name, [string]$Want) {
    $svc = Get-CimInstance -ClassName Win32_Service -Filter ("Name='" + (Quote-WqlLiteral $Name) + "'") -ErrorAction SilentlyContinue |
        Select-Object -First 1
    if (-not $svc) { Write-Output 'missing'; exit 1 }
    $mode = switch ($Want) {
        'auto'     { 'Automatic' }
        'manual'   { 'Manual' }
        'disabled' { 'Disabled' }
        default    { Write-Output "unknown start mode $Want"; exit 1 }
    }
    Set-Service -Name $Name -StartupType $mode -ErrorAction Stop
    Write-Output 'ok'
}

function Set-ServiceState([string]$Name, [string]$Want) {
    $svc = Get-CimInstance -ClassName Win32_Service -Filter ("Name='" + (Quote-WqlLiteral $Name) + "'") -ErrorAction SilentlyContinue |
        Select-Object -First 1
    if (-not $svc) { Write-Output 'missing'; exit 1 }
    switch ($Want) {
        'running' {
            if ([string]$svc.State -ne 'Running') { Start-Service -Name $Name -ErrorAction Stop }
        }
        'stopped' {
            if ([string]$svc.State -eq 'Running') { Stop-Service -Name $Name -Force -ErrorAction Stop }
        }
        default { Write-Output "unknown target $Want"; exit 1 }
    }
    Write-Output 'ok'
}

# Get-Process -Name matches wildcards, so a configured shell name containing
# * would match every process (and then Stop-Process would kill them all).
function Get-ShellProcess([string]$Process) {
    $exe = [System.IO.Path]::GetFileNameWithoutExtension($Process)
    return Get-Process -Name ([System.Management.Automation.WildcardPattern]::Escape($exe)) -ErrorAction SilentlyContinue
}

function Get-DesktopShell([string]$Process) {
    $p = Get-ShellProcess $Process
    if ($p) { Write-Output 'running' } else { Write-Output 'stopped' }
}

function Set-DesktopShell([string]$Process, [string]$Want) {
    $exe = [System.IO.Path]::GetFileNameWithoutExtension($Process)
    $p = Get-ShellProcess $Process
    switch ($Want) {
        'stopped' {
            # explorer.exe does not auto-restart by default on modern Windows,
            # so stopping it really does leave the session with no shell. That
            # is the whole point of phone mode, and "off" starts it again.
            # Failing to stop it must be an error: reporting success while the
            # desktop is still up would leave the user with no idea why.
            if ($p) { $p | Stop-Process -Force -ErrorAction Stop }
        }
        'running' {
            if (-not $p) { if ($exe -eq 'explorer') { Start-Process explorer.exe } else { Start-Process $Process } }
        }
        default { Write-Output "unknown target $Want"; exit 1 }
    }
    Write-Output 'ok'
}

function Set-Device([string]$InstanceID, [string]$Want) {
    switch ($Want) {
        'disabled' { Disable-PnpDevice -InstanceId $InstanceID -Confirm:$false -ErrorAction Stop }
        'enabled'  { Enable-PnpDevice  -InstanceId $InstanceID -Confirm:$false -ErrorAction Stop }
        default    { Write-Output "unknown target $Want"; exit 1 }
    }
    Write-Output 'ok'
}

$command = $args[0]
$rest = @()
if ($args.Length -gt 1) { $rest = $args[1..($args.Length - 1)] }

switch ($command) {
    'get-radios'      { Get-Radios }
    'get-displays'    { Get-Displays }
    'get-brightness'  { Get-Brightness }
    'set-brightness'  { Set-Brightness ([int]$rest[0]) }
    'get-service'     { Get-ServiceState $rest[0] }
    'set-service'     { Set-ServiceState $rest[0] $rest[1] }
    'set-service-start' { Set-ServiceStart $rest[0] $rest[1] }
    'get-desktop'     { Get-DesktopShell $rest[0] }
    'set-desktop'     { Set-DesktopShell $rest[0] $rest[1] }
    'set-device'      { Set-Device $rest[0] $rest[1] }
    default           { Write-Output "unknown command $command"; exit 2 }
}
