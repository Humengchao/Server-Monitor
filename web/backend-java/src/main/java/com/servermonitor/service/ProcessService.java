package com.servermonitor.service;

import java.io.IOException;
import java.util.ArrayList;
import java.util.Comparator;
import java.util.List;
import java.util.regex.Pattern;

import org.apache.sshd.client.session.ClientSession;
import org.springframework.stereotype.Service;

import com.servermonitor.ssh.SshSupport;

/** Remote process tables (ps / Windows perf counters), mirroring services/process.go. */
@Service
public class ProcessService {

    /** One row of a host's process table; unreportable fields keep their zero value. */
    public record ProcessInfo(int pid, String user, double cpuPercent, double memPercent,
        long rssBytes, String state, long elapsedSeconds, String command) {
    }

    /** Bounds the response so a busy host can't push a multi-megabyte payload. */
    public static final int MAX_PROCESSES = 300;

    private static final String LINUX_PROCESS_COMMAND =
        "ps -eo pid=,user=,pcpu=,pmem=,rss=,stat=,etimes=,args= 2>/dev/null | head -c 2000000";
    private static final String LINUX_PROCESS_FALLBACK_COMMAND =
        "ps aux 2>/dev/null | head -c 2000000";

    public List<ProcessInfo> listLinuxProcesses(ClientSession client) throws IOException {
        String primary = null;
        IOException primaryErr = null;
        try {
            primary = SshSupport.runCmd(client, LINUX_PROCESS_COMMAND);
        } catch (IOException e) {
            primaryErr = e;
        }
        if (primary != null) {
            List<ProcessInfo> procs = parseLinuxPS(primary);
            if (!procs.isEmpty()) {
                return procs;
            }
        }
        String fallback = null;
        IOException fallbackErr = null;
        try {
            fallback = SshSupport.runCmd(client, LINUX_PROCESS_FALLBACK_COMMAND);
        } catch (IOException e) {
            fallbackErr = e;
        }
        if (fallback != null) {
            List<ProcessInfo> procs = parsePSAux(fallback);
            if (!procs.isEmpty()) {
                return procs;
            }
        }
        if (primaryErr != null) {
            throw new IOException("list processes: " + primaryErr.getMessage(), primaryErr);
        }
        if (fallbackErr != null) {
            throw new IOException("list processes: " + fallbackErr.getMessage(), fallbackErr);
        }
        return List.of();
    }

    /** "pid user pcpu pmem rss stat etimes args" rows; a row whose PID does not parse is skipped. */
    static List<ProcessInfo> parseLinuxPS(String out) {
        List<ProcessInfo> procs = new ArrayList<>();
        for (String line : out.split("\n")) {
            String[] fields = line.trim().split("\\s+");
            if (fields.length < 8) {
                continue;
            }
            int pid;
            try {
                pid = Integer.parseInt(fields[0]);
            } catch (NumberFormatException e) {
                continue;
            }
            if (pid <= 0) {
                continue;
            }
            double cpu;
            try {
                cpu = Double.parseDouble(fields[2]);
            } catch (NumberFormatException e) {
                continue;
            }
            double mem = parseDouble(fields[3]);
            long rssKB = parseLong(fields[4]);
            long elapsed = parseLong(fields[6]);
            // Rejoin from the raw line so multiple spaces inside a command line survive.
            String command = joinRest(line, java.util.Arrays.copyOf(fields, 7));
            procs.add(new ProcessInfo(pid, fields[1], cpu, mem, rssKB * 1024, fields[5], elapsed, command));
        }
        return procs;
    }

    /** The BSD-style "USER PID %CPU %MEM VSZ RSS TTY STAT START TIME COMMAND" layout. */
    static List<ProcessInfo> parsePSAux(String out) {
        List<ProcessInfo> procs = new ArrayList<>();
        for (String line : out.split("\n")) {
            String[] fields = line.trim().split("\\s+");
            if (fields.length < 11) {
                continue;
            }
            int pid;
            try {
                pid = Integer.parseInt(fields[1]);
            } catch (NumberFormatException e) {
                continue;
            }
            if (pid <= 0) {
                continue;
            }
            double cpu;
            try {
                cpu = Double.parseDouble(fields[2]);
            } catch (NumberFormatException e) {
                continue;
            }
            double mem = parseDouble(fields[3]);
            long rssKB = parseLong(fields[5]);
            String command = joinRest(line, java.util.Arrays.copyOf(fields, 10));
            procs.add(new ProcessInfo(pid, fields[0], cpu, mem, rssKB * 1024, fields[7], 0, command));
        }
        return procs;
    }

    /** Remainder of line after the given leading fields, preserving the tail's original spacing. */
    static String joinRest(String line, String[] leading) {
        String rest = stripLeft(line);
        for (String field : leading) {
            if (rest.startsWith(field)) {
                rest = rest.substring(field.length());
            }
            rest = stripLeft(rest);
        }
        return rest.trim();
    }

    private static String stripLeft(String s) {
        int i = 0;
        while (i < s.length() && (s.charAt(i) == ' ' || s.charAt(i) == '\t')) {
            i++;
        }
        return s.substring(i);
    }

    private static final String WINDOWS_PROCESS_SCRIPT = """
        $ErrorActionPreference='SilentlyContinue'
        $cs = Get-CimInstance Win32_ComputerSystem
        $total = [double]$cs.TotalPhysicalMemory
        $cores = [double]$cs.NumberOfLogicalProcessors
        if ($cores -le 0) { $cores = 1 }
        Get-CimInstance Win32_PerfFormattedData_PerfProc_Process |
          Where-Object { $_.IDProcess -gt 0 -and $_.Name -ne '_Total' } |
          ForEach-Object {
            $cpu = [math]::Round([double]$_.PercentProcessorTime / $cores, 1)
            $rss = [int64]$_.WorkingSetPrivate
            $mem = if ($total -gt 0) { [math]::Round(($rss / $total) * 100, 1) } else { 0 }
            Write-Output ("{0}|{1}|{2}|{3}|{4}|{5}" -f $_.IDProcess, $cpu, $mem, $rss, [int64]$_.ElapsedTime, $_.Name)
          }""";

    public List<ProcessInfo> listWindowsProcesses(ClientSession client) throws IOException {
        String out = null;
        IOException err = null;
        try {
            out = SshSupport.runCmd(client, "powershell -NoProfile -NonInteractive -EncodedCommand "
                + WindowsProbes.encodePowerShell(WINDOWS_PROCESS_SCRIPT));
        } catch (IOException e) {
            err = e;
        }
        List<ProcessInfo> procs = parseWindowsProcesses(out == null ? "" : out);
        if (procs.isEmpty() && err != null) {
            throw new IOException("list processes: " + err.getMessage(), err);
        }
        return procs;
    }

    /** "pid|cpu|mem|rss|elapsed|name" rows. */
    static List<ProcessInfo> parseWindowsProcesses(String out) {
        List<ProcessInfo> procs = new ArrayList<>();
        for (String line : out.split("\n")) {
            String[] parts = line.trim().split("\\|");
            if (parts.length < 6) {
                continue;
            }
            int pid;
            try {
                pid = Integer.parseInt(parts[0].trim());
            } catch (NumberFormatException e) {
                continue;
            }
            if (pid <= 0) {
                continue;
            }
            double cpu;
            try {
                cpu = Double.parseDouble(parts[1].trim());
            } catch (NumberFormatException e) {
                continue;
            }
            double mem = parseDouble(parts[2].trim());
            long rss = parseLong(parts[3].trim());
            long elapsed = parseLong(parts[4].trim());
            String command = String.join("|", java.util.Arrays.copyOfRange(parts, 5, parts.length));
            procs.add(new ProcessInfo(pid, "", cpu, mem, rss, "running", elapsed, command));
        }
        return procs;
    }

    /** Sorts by CPU (memory breaks ties) and trims to the cap. */
    public static List<ProcessInfo> topProcesses(List<ProcessInfo> procs, int limit) {
        List<ProcessInfo> sorted = new ArrayList<>(procs);
        sorted.sort(Comparator.comparingDouble(ProcessInfo::cpuPercent).reversed()
            .thenComparing(Comparator.comparingDouble(ProcessInfo::memPercent).reversed()));
        if (limit > 0 && sorted.size() > limit) {
            return new ArrayList<>(sorted.subList(0, limit));
        }
        return sorted;
    }

    /**
     * Signals a process by PID. The PID is formatted as an integer, so no
     * caller-supplied text ever reaches the remote shell. PID 1 is refused:
     * killing it takes the whole host or container down.
     */
    public void killProcess(ClientSession client, int pid, String serverType, boolean force)
        throws IOException {
        if (pid <= 1) {
            throw new IOException("refusing to signal PID " + pid);
        }
        String cmd;
        if ("windows".equals(serverType)) {
            cmd = "powershell -NoProfile -NonInteractive -EncodedCommand "
                + WindowsProbes.encodePowerShell("Stop-Process -Id " + pid + " -Force -ErrorAction Stop");
        } else {
            cmd = "kill -" + (force ? "KILL" : "TERM") + " " + pid;
        }
        try {
            SshSupport.runCmdCombined(client, cmd);
        } catch (SshSupport.CommandFailedException e) {
            String detail = cleanCommandError(e.getMessage());
            throw new IOException(detail.isEmpty() ? e.getMessage() : detail, e);
        }
    }

    // "bash: line 1: " / "sh: 1: " location prefixes a shell adds to builtin errors.
    private static final Pattern SHELL_PREFIX = Pattern.compile("^[\\w.-]+: (?:line )?\\d+: ");

    /** Reduces a remote diagnostic to one presentable line. */
    public static String cleanCommandError(String text) {
        if (text == null) {
            return "";
        }
        int idx = -1;
        int cr = text.indexOf('\r');
        int lf = text.indexOf('\n');
        if (cr >= 0 && lf >= 0) {
            idx = Math.min(cr, lf);
        } else {
            idx = Math.max(cr, lf);
        }
        if (idx >= 0) {
            text = text.substring(0, idx);
        }
        return SHELL_PREFIX.matcher(text.trim()).replaceAll("").trim();
    }

    private static double parseDouble(String raw) {
        try {
            return Double.parseDouble(raw);
        } catch (NumberFormatException e) {
            return 0;
        }
    }

    private static long parseLong(String raw) {
        try {
            return Long.parseLong(raw);
        } catch (NumberFormatException e) {
            return 0;
        }
    }
}
