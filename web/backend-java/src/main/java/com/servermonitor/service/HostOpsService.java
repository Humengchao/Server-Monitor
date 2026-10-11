package com.servermonitor.service;

import java.io.IOException;
import java.util.ArrayList;
import java.util.Comparator;
import java.util.List;
import java.util.Map;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

import org.apache.sshd.client.session.ClientSession;
import org.springframework.stereotype.Service;

import com.servermonitor.ssh.SshSupport;

/** Service inventories (systemd/sysv/Windows SCM) over SSH, mirroring services/units.go. */
@Service
public class HostOpsService {

    /** One entry of a host's service inventory; platforms that report less leave fields empty. */
    public record ServiceUnit(String name, String load, String active, String sub,
        String enabled, String description) {
    }

    public static final int MAX_SERVICE_UNITS = 500;

    public enum ServiceManager {SYSTEMD, SYSV, WINDOWS, NONE}

    /** The host offers no way to enumerate services. */
    public static class NoServiceManagerException extends IOException {
        public NoServiceManagerException() {
            super("no service manager available to this SSH user: neither systemctl nor a service script is on its PATH");
        }
    }

    /** systemd is installed but did not answer — almost always an unprivileged SSH user. */
    public static class ServiceManagerUnreachableException extends IOException {
        public ServiceManagerUnreachableException() {
            super("systemd is installed but did not respond; this SSH user may not be permitted to query it");
        }
    }

    public record ServiceInventory(List<ServiceUnit> units, ServiceManager manager) {
    }

    private static final String LINUX_UNITS_COMMAND =
        "systemctl list-units --type=service --all --no-pager --plain --no-legend 2>/dev/null | head -c 500000";
    private static final String LINUX_UNIT_FILES_COMMAND =
        "systemctl list-unit-files --type=service --no-pager --plain --no-legend 2>/dev/null | head -c 500000";
    private static final String LINUX_SERVICE_FALLBACK_COMMAND =
        "service --status-all 2>&1 | head -c 200000";

    public ServiceInventory listLinuxServices(ClientSession client) throws IOException {
        String out;
        try {
            out = SshSupport.runCmd(client, LINUX_UNITS_COMMAND);
        } catch (IOException e) {
            out = "";
        }
        List<ServiceUnit> units = parseSystemdUnits(out);
        if (!units.isEmpty()) {
            try {
                String files = SshSupport.runCmd(client, LINUX_UNIT_FILES_COMMAND);
                applyUnitFileStates(units, parseUnitFileStates(files));
            } catch (IOException ignored) {
                // The boot disposition is a nice-to-have; the inventory stands without it.
            }
            return new ServiceInventory(units, ServiceManager.SYSTEMD);
        }

        boolean[] tools = probeServiceTools(client);
        if (tools[0]) {
            // On a systemd host the init scripts answer without consulting
            // systemd, and without privilege they answer wrongly.
            throw new ServiceManagerUnreachableException();
        }
        if (tools[1]) {
            String fallback;
            try {
                fallback = SshSupport.runCmd(client, LINUX_SERVICE_FALLBACK_COMMAND);
            } catch (IOException e) {
                throw new IOException("list services: " + e.getMessage(), e);
            }
            List<ServiceUnit> legacy = parseSysvServices(fallback);
            if (!legacy.isEmpty()) {
                return new ServiceInventory(legacy, ServiceManager.SYSV);
            }
        }
        throw new NoServiceManagerException();
    }

    /** Asks the remote shell which managers are on PATH (PATH decides, not the filesystem). */
    private boolean[] probeServiceTools(ClientSession client) {
        String out;
        try {
            out = SshSupport.runCmd(client,
                "command -v systemctl >/dev/null 2>&1 && echo systemctl; command -v service >/dev/null 2>&1 && echo service; true");
        } catch (IOException e) {
            out = "";
        }
        boolean systemctl = false, service = false;
        for (String line : out.split("\n")) {
            switch (line.trim()) {
                case "systemctl" -> systemctl = true;
                case "service" -> service = true;
                default -> { }
            }
        }
        return new boolean[]{systemctl, service};
    }

    // The status glyph an older systemd may print even with --plain.
    private static final Pattern UNIT_BULLET = Pattern.compile("^[\\p{Zs}]*[●*→↻×✕✗]+[\\p{Zs}]*");

    /** Reads "UNIT LOAD ACTIVE SUB DESCRIPTION" rows; rows not ending in .service are discarded. */
    static List<ServiceUnit> parseSystemdUnits(String out) {
        List<ServiceUnit> units = new ArrayList<>();
        for (String line : out.split("\n")) {
            line = UNIT_BULLET.matcher(line).replaceAll("");
            String[] fields = line.trim().split("\\s+");
            if (fields.length < 4 || !fields[0].endsWith(".service")) {
                continue;
            }
            String description = fields.length > 4
                ? String.join(" ", java.util.Arrays.copyOfRange(fields, 4, fields.length)) : "";
            units.add(new ServiceUnit(fields[0], fields[1], fields[2], fields[3], "", description));
        }
        return units;
    }

    /** Reads "UNIT FILE STATE [VENDOR PRESET]" rows (only the first two fields are stable). */
    static Map<String, String> parseUnitFileStates(String out) {
        Map<String, String> states = new java.util.HashMap<>();
        for (String line : out.split("\n")) {
            String[] fields = line.trim().split("\\s+");
            if (fields.length < 2 || !fields[0].endsWith(".service")) {
                continue;
            }
            states.put(fields[0], fields[1]);
        }
        return states;
    }

    static void applyUnitFileStates(List<ServiceUnit> units, Map<String, String> states) {
        for (int i = 0; i < units.size(); i++) {
            String state = states.get(units.get(i).name());
            if (state != null) {
                ServiceUnit u = units.get(i);
                units.set(i, new ServiceUnit(u.name(), u.load(), u.active(), u.sub(), state, u.description()));
            }
        }
    }

    // Debian's "service --status-all": "[ + ]" running, "[ - ]" stopped, "[ ? ]" declines.
    private static final Pattern SYSV_ROW = Pattern.compile("^\\s*\\[\\s*([+?-])\\s*\\]\\s+(\\S+)\\s*$");

    static List<ServiceUnit> parseSysvServices(String out) {
        List<ServiceUnit> units = new ArrayList<>();
        for (String line : out.split("\n")) {
            Matcher m = SYSV_ROW.matcher(line);
            if (!m.find()) {
                continue;
            }
            String active = switch (m.group(1)) {
                case "+" -> "active";
                case "?" -> "unknown";
                default -> "inactive";
            };
            units.add(new ServiceUnit(m.group(2), "", active, "", "", ""));
        }
        return units;
    }

    private static final String WINDOWS_SERVICES_SCRIPT = """
        $ErrorActionPreference='SilentlyContinue'
        Get-Service | ForEach-Object {
          $state = if ($_.Status -eq 'Running') { 'active' } else { 'inactive' }
          Write-Output ("{0}|{1}|{2}|{3}|{4}" -f $_.Name, $state, "$($_.Status)".ToLower(), "$($_.StartType)".ToLower(), $_.DisplayName)
        }""";

    public List<ServiceUnit> listWindowsServices(ClientSession client) throws IOException {
        String cmd = "powershell -NoProfile -NonInteractive -EncodedCommand "
            + WindowsProbes.encodePowerShell(WINDOWS_SERVICES_SCRIPT);
        String out;
        try {
            out = SshSupport.runCmd(client, cmd);
        } catch (IOException e) {
            throw new IOException("list services: " + e.getMessage(), e);
        }
        return parseWindowsServices(out);
    }

    /** Reads "name|active|status|starttype|displayname" rows. */
    static List<ServiceUnit> parseWindowsServices(String out) {
        List<ServiceUnit> units = new ArrayList<>();
        for (String line : out.split("\n")) {
            String[] fields = line.trim().split("\\|", 5);
            if (fields.length < 4 || fields[0].isEmpty()) {
                continue;
            }
            String description = fields.length > 4 ? fields[4] : "";
            units.add(new ServiceUnit(fields[0], "", fields[1], fields[2], fields[3], description));
        }
        return units;
    }

    /** Failed first, then running, then the rest, alphabetical inside each group. */
    public static void sortServiceUnits(List<ServiceUnit> units) {
        units.sort(Comparator.comparingInt(HostOpsService::unitRank).thenComparing(ServiceUnit::name));
    }

    private static int unitRank(ServiceUnit u) {
        if ("failed".equals(u.active()) || "failed".equals(u.sub())) {
            return 0;
        }
        if ("active".equals(u.active())) {
            return 1;
        }
        if ("activating".equals(u.active()) || "deactivating".equals(u.active())) {
            return 2;
        }
        return 3;
    }

    public static final Map<String, Boolean> SERVICE_ACTIONS = Map.of(
        "start", true, "stop", true, "restart", true, "reload", true);

    /**
     * Deliberately narrow: every admitted character is inert to a shell, which
     * is what makes interpolating a name into a command line safe without quoting.
     */
    private static final Pattern UNIT_NAME_PATTERN = Pattern.compile("^[A-Za-z0-9][A-Za-z0-9@._:-]{0,127}$");

    public static boolean validServiceName(String name) {
        return name != null && UNIT_NAME_PATTERN.matcher(name).matches();
    }

    /**
     * Runs one control verb against one unit and reports the host's own
     * diagnostic on failure. Nothing escalates privilege here: systemctl and the
     * SCM both refuse a non-privileged caller, and that refusal is surfaced
     * rather than retried under sudo.
     */
    public void controlService(ClientSession client, String name, String action, String serverType)
        throws IOException {
        if (!SERVICE_ACTIONS.containsKey(action)) {
            throw new IOException("unsupported action \"" + action + "\"");
        }
        if (!validServiceName(name)) {
            throw new IOException("unsupported service name");
        }
        String cmd;
        if ("windows".equals(serverType)) {
            String verb = switch (action) {
                case "start" -> "Start-Service";
                case "stop" -> "Stop-Service";
                case "restart" -> "Restart-Service";
                // Mapping reload onto Restart-Service would quietly drop every
                // connection the caller chose reload to avoid.
                default -> throw new IOException(action + " is not supported on Windows");
            };
            cmd = "powershell -NoProfile -NonInteractive -EncodedCommand "
                + WindowsProbes.encodePowerShell(verb + " -Name '" + name + "' -ErrorAction Stop");
        } else {
            cmd = "systemctl " + action + " " + name;
        }
        try {
            SshSupport.runCmdCombined(client, cmd);
        } catch (SshSupport.CommandFailedException e) {
            String detail = ProcessService.cleanCommandError(e.getMessage());
            throw new IOException(detail.isEmpty() ? e.getMessage() : detail, e);
        }
    }
}
