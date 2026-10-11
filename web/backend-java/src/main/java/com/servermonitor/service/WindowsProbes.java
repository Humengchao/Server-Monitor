package com.servermonitor.service;

import java.nio.ByteBuffer;
import java.nio.ByteOrder;
import java.nio.charset.StandardCharsets;
import java.util.Base64;
import java.util.HashMap;
import java.util.Map;

/** Windows (PowerShell over SSH) probe scripts and output parsing, mirroring services/metrics.go. */
final class WindowsProbes {

    private WindowsProbes() {
    }

    /** Encodes a script as UTF-16LE base64 for `powershell -EncodedCommand`. */
    static String encodePowerShell(String script) {
        byte[] utf16 = script.getBytes(StandardCharsets.UTF_16LE);
        ByteBuffer le = ByteBuffer.allocate(utf16.length).order(ByteOrder.LITTLE_ENDIAN);
        le.put(utf16);
        return Base64.getEncoder().encodeToString(le.array());
    }

    /**
     * Gathers all metrics in a single PowerShell invocation as key=value lines.
     * Uses CIM classes (locale-independent, unlike Get-Counter). Perf raw
     * counters are cumulative despite the name, matching /proc semantics.
     */
    static final String WINDOWS_METRICS_SCRIPT = """
        $ErrorActionPreference='SilentlyContinue'
        $os = Get-CimInstance Win32_OperatingSystem
        $cpu = (Get-CimInstance Win32_Processor | Measure-Object -Property LoadPercentage -Average).Average
        $cores = (Get-CimInstance Win32_ComputerSystem).NumberOfLogicalProcessors
        $nics = Get-CimInstance Win32_PerfRawData_Tcpip_NetworkInterface
        $netrx = ($nics | Measure-Object -Property BytesReceivedPersec -Sum).Sum
        $nettx = ($nics | Measure-Object -Property BytesSentPersec -Sum).Sum
        $disk = Get-CimInstance Win32_PerfRawData_PerfDisk_PhysicalDisk -Filter "Name='_Total'"
        $uptime = [int64]((Get-Date) - $os.LastBootUpTime).TotalSeconds
        $disks = Get-CimInstance Win32_LogicalDisk -Filter 'DriveType=3'
        $disktotal = ($disks | Measure-Object -Property Size -Sum).Sum
        $diskfree = ($disks | Measure-Object -Property FreeSpace -Sum).Sum
        $queue = (Get-CimInstance Win32_PerfFormattedData_PerfOS_System).ProcessorQueueLength
        $dockerpresent = if (Get-Command docker -ErrorAction SilentlyContinue) { 'installed' } else { 'absent' }
        $docker = if ($dockerpresent -eq 'installed') { docker info --format "{{.ServerVersion}}" 2>$null } else { '' }
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
        Write-Output ("dockerpresent=" + $dockerpresent)
        Write-Output ("docker=" + $docker)""";

    static String windowsMetricsCommand() {
        return "powershell -NoProfile -NonInteractive -EncodedCommand " + encodePowerShell(WINDOWS_METRICS_SCRIPT);
    }

    /** key=value lines from the metrics script. */
    record WindowsValues(Map<String, Long> values, String dockerPresent, String dockerVersion) {
        long get(String key) {
            return values.getOrDefault(key, 0L);
        }
    }

    static WindowsValues parseWindowsMetrics(String out) {
        Map<String, Long> values = new HashMap<>();
        String dockerVersion = "";
        String dockerPresent = "";
        for (String line : out.split("\n")) {
            int eq = line.indexOf('=');
            if (eq < 0) {
                continue;
            }
            String key = line.substring(0, eq).trim();
            String value = line.substring(eq + 1).trim();
            if (key.equals("docker")) {
                dockerVersion = LinuxMetricsParsers.parseDockerVersion(value);
                continue;
            }
            if (key.equals("dockerpresent")) {
                dockerPresent = value;
                continue;
            }
            try {
                values.put(key, Long.parseLong(value));
            } catch (NumberFormatException ignored) { }
        }
        return new WindowsValues(values, dockerPresent, dockerVersion);
    }

    /** A version alone proves installation; nothing at all is unknown rather than absent. */
    static LinuxMetricsParsers.DockerPresence windowsDockerPresence(String verdict, String version) {
        if ("installed".equals(verdict)) {
            return LinuxMetricsParsers.DockerPresence.INSTALLED;
        }
        if ("absent".equals(verdict)) {
            return LinuxMetricsParsers.DockerPresence.ABSENT;
        }
        if (version != null && !version.isEmpty()) {
            return LinuxMetricsParsers.DockerPresence.INSTALLED;
        }
        return LinuxMetricsParsers.DockerPresence.UNKNOWN;
    }

    /** The standalone Docker re-check command used by the "re-detect" button. */
    static String windowsDockerProbeCommand() {
        return "powershell -NoProfile -NonInteractive -EncodedCommand "
            + encodePowerShell("$p = if (Get-Command docker -ErrorAction SilentlyContinue) { 'installed' } else { 'absent' }\n"
                + "$v = if ($p -eq 'installed') { docker info --format \"{{.ServerVersion}}\" 2>$null } else { '' }\n"
                + "Write-Output (\"dockerpresent=\" + $p); Write-Output (\"docker=\" + $v)");
    }

    record WindowsDockerProbe(LinuxMetricsParsers.DockerPresence presence, String version) {
    }

    static WindowsDockerProbe parseWindowsDockerProbe(String out) {
        String verdict = "";
        String version = "";
        for (String line : out.split("\n")) {
            int eq = line.indexOf('=');
            if (eq < 0) {
                continue;
            }
            String key = line.substring(0, eq).trim();
            String value = line.substring(eq + 1).trim();
            if (key.equals("dockerpresent")) {
                verdict = value;
            }
            if (key.equals("docker")) {
                version = LinuxMetricsParsers.parseDockerVersion(value);
            }
        }
        return new WindowsDockerProbe(windowsDockerPresence(verdict, version), version);
    }
}
