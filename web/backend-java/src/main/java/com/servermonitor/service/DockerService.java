package com.servermonitor.service;

import java.io.IOException;
import java.time.Duration;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

import org.apache.sshd.client.session.ClientSession;
import org.springframework.stereotype.Service;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.servermonitor.ssh.SshSupport;

/** Docker commands over SSH plus stats/size parsers, mirroring services/docker*.go. */
@Service
public class DockerService {

    private static final ObjectMapper JSON = new ObjectMapper();

    public static final Duration DOCKER_COMMAND_TIMEOUT = Duration.ofSeconds(15);
    public static final int DOCKER_LIST_OUTPUT_LIMIT = 512 * 1024;
    public static final int DOCKER_STATS_OUTPUT_LIMIT = 1024 * 1024;

    /** Values reported by `docker stats` and `docker ps --size`; bytes are bytes, block I/O cumulative. */
    public record ContainerStats(double cpuPercent, long memoryUsage, long memoryLimit,
        double memoryPercent, long diskReadBytes, long diskWriteBytes,
        boolean blockIOAvailable, long diskUsage, boolean statsAvailable) {
    }

    /** One row of `docker ps`, merged with stats and (optionally) size. */
    public record Container(String id, String name, String image, String status, String state,
        String ports, String created, double cpuPercent, long memoryUsage, long memoryLimit,
        double memoryPercent, long diskReadBytes, long diskWriteBytes, boolean blockIOAvailable,
        long diskUsage, long diskVirtualUsage, boolean diskAvailable, boolean statsAvailable) {
    }

    /**
     * docker, falling back to passwordless sudo (keeping sudo non-interactive —
     * a password prompt must never hold an API request open).
     */
    public record DockerOutput(String output, IOException error) {
    }

    /** docker with output cap and timeout, like RunDockerCmdBounded (including the timeout-not-permissions rule). */
    public DockerOutput runDockerCmdBounded(ClientSession client, String args) {
        SshSupport.BoundedResult plain = SshSupport.runCmdBounded(client, "docker " + args,
            DOCKER_COMMAND_TIMEOUT, DOCKER_LIST_OUTPUT_LIMIT);
        if (plain.error() == null) {
            return new DockerOutput(plain.output(), null);
        }
        if (plain.error().getMessage() != null && plain.error().getMessage().contains("timed out after")) {
            return new DockerOutput(plain.output(), plain.error());
        }
        SshSupport.BoundedResult sudo = SshSupport.runCmdBounded(client, "sudo -n docker " + args,
            DOCKER_COMMAND_TIMEOUT, DOCKER_LIST_OUTPUT_LIMIT);
        if (sudo.error() != null) {
            return new DockerOutput(sudo.output(), sudo.error());
        }
        return new DockerOutput(sudo.output(), null);
    }

    /** Runs a docker command with the given output limit (used by logs/list/stats). */
    public DockerOutput runDocker(ClientSession client, String args, int limit) {
        SshSupport.BoundedResult plain = SshSupport.runCmdBounded(client, "docker " + args,
            DOCKER_COMMAND_TIMEOUT, limit);
        if (plain.error() == null) {
            return new DockerOutput(plain.output(), null);
        }
        if (plain.error().getMessage() != null && plain.error().getMessage().contains("timed out after")) {
            return new DockerOutput(plain.output(), plain.error());
        }
        SshSupport.BoundedResult sudo = SshSupport.runCmdBounded(client, "sudo -n docker " + args,
            DOCKER_COMMAND_TIMEOUT, limit);
        if (sudo.error() != null) {
            return new DockerOutput(sudo.output(), sudo.error());
        }
        return new DockerOutput(sudo.output(), null);
    }

    /** Non-interactive docker command, like services.RunDockerCmd (docker → sudo docker fallback). */
    public String runDockerCmd(ClientSession client, String args) throws IOException {
        try {
            return SshSupport.runCmd(client, "docker " + args);
        } catch (IOException first) {
            try {
                return SshSupport.runCmd(client, "sudo docker " + args);
            } catch (IOException second) {
                throw second;
            }
        }
    }

    /**
     * Reads the container list (optionally with stats+sizes), mirroring
     * handlers/docker.go's readDockerContainers including its --size fallback.
     */
    public List<Container> readContainers(ClientSession client, boolean detailed) throws IOException {
        String format = "--format '{\"id\":\"{{.ID}}\",\"name\":\"{{.Names}}\",\"image\":\"{{.Image}}\",\"status\":\"{{.Status}}\",\"state\":\"{{.State}}\",\"ports\":\"{{.Ports}}\",\"created\":\"{{.CreatedAt}}\",\"size\":\"{{.Size}}\"}'";
        String options = "ps -a --no-trunc " + (detailed ? "--size " : "");
        DockerOutput ps = runDocker(client, options + format, DOCKER_LIST_OUTPUT_LIMIT);
        if (ps.error() != null && detailed) {
            ps = runDocker(client, "ps -a --no-trunc " + format, DOCKER_LIST_OUTPUT_LIMIT);
        }
        if (ps.error() != null) {
            throw ps.error();
        }
        Map<String, ContainerStats> statsById = Map.of();
        if (detailed) {
            DockerOutput stats = runDocker(client, "stats --no-stream --no-trunc --format '{{json .}}'",
                DOCKER_STATS_OUTPUT_LIMIT);
            if (stats.error() != null) {
                throw stats.error();
            }
            statsById = parseDockerStats(stats.output());
        }
        List<Container> containers = new ArrayList<>();
        for (String line : ps.output().trim().split("\n")) {
            line = line.trim();
            if (line.isEmpty()) {
                continue;
            }
            JsonNode node;
            try {
                node = JSON.readTree(line);
            } catch (Exception e) {
                continue;
            }
            String id = text(node, "id");
            String size = text(node, "size");
            long[] sizes = dockerSizesFromPS(size);
            boolean diskAvailable = detailed && sizes[2] == 1;
            Container merged = new Container(id, text(node, "name"), text(node, "image"),
                text(node, "status"), text(node, "state"), text(node, "ports"), text(node, "created"),
                0, 0, 0, 0, 0, 0, false, sizes[0], sizes[1], diskAvailable, false);
            ContainerStats stats = statsById.get(id);
            if (stats == null) {
                // Stats IDs are usually 12 characters; some versions emit the full ID. Match either direction.
                for (Map.Entry<String, ContainerStats> e : statsById.entrySet()) {
                    if (e.getKey().startsWith(id) || id.startsWith(e.getKey())) {
                        stats = e.getValue();
                        break;
                    }
                }
            }
            if (stats != null) {
                merged = new Container(merged.id(), merged.name(), merged.image(), merged.status(),
                    merged.state(), merged.ports(), merged.created(),
                    stats.cpuPercent(), stats.memoryUsage(), stats.memoryLimit(), stats.memoryPercent(),
                    stats.diskReadBytes(), stats.diskWriteBytes(), stats.blockIOAvailable(),
                    merged.diskUsage(), merged.diskVirtualUsage(), merged.diskAvailable(), stats.statsAvailable());
            }
            containers.add(merged);
        }
        return containers;
    }

    private static String text(JsonNode node, String field) {
        JsonNode child = node.get(field);
        return child == null || child.isNull() ? "" : child.asText("");
    }

    /** One JSON object per line from `docker stats --no-stream --format '{{json .}}'`; invalid lines ignored. */
    public Map<String, ContainerStats> parseDockerStats(String output) {
        Map<String, ContainerStats> stats = new HashMap<>();
        for (String line : output.trim().split("\n")) {
            line = line.trim();
            if (line.endsWith("\r")) {
                line = line.substring(0, line.length() - 1);
            }
            if (line.isEmpty()) {
                continue;
            }
            JsonNode node;
            try {
                node = JSON.readTree(line);
            } catch (Exception e) {
                continue;
            }
            String id = text(node, "ID");
            if (id.isEmpty()) {
                id = text(node, "Container");
            }
            id = id.trim();
            if (id.isEmpty()) {
                continue;
            }
            double cpu = parseDockerPercent(text(node, "CPUPerc"));
            long[] mem = parseDockerPair(text(node, "MemUsage"));
            long[] block = parseDockerBlockIO(text(node, "BlockIO"));
            stats.put(id, new ContainerStats(cpu, mem[0], mem[1],
                parseDockerPercent(text(node, "MemPerc")),
                block[0], block[1], block[2] == 1, 0, true));
        }
        return stats;
    }

    private static final Pattern DOCKER_SIZE_RE = Pattern.compile("(?i)^([0-9]+(?:\\.[0-9]+)?)\\s*([kmgtp]?i?b)$");
    private static final Pattern DOCKER_VIRTUAL_SIZE_RE = Pattern.compile("(?i)\\(\\s*virtual\\s+([^)]*)\\)");

    /** Docker's cumulative block I/O pair; a dash means no accounting support, different from 0B/0B. */
    static long[] parseDockerBlockIO(String raw) {
        raw = raw.trim();
        String[] parts = raw.split("/", 2);
        if (parts.length != 2 || parts[0].trim().equals("-") || parts[1].trim().equals("-")) {
            return new long[]{0, 0, 0};
        }
        long left = parseDockerSize(parts[0]);
        long right = parseDockerSize(parts[1]);
        boolean ok = DOCKER_SIZE_RE.matcher(parts[0].trim()).matches()
            && DOCKER_SIZE_RE.matcher(parts[1].trim()).matches();
        return new long[]{left, right, ok ? 1 : 0};
    }

    static double parseDockerPercent(String raw) {
        raw = raw.trim();
        if (raw.endsWith("%")) {
            raw = raw.substring(0, raw.length() - 1).trim();
        }
        if (raw.isEmpty() || raw.equals("-") || raw.equals("--")) {
            return 0;
        }
        try {
            double v = Double.parseDouble(raw);
            if (Double.isNaN(v) || Double.isInfinite(v) || v < 0) {
                return 0;
            }
            return v;
        } catch (NumberFormatException e) {
            return 0;
        }
    }

    static long[] parseDockerPair(String raw) {
        String[] parts = raw.split("/", 2);
        if (parts.length != 2) {
            return new long[2];
        }
        return new long[]{parseDockerSize(parts[0]), parseDockerSize(parts[1])};
    }

    /**
     * Converts Docker's human-readable sizes ("12.5MiB", "1.2GB", "0B") to
     * bytes, accepting both SI and IEC suffixes. `docker ps --size` may append
     * "(virtual ...)"; the writable layer (first value) is the useful one.
     */
    public static long parseDockerSize(String raw) {
        raw = raw.trim();
        if (raw.isEmpty() || raw.equals("-") || raw.equals("--")) {
            return 0;
        }
        int paren = raw.indexOf('(');
        if (paren >= 0) {
            raw = raw.substring(0, paren).trim();
        }
        Matcher m = DOCKER_SIZE_RE.matcher(raw);
        if (!m.matches()) {
            return 0;
        }
        double n;
        try {
            n = Double.parseDouble(m.group(1));
        } catch (NumberFormatException e) {
            return 0;
        }
        if (n < 0) {
            return 0;
        }
        double multiplier = switch (m.group(2).toUpperCase()) {
            case "KB" -> 1e3;
            case "MB" -> 1e6;
            case "GB" -> 1e9;
            case "TB" -> 1e12;
            case "PB" -> 1e15;
            case "KIB" -> 1 << 10;
            case "MIB" -> 1 << 20;
            case "GIB" -> 1 << 30;
            case "TIB" -> 1L << 40;
            case "PIB" -> 1L << 50;
            case "B" -> 1;
            default -> -1;
        };
        if (multiplier < 0) {
            return 0;
        }
        double bytes = n * multiplier;
        if (bytes > Long.MAX_VALUE) {
            return Long.MAX_VALUE;
        }
        return (long) (bytes + 0.5);
    }

    /** Writable layer, virtual total, and whether parsing succeeded ({return}[2] == 1). */
    public static long[] dockerSizesFromPS(String raw) {
        raw = raw.trim();
        if (raw.isEmpty() || raw.equals("-") || raw.equals("--")) {
            return new long[]{0, 0, 0};
        }
        long writable = parseDockerSize(raw);
        String first = raw;
        int paren = first.indexOf('(');
        if (paren >= 0) {
            first = first.substring(0, paren).trim();
        }
        boolean firstOk = DOCKER_SIZE_RE.matcher(first).matches();
        long virtual = 0;
        Matcher m = DOCKER_VIRTUAL_SIZE_RE.matcher(raw);
        if (m.find()) {
            virtual = parseDockerSize(m.group(1));
        }
        return new long[]{writable, virtual, firstOk ? 1 : 0};
    }
}
