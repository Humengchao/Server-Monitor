# Runs the installer against temporary paths, mock service/network functions,
# and native credential-free fixture binaries. Never touches a real service.
param([Parameter(Mandatory=$true)][string]$FixtureRoot)
$ErrorActionPreference = 'Stop'
$source = Get-Content -LiteralPath (Join-Path $PSScriptRoot 'install.ps1') -Raw
$adminGuard = "if (-not `$principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) { throw 'Run PowerShell as Administrator.' }"
if (-not $source.Contains($adminGuard)) { throw 'Installer privilege guard changed; update the isolated test explicitly.' }
$fixtureInstaller = Join-Path $FixtureRoot 'fixture-install.ps1'
# The production privilege check is irrelevant to mocks. Everything else,
# including Windows paths with spaces and native exit codes, remains intact.
[IO.File]::WriteAllText($fixtureInstaller, $source.Replace($adminGuard, ''))
$agentSource = @'
using System;
using System.IO;
public static class CLASSNAME {
  public static int Main(string[] args) {
    if (args.Length == 0) return 2;
    string config = null;
    for (int i=1; i+1<args.Length; i++) if (args[i] == "--config") config = args[i+1];
    switch(args[0]) {
      case "version": Console.WriteLine("VERSION"); return 0;
      case "config-url": Console.WriteLine("https://panel.invalid"); return 0;
      case "check":
        if (Environment.GetEnvironmentVariable("AGENT_FIXTURE_FAILURE") == "check" && "VERSION" == "new") return 41;
        return File.Exists(config) ? 0 : 1;
      case "enroll":
        string ticket = Console.ReadLine();
        if (ticket == "rejected-fixture") return 1;
        File.WriteAllText(config, ticket);
        File.WriteAllText(Path.Combine(Environment.GetEnvironmentVariable("AGENT_FIXTURE_CASE"), "registered.identity"), ticket);
        return 0;
      default: return 2;
    }
  }
}
'@
$global:AgentFixtureOldBinary = Join-Path $FixtureRoot 'old-agent.exe'
$global:AgentFixtureNewBinary = Join-Path $FixtureRoot 'new-agent.exe'
Add-Type -TypeDefinition ($agentSource.Replace('CLASSNAME', 'OldFixtureAgent').Replace('VERSION', 'old')) -OutputAssembly $global:AgentFixtureOldBinary -OutputType ConsoleApplication
Add-Type -TypeDefinition ($agentSource.Replace('CLASSNAME', 'NewFixtureAgent').Replace('VERSION', 'new')) -OutputAssembly $global:AgentFixtureNewBinary -OutputType ConsoleApplication

function Invoke-WebRequest {
    param([switch]$UseBasicParsing, $Uri, $OutFile, $TimeoutSec, $MaximumRedirection)
    if ([string]$Uri -notlike 'https://panel.invalid/api/agent/download/windows/*') { throw 'Unexpected fixture URL.' }
    if ([string]$Uri -like '*?checksum=1') {
        [IO.File]::WriteAllText($OutFile, (Get-FileHash -LiteralPath $global:AgentFixtureNewBinary -Algorithm SHA256).Hash)
    } else { Copy-Item -LiteralPath $global:AgentFixtureNewBinary -Destination $OutFile }
}
function Get-Service { param($Name, $ErrorAction) return $global:AgentFixtureService }
function Stop-Service { param($Name) $global:AgentFixtureService.Status = 'Stopped' }
function Start-Service {
    param($Name)
    $installed = Join-Path $env:ProgramFiles 'Server Monitor Agent\server-monitor-agent.exe'
    if ($env:AGENT_FIXTURE_FAILURE -eq 'start' -and (& $installed version) -eq 'new') { throw 'Fixture service start failed.' }
    $current = [IO.File]::ReadAllText((Join-Path $env:ProgramData 'ServerMonitorAgent\config.json'))
    $registered = [IO.File]::ReadAllText((Join-Path $env:AGENT_FIXTURE_CASE 'registered.identity'))
    if ($current -ne $registered) { throw 'Rollback restored a revoked identity.' }
    $global:AgentFixtureService.Status = 'Running'
}
function Get-CimInstance { param($ClassName, $Filter, $ErrorAction) return [pscustomobject]@{StartName = [Security.Principal.WindowsIdentity]::GetCurrent().Name} }
# ACLs are covered by the config tests. Keep this test usable without elevation.
function Set-Acl { param($LiteralPath, $AclObject) }
function Get-Acl { param($LiteralPath) return [Security.AccessControl.FileSecurity]::new() }

foreach ($failure in @('check', 'start', 'reject', 'none')) {
    $case = Join-Path $FixtureRoot $failure
    $env:AGENT_FIXTURE_CASE = $case
    $env:AGENT_FIXTURE_FAILURE = $failure
    $env:ProgramFiles = Join-Path $case 'Program Files'
    $env:ProgramData = Join-Path $case 'Program Data'
    $env:TEMP = Join-Path $case 'Temporary Files'
    $env:TMP = $env:TEMP
    $installed = Join-Path $env:ProgramFiles 'Server Monitor Agent\server-monitor-agent.exe'
    $config = Join-Path $env:ProgramData 'ServerMonitorAgent\config.json'
    foreach ($directory in @((Split-Path $installed), (Split-Path $config), $env:TEMP)) { New-Item -ItemType Directory -Path $directory -Force | Out-Null }
    Copy-Item -LiteralPath $global:AgentFixtureOldBinary -Destination $installed
    [IO.File]::WriteAllText($config, 'previous-fixture')
    [IO.File]::WriteAllText((Join-Path $case 'registered.identity'), 'previous-fixture')
    $global:AgentFixtureService = [pscustomobject]@{Status = 'Running'}
    $global:AgentFixtureService | Add-Member -MemberType ScriptMethod -Name WaitForStatus -Value {
        param($state, $timeout)
        if ($this.Status -ne $state) { throw 'Unexpected fixture service state.' }
    }
    $ticket = if ($failure -eq 'reject') { 'rejected-fixture' } else { 'replacement-fixture' }
    $failed = $false
    $failureMessage = ''
    $AgentFixtureWarnings = @()
    try { & $fixtureInstaller -Action reenroll -ServerUrl 'https://panel.invalid' -Ticket $ticket -WarningVariable AgentFixtureWarnings }
    catch { $failed = $true; $failureMessage = $_.Exception.Message }
    if ($failed -ne ($failure -ne 'none')) { throw "Unexpected installer result for $failure. Installer error: $failureMessage" }
    $retainedWarning = @($AgentFixtureWarnings | Where-Object { [string]$_ -like '*newly enrolled configuration was retained*' }).Count -gt 0
    if ($retainedWarning -ne ($failure -in @('check', 'start'))) { throw "Missing or misleading identity recovery warning for $failure. Installer error: $failureMessage" }
    $expected = if ($failure -eq 'reject') { 'previous-fixture' } else { 'replacement-fixture' }
    if ([IO.File]::ReadAllText($config) -ne $expected) { throw "Valid enrollment was lost in $failure. Installer error: $failureMessage" }
    if ([IO.File]::ReadAllText($config) -ne [IO.File]::ReadAllText((Join-Path $case 'registered.identity'))) { throw 'Local configuration disagrees with registration.' }
    $expectedVersion = if ($failure -eq 'none') { 'new' } else { 'old' }
    if ((& $installed version) -ne $expectedVersion) { throw 'Executable rollback did not restore the expected version.' }
    if ($global:AgentFixtureService.Status -ne 'Running') { throw 'The old service was not recovered.' }
    if (Get-ChildItem -LiteralPath $env:TEMP -Force | Select-Object -First 1) { throw 'Installer left temporary credentials behind.' }
}
Write-Output 'Windows installer preserves the valid registration through check/start failures, restores failed binaries, and leaves rejected registration unchanged.'
