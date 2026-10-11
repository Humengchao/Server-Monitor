package com.servermonitor.api;

import java.io.IOException;
import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.UUID;

import org.apache.sshd.client.session.ClientSession;
import org.springframework.web.bind.annotation.DeleteMapping;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RequestParam;
import org.springframework.web.bind.annotation.RestController;

import com.servermonitor.repo.Servers;
import com.servermonitor.repo.Servers.ServerSecrets;
import com.servermonitor.service.HostOpsService;
import com.servermonitor.service.PortService;
import com.servermonitor.service.ProcessService;
import com.servermonitor.ssh.SshConnectionCache;

import jakarta.servlet.http.HttpServletRequest;

/**
 * The host-facing read-only and control endpoints that run over the shared SSH
 * connection cache: processes, systemd/sysv services, listening ports. Ownership
 * checks and "invalid id" answers are identical to the Go handlers' shape.
 */
@RestController
@RequestMapping("/api/servers")
public class HostControllers {

    private final Servers servers;
    private final SshConnectionCache sshCache;
    private final ProcessService processes;
    private final HostOpsService hostOps;
    private final PortService ports;

    public HostControllers(Servers servers, SshConnectionCache sshCache, ProcessService processes,
        HostOpsService hostOps, PortService ports) {
        this.servers = servers;
        this.sshCache = sshCache;
        this.processes = processes;
        this.hostOps = hostOps;
        this.ports = ports;
    }

    /** Loads a server the caller owns — malformed ids and foreign servers answer identically. */
    ServerSecrets resolveOwned(String id, HttpServletRequest req) {
        UUID userId = AuthSupport.userId(req);
        UUID serverId = UuidParser.parse(id);
        return servers.getServerByIdAndUser(serverId, userId)
            .orElseThrow(() -> ApiException.notFound("server not found"));
    }

    private ClientSession connect(ServerSecrets server) {
        try {
            return sshCache.get(server);
        } catch (Exception e) {
            throw ApiException.of(502, "SSH connection failed");
        }
    }

    // --- processes ----------------------------------------------------------

    @GetMapping("/{id}/processes")
    public Map<String, Object> listProcesses(@PathVariable String id, HttpServletRequest req) {
        ServerSecrets server = resolveOwned(id, req);
        ClientSession client = connect(server);
        List<ProcessService.ProcessInfo> procs;
        try {
            procs = "windows".equals(server.serverType)
                ? processes.listWindowsProcesses(client)
                : processes.listLinuxProcesses(client);
        } catch (IOException e) {
            throw ApiException.of(502, "failed to read the process list");
        }
        int total = procs.size();
        List<ProcessService.ProcessInfo> top = ProcessService.topProcesses(procs, ProcessService.MAX_PROCESSES);
        List<Map<String, Object>> out = new ArrayList<>();
        for (ProcessService.ProcessInfo p : top) {
            Map<String, Object> row = new LinkedHashMap<>();
            row.put("pid", p.pid());
            row.put("user", p.user());
            row.put("cpu_percent", p.cpuPercent());
            row.put("mem_percent", p.memPercent());
            row.put("rss_bytes", p.rssBytes());
            row.put("state", p.state());
            row.put("elapsed_seconds", p.elapsedSeconds());
            row.put("command", p.command());
            out.add(row);
        }
        // total lets the UI say "showing 300 of 812".
        return Map.of("processes", out, "total", total, "returned", out.size());
    }

    @DeleteMapping("/{id}/processes/{pid}")
    public Map<String, Object> killProcess(@PathVariable String id, @PathVariable String pid,
        @RequestParam(required = false) String force, HttpServletRequest req) {
        int pidNum;
        try {
            pidNum = Integer.parseInt(pid);
        } catch (NumberFormatException e) {
            throw ApiException.badRequest("invalid pid");
        }
        if (pidNum <= 0) {
            throw ApiException.badRequest("invalid pid");
        }
        ServerSecrets server = resolveOwned(id, req);
        ClientSession client = connect(server);
        try {
            processes.killProcess(client, pidNum, server.serverType, "1".equals(force));
        } catch (IOException e) {
            throw ApiException.of(502, e.getMessage());
        }
        return Map.of("message", "signal sent");
    }

    // --- services -----------------------------------------------------------

    @GetMapping("/{id}/services")
    public Map<String, Object> listServices(@PathVariable String id, HttpServletRequest req) {
        ServerSecrets server = resolveOwned(id, req);
        ClientSession client = connect(server);
        List<HostOpsService.ServiceUnit> units;
        String manager;
        try {
            if ("windows".equals(server.serverType)) {
                units = hostOps.listWindowsServices(client);
                manager = "windows";
            } else {
                var inventory = hostOps.listLinuxServices(client);
                units = inventory.units();
                manager = inventory.manager().name().toLowerCase();
            }
        } catch (HostOpsService.NoServiceManagerException | HostOpsService.ServiceManagerUnreachableException e) {
            // Not an API failure: the host genuinely cannot be asked.
            return Map.of(
                "services", List.of(), "total", 0, "returned", 0,
                "supported", false,
                "reason_code", e instanceof HostOpsService.ServiceManagerUnreachableException
                    ? "unreachable" : "absent",
                "reason", e.getMessage());
        } catch (IOException e) {
            throw ApiException.of(502, "failed to read the service list");
        }
        int total = units.size();
        HostOpsService.sortServiceUnits(units);
        if (units.size() > HostOpsService.MAX_SERVICE_UNITS) {
            units = new ArrayList<>(units.subList(0, HostOpsService.MAX_SERVICE_UNITS));
        }
        List<Map<String, Object>> out = new ArrayList<>();
        for (HostOpsService.ServiceUnit u : units) {
            Map<String, Object> row = new LinkedHashMap<>();
            row.put("name", u.name());
            row.put("load", u.load());
            row.put("active", u.active());
            row.put("sub", u.sub());
            row.put("enabled", u.enabled());
            row.put("description", u.description());
            out.add(row);
        }
        return Map.of("services", out, "manager", manager, "total", total,
            "returned", out.size(), "supported", true);
    }

    public record ServiceControlBody(String name, String action) {
    }

    @PostMapping("/{id}/services/control")
    public Map<String, Object> controlService(@PathVariable String id,
        @RequestBody(required = false) ServiceControlBody body, HttpServletRequest req) {
        if (body == null || body.name() == null || body.action() == null) {
            throw ApiException.badRequest("name and action are required");
        }
        // Validated before opening a connection: a rejected name should not cost an SSH session.
        if (!HostOpsService.SERVICE_ACTIONS.containsKey(body.action())) {
            throw ApiException.badRequest("unsupported action");
        }
        if (!HostOpsService.validServiceName(body.name())) {
            throw ApiException.badRequest("unsupported service name");
        }
        ServerSecrets server = resolveOwned(id, req);
        ClientSession client = connect(server);
        try {
            hostOps.controlService(client, body.name(), body.action(), server.serverType);
        } catch (IOException e) {
            // The host's own refusal ("Access denied", "Unit not found") is the useful part.
            throw ApiException.of(502, e.getMessage());
        }
        return Map.of("message", "command sent");
    }

    // --- ports ----------------------------------------------------------------

    @GetMapping("/{id}/ports")
    public Map<String, Object> listPorts(@PathVariable String id, HttpServletRequest req) {
        ServerSecrets server = resolveOwned(id, req);
        ClientSession client = connect(server);
        List<PortService.ListeningPort> list;
        try {
            list = "windows".equals(server.serverType)
                ? ports.listWindowsPorts(client)
                : ports.listLinuxPorts(client);
        } catch (IOException e) {
            throw ApiException.of(502, "failed to read the listening ports");
        }
        int total = list.size();
        PortService.sortListeningPorts(list);
        if (list.size() > PortService.MAX_LISTENING_PORTS) {
            list = new ArrayList<>(list.subList(0, PortService.MAX_LISTENING_PORTS));
        }
        List<Map<String, Object>> out = new ArrayList<>();
        for (PortService.ListeningPort p : list) {
            Map<String, Object> row = new LinkedHashMap<>();
            row.put("protocol", p.protocol());
            row.put("address", p.address());
            row.put("port", p.port());
            row.put("process", p.process());
            row.put("pid", p.pid());
            row.put("exposure", p.exposure());
            out.add(row);
        }
        return Map.of("ports", out, "total", total, "returned", out.size());
    }
}
