package com.servermonitor.service;

import java.util.ArrayList;
import java.util.List;
import java.util.Locale;

/** Parsers for the batched Linux metrics command and its sections, mirroring services/metrics.go. */
final class LinuxMetricsParsers {

    private LinuxMetricsParsers() {
    }

    /** Delimits command outputs inside the batched metrics command (a line of only this marker is a boundary). */
    static final String SECTION_SEPARATOR = "__SVRMON_SECTION__";

    static final int SECTION_STAT_FIRST = 0;
    static final int SECTION_STAT_SECOND = 1;
    static final int SECTION_MEMINFO = 2;
    static final int SECTION_LOADAVG = 3;
    static final int SECTION_NETDEV = 4;
    static final int SECTION_DISKSTATS = 5;
    static final int SECTION_UPTIME = 6;
    static final int SECTION_NPROC = 7;
    static final int SECTION_DISKUSAGE = 8;
    static final int SECTION_DOCKER = 9;
    static final int SECTION_COUNT = 10;

    /**
     * Gathers every sample in a single SSH exec round trip instead of one
     * session per file. The second /proc/stat read is delayed remotely so CPU
     * usage still comes from two samples half a second apart.
     */
    /**
     * Answers two separate questions: "is Docker installed" (CLI on PATH — a
     * fact that does not flicker) and "did the daemon answer just now" (a
     * version that can legitimately be empty for a moment). `docker info` is
     * capped at 5s so a wedged daemon cannot starve the metrics behind it.
     */
    static final String LINUX_DOCKER_PROBE = "if command -v docker >/dev/null 2>&1; then echo installed; "
        + "timeout 5 docker info --format '{{.ServerVersion}}' 2>/dev/null || "
        + "timeout 5 sudo -n docker info --format '{{.ServerVersion}}' 2>/dev/null || true; "
        + "else echo absent; fi";

    static final String LINUX_METRICS_COMMAND = String.join("; echo " + SECTION_SEPARATOR + "; ",
        "cat /proc/stat",
        "sleep 0.5 || sleep 1; cat /proc/stat",
        "cat /proc/meminfo",
        "cat /proc/loadavg",
        "cat /proc/net/dev",
        "cat /proc/diskstats",
        "cat /proc/uptime",
        "nproc",
        "df -P -B1 /",
        LINUX_DOCKER_PROBE);

    /** Splits batched command output on separator lines; truncated output pads the remaining sections empty. */
    static List<String> splitLinuxSections(String out, int want) {
        List<String> sections = new ArrayList<>(want);
        StringBuilder current = new StringBuilder();
        for (String line : out.split("\n", -1)) {
            if (line.trim().equals(SECTION_SEPARATOR)) {
                sections.add(current.toString());
                current.setLength(0);
                continue;
            }
            current.append(line).append('\n');
        }
        sections.add(current.toString());
        while (sections.size() < want) {
            sections.add("");
        }
        return sections;
    }

    /** CPU usage from two /proc/stat snapshots taken half a second apart. */
    static double cpuPercentFromSamples(String first, String second) {
        long[] a = parseProcStatCPU(first);
        long[] b = parseProcStatCPU(second);
        long dIdle = b[0] - a[0];
        long dTotal = b[1] - a[1];
        if (dTotal > 0) {
            return (1.0 - (double) dIdle / dTotal) * 100;
        }
        return 0;
    }

    /** Returns {idle, total} jiffies from the aggregate "cpu " line. */
    static long[] parseProcStatCPU(String out) {
        for (String line : out.split("\n")) {
            if (line.startsWith("cpu ")) {
                String[] fields = line.trim().split("\\s+");
                if (fields.length < 9) {
                    return new long[2];
                }
                long[] vals = new long[8];
                for (int i = 0; i < 8 && i < fields.length - 1; i++) {
                    vals[i] = parseLong(fields[i + 1]);
                }
                long idle = vals[3] + vals[4];
                long total = vals[0] + vals[1] + vals[2] + vals[3] + vals[4] + vals[5] + vals[6] + vals[7];
                return new long[]{idle, total};
            }
        }
        return new long[2];
    }

    /** Mem used/total in bytes from /proc/meminfo (which reports kB). */
    static long[] parseMemInfo(String out) {
        long memTotal = 0, memFree = 0, buffers = 0, cached = 0;
        for (String line : out.split("\n")) {
            String[] fields = line.trim().split("\\s+");
            if (fields.length < 2) {
                continue;
            }
            long val = parseLong(fields[1]);
            switch (fields[0]) {
                case "MemTotal:" -> memTotal = val;
                case "MemFree:" -> memFree = val;
                case "Buffers:" -> buffers = val;
                case "Cached:" -> cached = val;
                default -> { }
            }
        }
        long used = memTotal - memFree - buffers - cached;
        if (used < 0) {
            used = 0;
        }
        return new long[]{used * 1024, memTotal * 1024};
    }

    static double[] parseLoadAvg(String out) {
        String[] fields = out.trim().split("\\s+");
        if (fields.length < 3) {
            return new double[3];
        }
        return new double[]{parseDouble(fields[0]), parseDouble(fields[1]), parseDouble(fields[2])};
    }

    /** Cumulative rx/tx bytes across interfaces, the loopback excluded. */
    static long[] parseNetDev(String out) {
        long rxTotal = 0, txTotal = 0;
        for (String line : out.split("\n")) {
            if (!line.contains(":")) {
                continue;
            }
            String[] fields = line.trim().split("\\s+");
            if (fields.length < 10) {
                continue;
            }
            String iface = fields[0].endsWith(":") ? fields[0].substring(0, fields[0].length() - 1) : fields[0];
            if ("lo".equals(iface)) {
                continue;
            }
            rxTotal += parseLong(fields[1].trim());
            txTotal += parseLong(fields[9].trim());
        }
        return new long[]{rxTotal, txTotal};
    }

    static long parseUptime(String out) {
        String[] parts = out.trim().split("\\s+");
        if (parts.length > 0 && !parts[0].isEmpty()) {
            return (long) parseDouble(parts[0]);
        }
        return 0;
    }

    /**
     * Cumulative bytes read/written summed over whole disks only (partitions are
     * skipped: sdN/vdN end in a digit, nvme partitions contain a "p").
     */
    static long[] parseDiskStats(String out) {
        long readSectors = 0, writeSectors = 0;
        for (String line : out.split("\n")) {
            String[] fields = line.trim().split("\\s+");
            if (fields.length < 14) {
                continue;
            }
            String name = fields[2];
            if (!(name.startsWith("sd") || name.startsWith("vd") || name.startsWith("nvme"))) {
                continue;
            }
            char last = name.charAt(name.length() - 1);
            if (Character.isDigit(last) && !name.contains("nvme")) {
                continue;
            }
            if (name.contains("p") && name.contains("nvme")) {
                continue;
            }
            readSectors += parseLong(fields[5]);
            writeSectors += parseLong(fields[9]);
        }
        return new long[]{readSectors * 512, writeSectors * 512};
    }

    /** Used/total bytes of the root filesystem from `df -P -B1 /`. */
    static long[] parseDiskUsage(String out) {
        String trimmed = out.trim();
        if (trimmed.isEmpty()) {
            return new long[2];
        }
        String[] lines = trimmed.split("\n");
        String[] fields = lines[lines.length - 1].trim().split("\\s+");
        if (fields.length < 5) {
            return new long[2];
        }
        long total = parseLong(fields[fields.length - 5]);
        long used = parseLong(fields[fields.length - 4]);
        return new long[]{used, total};
    }

    static int parseNproc(String out) {
        try {
            return Integer.parseInt(out.trim());
        } catch (NumberFormatException e) {
            return 0;
        }
    }

    // --- Docker probe --------------------------------------------------

    enum DockerPresence {UNKNOWN, ABSENT, INSTALLED}

    static DockerPresence parseDockerProbePresence(String presence) {
        return switch (presence) {
            case "absent" -> DockerPresence.ABSENT;
            case "installed" -> DockerPresence.INSTALLED;
            default -> DockerPresence.UNKNOWN;
        };
    }

    record DockerProbeResult(DockerPresence presence, String version) {
    }

    /**
     * Reads the probe section: an "installed"/"absent" verdict line, optionally
     * followed by the daemon version. A bare valid version also proves
     * installation (backwards compatibility); anything else is unknown rather
     * than absent.
     */
    static DockerProbeResult parseDockerProbe(String out) {
        String trimmed = out.trim();
        String[] lines = trimmed.split("\n", -1);
        String verdict = lines[0].trim().toLowerCase(Locale.ROOT);
        switch (verdict) {
            case "absent":
                return new DockerProbeResult(DockerPresence.ABSENT, "");
            case "installed":
                if (lines.length < 2) {
                    return new DockerProbeResult(DockerPresence.INSTALLED, "");
                }
                return new DockerProbeResult(DockerPresence.INSTALLED, parseDockerVersion(lines[1]));
            default:
                String version = parseDockerVersion(out);
                if (!version.isEmpty()) {
                    return new DockerProbeResult(DockerPresence.INSTALLED, version);
                }
                return new DockerProbeResult(DockerPresence.UNKNOWN, "");
        }
    }

    /** A real server version is a short single token like "24.0.7"; anything else is not a version. */
    static String parseDockerVersion(String out) {
        String version = out.trim();
        if (version.isEmpty() || version.length() > 31
            || version.contains(" ") || version.contains("\t") || version.contains("\n")) {
            return "";
        }
        return version;
    }

    // --- shared small utils --------------------------------------------

    static long parseLong(String raw) {
        try {
            return Long.parseLong(raw.trim());
        } catch (NumberFormatException e) {
            return 0;
        }
    }

    static double parseDouble(String raw) {
        try {
            return Double.parseDouble(raw.trim());
        } catch (NumberFormatException e) {
            return 0;
        }
    }

    /** Average rtt from `ping -c 3` output ("min/avg/max" or "round-trip" line). */
    static int parsePingAverage(String output) {
        for (String line : output.split("\n")) {
            if (!line.contains("min/avg/max") && !line.contains("round-trip")) {
                continue;
            }
            int eq = line.indexOf('=');
            if (eq < 0) {
                continue;
            }
            String[] parts = line.substring(eq + 1).trim().split("/");
            if (parts.length < 2) {
                continue;
            }
            try {
                double average = Double.parseDouble(parts[1].trim());
                return Math.max(1, (int) (average + 0.5));
            } catch (NumberFormatException ignored) {
                continue;
            }
        }
        return 0;
    }
}
