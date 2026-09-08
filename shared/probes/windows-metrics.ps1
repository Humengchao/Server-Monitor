# Windows collection, one `key=value` per line. See README.md for how this is
# delivered (deflate + UTF-16LE base64 + -EncodedCommand) and why.
#
# CIM classes rather than Get-Counter: counter *names* are localised and simply
# do not exist in English on a Chinese or German Windows, while CIM class and
# property names are invariant.
$ErrorActionPreference='SilentlyContinue'
# PowerShell otherwise serialises its progress stream into stdout as CLIXML
# when the output is redirected. Verified on Windows Server 2016, where
# "preparing modules for first use" records arrived mixed in with the metrics.
$ProgressPreference='SilentlyContinue'
$os = Get-CimInstance Win32_OperatingSystem
$cores = (Get-CimInstance Win32_ComputerSystem).NumberOfLogicalProcessors
# Win32_PerfRawData_* counters are cumulative despite the "Persec" suffix,
# which is what makes them usable the way the /proc counters are: take a delta
# between two reads. A single Win32_Processor.LoadPercentage is a point sample
# and disagrees with what the host's own Task Manager shows.
$raw1 = @{}; foreach ($r in Get-CimInstance Win32_PerfRawData_PerfOS_Processor) { $raw1[$r.Name] = $r }
Start-Sleep -Milliseconds 500
$raw2 = Get-CimInstance Win32_PerfRawData_PerfOS_Processor
$cpuPct = @{}
foreach ($b in $raw2) {
  $a = $raw1[$b.Name]
  if ($a) {
    $dt = [double]($b.Timestamp_Sys100NS - $a.Timestamp_Sys100NS)
    $di = [double]($b.PercentProcessorTime - $a.PercentProcessorTime)
    if ($dt -gt 0) { $cpuPct[$b.Name] = [math]::Round((1 - ($di / $dt)) * 100, 1) }
  }
}
$cpu = $cpuPct['_Total']
if ($null -eq $cpu) { $cpu = (Get-CimInstance Win32_Processor | Measure-Object -Property LoadPercentage -Average).Average }
$nics = Get-CimInstance Win32_PerfRawData_Tcpip_NetworkInterface
$netrx = ($nics | Measure-Object -Property BytesReceivedPersec -Sum).Sum
$nettx = ($nics | Measure-Object -Property BytesSentPersec -Sum).Sum
$disk = Get-CimInstance Win32_PerfRawData_PerfDisk_PhysicalDisk -Filter "Name='_Total'"
$uptime = [int64]((Get-Date) - $os.LastBootUpTime).TotalSeconds
$disks = Get-CimInstance Win32_LogicalDisk -Filter 'DriveType=3'
$disktotal = ($disks | Measure-Object -Property Size -Sum).Sum
$diskfree = ($disks | Measure-Object -Property FreeSpace -Sum).Sum
$queue = (Get-CimInstance Win32_PerfFormattedData_PerfOS_System).ProcessorQueueLength
$df = "{{.ServerVersion}}|{{.Images}}|{{.ContainersRunning}}|{{.ContainersStopped}}|{{.ContainersPaused}}"
$docker = if (Get-Command docker -ErrorAction SilentlyContinue) { docker info --format $df } else { '' }
Write-Output ("cpu=" + [int64][math]::Round([double]$cpu))
Write-Output ("memtotal=" + $os.TotalVisibleMemorySize)
Write-Output ("memfree=" + $os.FreePhysicalMemory)
Write-Output ("netrx=" + [int64]$netrx)
Write-Output ("nettx=" + [int64]$nettx)
Write-Output ("diskread=" + [int64]$disk.DiskReadBytesPersec)
Write-Output ("diskwrite=" + [int64]$disk.DiskWriteBytesPersec)
Write-Output ("uptime=" + $uptime)
Write-Output ("cores=" + $cores)
Write-Output ("disktotal=" + [int64]$disktotal)
Write-Output ("diskfree=" + [int64]$diskfree)
Write-Output ("queue=" + [int64]$queue)
Write-Output ("docker=" + $docker)
$cpuName = (Get-CimInstance Win32_Processor | Select-Object -First 1).Name
$ident = @($env:COMPUTERNAME, $os.Version, (Get-CimInstance Win32_ComputerSystem).SystemType, $os.Caption, $cpuName)
Write-Output ("ident=" + ($ident -join '|'))
$ips = (Get-CimInstance Win32_NetworkAdapterConfiguration -Filter 'IPEnabled=True' | ForEach-Object { $_.IPAddress } | Where-Object { $_ -and $_ -notmatch ':' })
Write-Output ("ips=" + ($ips -join ' '))
foreach ($n in ($cpuPct.Keys | Where-Object { $_ -ne '_Total' })) {
  Write-Output ("core=" + $n + '|' + $cpuPct[$n])
}
foreach ($d in $disks) {
  Write-Output ("fs=" + $d.DeviceID + '|' + [int64]$d.Size + '|' + [int64]($d.Size - $d.FreeSpace))
}
foreach ($n in $nics) {
  Write-Output ("if=" + ($n.Name -replace '\|','_') + '|' + [int64]$n.BytesReceivedPersec + '|' + [int64]$n.BytesSentPersec)
}
$totalmem = [double]$os.TotalVisibleMemorySize * 1024
foreach ($pr in Get-Process | Sort-Object -Property CPU -Descending | Select-Object -First 25) {
  $pct = if ($totalmem -gt 0) { [math]::Round(($pr.WorkingSet64 / $totalmem) * 100, 1) } else { 0 }
  Write-Output ("proc=" + $pr.Id + '|' + $pr.ProcessName + '|' + [math]::Round([double]$pr.CPU, 1) + '|' + $pct + '|' + [int64]$pr.WorkingSet64)
}
