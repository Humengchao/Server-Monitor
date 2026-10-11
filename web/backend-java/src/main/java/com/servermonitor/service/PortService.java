package com.servermonitor.service;

import java.io.IOException;
import java.net.InetAddress;
import java.util.ArrayList;
import java.util.Comparator;
import java.util.List;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

import org.apache.sshd.client.session.ClientSession;
import org.springframework.stereotype.Service;

import com.servermonitor.ssh.SshSupport;

/** Listening-socket inventories (ss / netstat / Windows TCP-UDP), mirroring services/ports.go. */
@Service
public class PortService {

    /** One socket a host is accepting traffic on. */
    public record ListeningPort(String protocol, String address, int port, String process, int pid,
        String exposure) {
    }

    public static final int MAX_LISTENING_PORTS = 300;

    private static final String LINUX_PORTS_COMMAND = "ss -tulnp 2>/dev/null | head -c 500000";
    private static final String LINUX_PORTS_FALLBACK_COMMAND = "netstat -tulnp 2>/dev/null | head -c 500000";

    public List<ListeningPort> listLinuxPorts(ClientSession client) throws IOException {
        String out = null;
        IOException firstErr = null;
        try {
            out = SshSupport.runCmd(client, LINUX_PORTS_COMMAND);
        } catch (IOException e) {
            firstErr = e;
        }
        if (out != null) {
            List<ListeningPort> ports = parseSS(out);
            if (!ports.isEmpty()) {
                return ports;
            }
        }
        String fallback = null;
        IOException fallbackErr = null;
        try {
            fallback = SshSupport.runCmd(client, LINUX_PORTS_FALLBACK_COMMAND);
        } catch (IOException e) {
            fallbackErr = e;
        }
        if (fallback != null) {
            List<ListeningPort> ports = parseNetstat(fallback);
            if (!ports.isEmpty()) {
                return ports;
            }
        }
        if (firstErr != null) {
            throw new IOException("list ports: " + firstErr.getMessage(), firstErr);
        }
        if (fallbackErr != null) {
            throw new IOException("list ports: " + fallbackErr.getMessage(), fallbackErr);
        }
        return List.of();
    }

    // ss's users:(("name",pid=N,fd=M)) field; the first entry is the one shown.
    private static final Pattern SS_PROCESS = Pattern.compile("\\(\\(\"([^\"]*)\",pid=(\\d+)");

    /** "netid state recv-q send-q local peer [process]" rows; rows without a numeric port are skipped. */
    static List<ListeningPort> parseSS(String out) {
        List<ListeningPort> ports = new ArrayList<>();
        for (String line : out.split("\n")) {
            String[] fields = line.trim().split("\\s+");
            if (fields.length < 5) {
                continue;
            }
            HostPort hp = splitHostPort(fields[4]);
            if (hp == null) {
                continue;
            }
            String process = "";
            int pid = 0;
            Matcher m = SS_PROCESS.matcher(line);
            if (m.find()) {
                process = m.group(1);
                try {
                    pid = Integer.parseInt(m.group(2));
                } catch (NumberFormatException ignored) { }
            }
            ports.add(new ListeningPort(fields[0].toLowerCase(), hp.host, hp.port, process, pid,
                classifyExposure(hp.host)));
        }
        return ports;
    }

    // netstat's "PID/Program name" column.
    private static final Pattern NETSTAT_PROCESS = Pattern.compile("^(\\d+)/(.*)$");

    /** "proto recv-q send-q local foreign [state] [pid/program]"; trailing columns searched by shape. */
    static List<ListeningPort> parseNetstat(String out) {
        List<ListeningPort> ports = new ArrayList<>();
        for (String line : out.split("\n")) {
            String[] fields = line.trim().split("\\s+");
            if (fields.length < 4) {
                continue;
            }
            String proto = fields[0].toLowerCase();
            if (!proto.startsWith("tcp") && !proto.startsWith("udp")) {
                continue;
            }
            HostPort hp = splitHostPort(fields[3]);
            if (hp == null) {
                continue;
            }
            String process = "";
            int pid = 0;
            for (int i = 4; i < fields.length; i++) {
                Matcher m = NETSTAT_PROCESS.matcher(fields[i]);
                if (m.matches()) {
                    try {
                        pid = Integer.parseInt(m.group(1));
                    } catch (NumberFormatException ignored) { }
                    process = cleanNetstatProcessName(m.group(2));
                    break;
                }
            }
            ports.add(new ListeningPort(proto, hp.host, hp.port, process, pid, classifyExposure(hp.host)));
        }
        return ports;
    }

    /** netstat's program column holds a truncated title ("sshd: /usr/sbin"); keep only the name. */
    static String cleanNetstatProcessName(String name) {
        int idx = -1;
        for (char c : new char[]{':', ' ', '\t'}) {
            int i = name.indexOf(c);
            if (i >= 0 && (idx < 0 || i < idx)) {
                idx = i;
            }
        }
        if (idx >= 0) {
            name = name.substring(0, idx);
        }
        return name.trim();
    }

    private record HostPort(String host, int port) {
    }

    /** Splits at the last colon so both "0.0.0.0:22" and "[::]:22" work; a non-numeric port rejects the row. */
    static HostPort splitHostPort(String value) {
        int idx = value.lastIndexOf(':');
        if (idx < 0) {
            return null;
        }
        int port;
        try {
            port = Integer.parseInt(value.substring(idx + 1));
        } catch (NumberFormatException e) {
            return null;
        }
        if (port <= 0 || port > 65535) {
            return null;
        }
        String host = value.substring(0, idx);
        if (host.startsWith("[")) {
            host = host.substring(1);
        }
        if (host.endsWith("]")) {
            host = host.substring(0, host.length() - 1);
        }
        if ("*".equals(host)) {
            host = "::";
        }
        return new HostPort(host, port);
    }

    /**
     * Classifies the bind address by who can reach it: "public" for a wildcard
     * or routable bind (the one worth flagging — every interface, including the
     * public one the cloud attached), "private" for RFC1918 and link-local,
     * "loopback" for 127.0.0.0/8 and ::1.
     */
    static String classifyExposure(String addr) {
        InetAddress ip;
        try {
            ip = InetAddress.getByName(addr);
        } catch (Exception e) {
            return "unknown";
        }
        if (ip.isAnyLocalAddress()) {
            return "public";
        }
        if (ip.isLoopbackAddress()) {
            return "loopback";
        }
        if (ip.isSiteLocalAddress() || ip.isLinkLocalAddress() || ip.isMulticastAddress()) {
            return "private";
        }
        return "public";
    }

    private static final String WINDOWS_PORTS_SCRIPT = """
        $ErrorActionPreference='SilentlyContinue'
        $names = @{}
        Get-Process | ForEach-Object { $names[[int]$_.Id] = $_.ProcessName }
        Get-NetTCPConnection -State Listen | ForEach-Object {
          Write-Output ("{0}|{1}|{2}|{3}|{4}" -f 'tcp', $_.LocalAddress, $_.LocalPort, $_.OwningProcess, $names[[int]$_.OwningProcess])
        }
        Get-NetUDPEndpoint | ForEach-Object {
          Write-Output ("{0}|{1}|{2}|{3}|{4}" -f 'udp', $_.LocalAddress, $_.LocalPort, $_.OwningProcess, $names[[int]$_.OwningProcess])
        }""";

    public List<ListeningPort> listWindowsPorts(ClientSession client) throws IOException {
        String out = null;
        IOException err = null;
        try {
            out = SshSupport.runCmd(client, "powershell -NoProfile -NonInteractive -EncodedCommand "
                + WindowsProbes.encodePowerShell(WINDOWS_PORTS_SCRIPT));
        } catch (IOException e) {
            err = e;
        }
        List<ListeningPort> ports = parseWindowsPorts(out == null ? "" : out);
        if (ports.isEmpty() && err != null) {
            throw new IOException("list ports: " + err.getMessage(), err);
        }
        return ports;
    }

    /** "proto|address|port|pid|process" rows. */
    static List<ListeningPort> parseWindowsPorts(String out) {
        List<ListeningPort> ports = new ArrayList<>();
        for (String line : out.split("\n")) {
            String[] fields = line.trim().split("\\|");
            if (fields.length < 4) {
                continue;
            }
            int port;
            try {
                port = Integer.parseInt(fields[2]);
            } catch (NumberFormatException e) {
                continue;
            }
            if (port <= 0 || port > 65535) {
                continue;
            }
            String addr = fields[1];
            if (addr.startsWith("[")) {
                addr = addr.substring(1);
            }
            if (addr.endsWith("]")) {
                addr = addr.substring(0, addr.length() - 1);
            }
            int pid = 0;
            try {
                pid = Integer.parseInt(fields[3]);
            } catch (NumberFormatException ignored) { }
            String process = fields.length > 4 ? fields[4] : "";
            ports.add(new ListeningPort(fields[0], addr, port, process, pid, classifyExposure(addr)));
        }
        return ports;
    }

    /** Orders by port, then protocol, then address — the order a long table stays scannable in. */
    public static void sortListeningPorts(List<ListeningPort> ports) {
        ports.sort(Comparator.comparingInt(ListeningPort::port)
            .thenComparing(ListeningPort::protocol)
            .thenComparing(ListeningPort::address));
    }
}
