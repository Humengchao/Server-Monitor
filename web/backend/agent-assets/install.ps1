[CmdletBinding()]
param(
    [ValidateSet('install', 'reenroll', 'upgrade', 'uninstall')][string]$Action = 'install',
    [string]$ServerUrl,
    [string]$Ticket,
    [switch]$TicketStdin,
    [switch]$PurgeConfig
)
$ErrorActionPreference = 'Stop'
$serviceName = 'ServerMonitorAgent'
$installDir = Join-Path $env:ProgramFiles 'Server Monitor Agent'
$configDir = Join-Path $env:ProgramData 'ServerMonitorAgent'
$executable = Join-Path $installDir 'server-monitor-agent.exe'
$config = Join-Path $configDir 'config.json'
$principal = [Security.Principal.WindowsPrincipal]::new([Security.Principal.WindowsIdentity]::GetCurrent())
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) { throw 'Run PowerShell as Administrator.' }
if ([Environment]::OSVersion.Version.Build -lt 17763) { throw 'Windows 10 1809 / Windows Server 2019 or newer is required for ConPTY.' }

function Stop-AgentService {
    $service = Get-Service -Name $serviceName -ErrorAction SilentlyContinue
    if ($service -and $service.Status -ne 'Stopped') {
        Stop-Service -Name $serviceName
        $service.WaitForStatus('Stopped', [TimeSpan]::FromSeconds(30))
    }
}

if ($Action -eq 'uninstall') {
    Stop-AgentService
    if (Get-Service -Name $serviceName -ErrorAction SilentlyContinue) {
        & sc.exe delete $serviceName | Out-Null
        if ($LASTEXITCODE -ne 0) { throw 'Could not remove the Windows service.' }
    }
    if (Test-Path -LiteralPath $executable) { Remove-Item -LiteralPath $executable -Force }
    if (Test-Path -LiteralPath ($executable + '.previous')) { Remove-Item -LiteralPath ($executable + '.previous') -Force }
    if ((Test-Path -LiteralPath $installDir) -and -not (Get-ChildItem -LiteralPath $installDir -Force | Select-Object -First 1)) { Remove-Item -LiteralPath $installDir }
    if ($PurgeConfig) {
        foreach ($name in @('config.json', 'agent.log', 'agent.log.old')) {
            $path = Join-Path $configDir $name
            if (Test-Path -LiteralPath $path) { Remove-Item -LiteralPath $path -Force }
        }
        if ((Test-Path -LiteralPath $configDir) -and -not (Get-ChildItem -LiteralPath $configDir -Force | Select-Object -First 1)) { Remove-Item -LiteralPath $configDir }
    }
    Write-Host 'Agent removed. Revoke its token in the panel. Configuration is retained unless -PurgeConfig was supplied.'
    return
}

if ($Action -eq 'upgrade') {
    if (-not (Test-Path -LiteralPath $executable) -or -not (Test-Path -LiteralPath $config)) { throw 'No enrolled agent to upgrade.' }
    if (-not $ServerUrl) {
        $ServerUrl = & $executable config-url --config $config
        if ($LASTEXITCODE -ne 0) { throw 'Could not read the existing configuration.' }
    }
} elseif ($Action -eq 'install' -and (Test-Path -LiteralPath $config)) { throw 'Agent is already enrolled. Use -Action upgrade to preserve identity or reenroll with a new ticket.' }

$panel = $null
if (-not [Uri]::TryCreate($ServerUrl, [UriKind]::Absolute, [ref]$panel) -or $panel.UserInfo -or $panel.Query -or $panel.Fragment) { throw 'A valid panel URL is required.' }
if ($panel.Scheme -ne 'https' -and -not ($panel.Scheme -eq 'http' -and $panel.IsLoopback)) { throw 'Use an HTTPS panel URL.' }
$ServerUrl = $ServerUrl.TrimEnd('/')
$architecture = [Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString().ToLowerInvariant()
if ($architecture -eq 'x64') { $architecture = 'amd64' }
if ($architecture -notin @('amd64', 'arm64')) { throw 'Only Windows amd64 and arm64 are supported.' }
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
$scratch = Join-Path ([IO.Path]::GetTempPath()) ('sm-agent-' + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $scratch | Out-Null
# Re-enrollment temporarily copies a credential-bearing config here. Do not
# assume that a customized TEMP directory has private inherited permissions.
$scratchAcl = [Security.AccessControl.DirectorySecurity]::new()
$scratchAcl.SetAccessRuleProtection($true, $false)
$scratchSids = @('S-1-5-18', 'S-1-5-32-544', [Security.Principal.WindowsIdentity]::GetCurrent().User.Value)
foreach ($sid in ($scratchSids | Select-Object -Unique)) {
    $identity = [Security.Principal.SecurityIdentifier]::new($sid)
    $scratchAcl.AddAccessRule([Security.AccessControl.FileSystemAccessRule]::new($identity, 'FullControl', 'ContainerInherit,ObjectInherit', 'None', 'Allow'))
}
Set-Acl -LiteralPath $scratch -AclObject $scratchAcl
$download = Join-Path $scratch 'agent.exe'
$checksum = Join-Path $scratch 'checksum.txt'
$configBackup = Join-Path $scratch 'config.previous'
$completed = $false
$enrolled = $false
$succeeded = $false
try {
    $url = "$ServerUrl/api/agent/download/windows/$architecture"
    Invoke-WebRequest -UseBasicParsing -Uri $url -OutFile $download -TimeoutSec 180 -MaximumRedirection 0
    Invoke-WebRequest -UseBasicParsing -Uri ($url + '?checksum=1') -OutFile $checksum -TimeoutSec 30 -MaximumRedirection 0
    $expected = ((Get-Content -LiteralPath $checksum -Raw).Trim() -split '\s+')[0]
    if ($expected -notmatch '^[a-fA-F0-9]{64}$' -or (Get-FileHash -LiteralPath $download -Algorithm SHA256).Hash -ne $expected) { throw 'Agent checksum mismatch.' }
    & $download version
    if ($LASTEXITCODE -ne 0) { throw 'The downloaded binary cannot run on this machine.' }
    if ($Action -eq 'install' -or $Action -eq 'reenroll') {
        $enrollFlags = @()
        if (Test-Path -LiteralPath $config) {
            Copy-Item -LiteralPath $config -Destination $configBackup
            $enrollFlags += '--force'
        }
        New-Item -ItemType Directory -Path $configDir -Force | Out-Null
        $acl = [Security.AccessControl.DirectorySecurity]::new()
        $acl.SetAccessRuleProtection($true, $false)
        $serviceSids = @('S-1-5-18', 'S-1-5-32-544')
        $existingService = Get-CimInstance Win32_Service -Filter "Name='$serviceName'" -ErrorAction SilentlyContinue
        if ($existingService -and $existingService.StartName -ne 'LocalSystem') {
            $account = [Security.Principal.NTAccount]::new($existingService.StartName)
            $serviceSids += $account.Translate([Security.Principal.SecurityIdentifier]).Value
        }
        foreach ($sid in ($serviceSids | Select-Object -Unique)) {
            $identity = [Security.Principal.SecurityIdentifier]::new($sid)
            $acl.AddAccessRule([Security.AccessControl.FileSystemAccessRule]::new($identity, 'FullControl', 'ContainerInherit,ObjectInherit', 'None', 'Allow'))
        }
        Set-Acl -LiteralPath $configDir -AclObject $acl
        if ($TicketStdin) { $Ticket = [Console]::ReadLine() }
        if (-not $Ticket) {
            $secret = Read-Host 'Enrollment ticket' -AsSecureString
            $credential = [PSCredential]::new('ticket', $secret)
            $Ticket = $credential.GetNetworkCredential().Password
        }
        $Ticket | & $download enroll --server-url $ServerUrl --ticket-stdin --config $config @enrollFlags
        $Ticket = $null
        if ($LASTEXITCODE -ne 0) { throw 'Enrollment failed.' }
        # The panel has now revoked the previous token. The new identity must
        # survive a later binary/service rollback, even if this install fails.
        $enrolled = $true
        # enroll protects its new file for its own account. Also grant the
        # existing service identity when an administrator re-enrolls it.
        $fileAcl = Get-Acl -LiteralPath $config
        foreach ($sid in ($serviceSids | Select-Object -Unique)) {
            $identity = [Security.Principal.SecurityIdentifier]::new($sid)
            $fileAcl.AddAccessRule([Security.AccessControl.FileSystemAccessRule]::new($identity, 'FullControl', 'Allow'))
        }
        Set-Acl -LiteralPath $config -AclObject $fileAcl
    }
    & $download check --config $config
    if ($LASTEXITCODE -ne 0) { throw 'The new agent cannot read the enrolled configuration.' }
    New-Item -ItemType Directory -Path $installDir -Force | Out-Null
    Stop-AgentService
    $previous = $executable + '.previous'
    if (Test-Path -LiteralPath $previous) { Remove-Item -LiteralPath $previous -Force }
    $completed = $true
    if (Test-Path -LiteralPath $executable) { Move-Item -LiteralPath $executable -Destination $previous }
    try {
        Copy-Item -LiteralPath $download -Destination $executable
        if (-not (Get-Service -Name $serviceName -ErrorAction SilentlyContinue)) {
            $command = '"' + $executable + '" service --config "' + $config + '"'
            New-Service -Name $serviceName -BinaryPathName $command -DisplayName 'Server Monitor Agent' -StartupType Automatic | Out-Null
            & sc.exe failure $serviceName reset= 86400 actions= restart/5000/restart/15000/restart/60000 | Out-Null
            if ($LASTEXITCODE -ne 0) { throw 'Could not configure service recovery.' }
        }
        Start-Service -Name $serviceName
        (Get-Service -Name $serviceName).WaitForStatus('Running', [TimeSpan]::FromSeconds(30))
    } catch {
        Stop-AgentService
        if (Test-Path -LiteralPath $previous) {
            if (Test-Path -LiteralPath $executable) { Remove-Item -LiteralPath $executable -Force }
            Move-Item -LiteralPath $previous -Destination $executable
            Start-Service -Name $serviceName
        }
        throw
    }
    if (Test-Path -LiteralPath $previous) { Remove-Item -LiteralPath $previous -Force }
    $succeeded = $true
    Write-Host "Agent is running as its Windows service identity (LocalSystem by default). Logs: $configDir\agent.log"
} finally {
    if (-not $completed -and -not $enrolled -and (Test-Path -LiteralPath $configBackup)) { Copy-Item -LiteralPath $configBackup -Destination $config -Force }
    if (-not $succeeded -and $enrolled) { Write-Warning 'Installation failed after registration. The newly enrolled configuration was retained because the old identity has been revoked. Retry with a new enrollment command or restore the executable using the retained configuration.' }
    foreach ($path in @($download, $checksum, $configBackup)) { if (Test-Path -LiteralPath $path) { Remove-Item -LiteralPath $path -Force } }
    if (Test-Path -LiteralPath $scratch) { Remove-Item -LiteralPath $scratch }
}
