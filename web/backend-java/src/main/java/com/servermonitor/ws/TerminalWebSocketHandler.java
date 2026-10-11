package com.servermonitor.ws;

import java.io.IOException;
import java.io.InputStream;
import java.io.PipedInputStream;
import java.io.PipedOutputStream;
import java.net.URI;
import java.net.URLDecoder;
import java.nio.ByteBuffer;
import java.nio.charset.StandardCharsets;
import java.util.Map;
import java.util.UUID;
import java.util.concurrent.ConcurrentHashMap;

import org.apache.sshd.client.channel.ChannelShell;
import org.apache.sshd.client.session.ClientSession;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.lang.NonNull;
import org.springframework.stereotype.Component;
import org.springframework.web.socket.BinaryMessage;
import org.springframework.web.socket.CloseStatus;
import org.springframework.web.socket.TextMessage;
import org.springframework.web.socket.WebSocketSession;
import org.springframework.web.socket.handler.AbstractWebSocketHandler;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.servermonitor.repo.Servers;
import com.servermonitor.repo.Servers.ServerSecrets;
import com.servermonitor.ssh.SshSupport;

/**
 * The SSH web terminal and the docker exec session, mirroring
 * handlers/ssh.go + services/ssh.go's TerminalSession. Client→server messages
 * prefixed with 0x01 carry a JSON control payload (terminal resize); everything
 * else — binary or text — is forwarded to the PTY's stdin. Server→client
 * chunks are binary frames so split UTF-8 sequences survive intact.
 */
@Component
public class TerminalWebSocketHandler extends AbstractWebSocketHandler {

    private static final Logger log = LoggerFactory.getLogger(TerminalWebSocketHandler.class);
    private static final ObjectMapper JSON = new ObjectMapper();

    private static final Map<String, Terminal> TERMINALS = new ConcurrentHashMap<>();

    private final Servers servers;

    public TerminalWebSocketHandler(Servers servers) {
        this.servers = servers;
    }

    enum Kind {SSH, DOCKER_EXEC}

    /** Registered per websocket id before the handshake is accepted. */
    static void prepare(String wsId, Kind kind, UUID serverId, UUID userId, String containerId) {
        TERMINALS.put(wsId, new Terminal(kind, serverId, userId, containerId));
    }

    private record Terminal(Kind kind, UUID serverId, UUID userId, String containerId) {
    }

    private static final class Pipe {
        final PipedOutputStream source = new PipedOutputStream();
        final PipedInputStream sink;

        Pipe() throws IOException {
            sink = new PipedInputStream(source, 1 << 16);
        }
    }

    private record SessionState(ClientSession client, ChannelShell channel,
        PipedOutputStream stdin, Pipe out) {
    }

    @Override
    public void afterConnectionEstablished(@NonNull WebSocketSession ws) throws Exception {
        String wsId = queryParam(ws, "ws_id");
        if (wsId == null) {
            Object attr = ws.getAttributes().get("ws_id");
            wsId = attr instanceof String s ? s : null;
        }
        Terminal terminal = wsId == null ? null : TERMINALS.remove(wsId);
        if (terminal == null) {
            ws.close(CloseStatus.POLICY_VIOLATION.withReason("no prepared terminal"));
            return;
        }
        ServerSecrets server = servers.getServerByIdAndUser(terminal.serverId(), terminal.userId())
            .orElse(null);
        if (server == null) {
            ws.close(CloseStatus.SERVER_ERROR.withReason("server not found"));
            return;
        }
        ClientSession client;
        try {
            client = SshSupport.dial(server.host, server.port, server.sshUsername,
                server.sshPassword, server.sshKey, server.sshHostKey);
        } catch (Exception e) {
            ws.sendMessage(new TextMessage("SSH connection failed: " + e.getMessage()));
            ws.close(CloseStatus.SERVER_ERROR);
            return;
        }
        ChannelShell channel = null;
        try {
            channel = client.createShellChannel();
            channel.setPtyType("xterm-256color");
            channel.setPtyColumns(80);
            channel.setPtyLines(40);
            Pipe out = new Pipe();
            channel.setOut(out.source);
            channel.setErr(out.source);
            PipedOutputStream stdin = new PipedOutputStream();
            PipedInputStream stdinReader = new PipedInputStream(stdin, 1 << 16);
            channel.setIn(stdinReader);
            channel.open().verify(SshSupport.CHANNEL_OPEN_TIMEOUT.toMillis());

            SessionState state = new SessionState(client, channel, stdin, out);
            ws.getAttributes().put("terminal", state);

            if (terminal.kind() == Kind.DOCKER_EXEC && terminal.containerId() != null) {
                // Wait for the shell, then start docker exec (bash preferred, sh fallback).
                final PipedOutputStream in = stdin;
                Thread starter = new Thread(() -> {
                    try {
                        Thread.sleep(500);
                        String shell = "sh -c 'command -v bash >/dev/null && exec bash || exec sh'";
                        String cmd = String.format(
                            "if docker ps >/dev/null 2>&1; then docker exec -it %s %s; else sudo docker exec -it %s %s; fi\r",
                            terminal.containerId(), shell, terminal.containerId(), shell);
                        synchronized (TerminalWebSocketHandler.this) {
                            in.write(cmd.getBytes(StandardCharsets.UTF_8));
                            in.flush();
                        }
                    } catch (Exception e) {
                        log.debug("docker exec kickoff: {}", e.toString());
                    }
                }, "docker-exec-kickoff");
                starter.setDaemon(true);
                starter.start();
            }

            // SSH output → websocket (binary frames preserve split UTF-8 sequences).
            ChannelShell pumpChannel = channel;
            Thread pump = new Thread(() -> {
                byte[] buf = new byte[16 * 1024];
                try (InputStream in = out.sink) {
                    while (true) {
                        int read = in.read(buf);
                        if (read < 0) {
                            break;
                        }
                        synchronized (ws) {
                            if (!ws.isOpen()) {
                                break;
                            }
                            ws.sendMessage(new BinaryMessage(ByteBuffer.wrap(buf, 0, read)));
                        }
                    }
                } catch (IOException ignored) {
                    // Channel closed during teardown.
                }
                closeQuietly(ws, pumpChannel);
            }, "terminal-out-" + ws.getId());
            pump.setDaemon(true);
            pump.start();
        } catch (Exception e) {
            if (channel != null) {
                try {
                    channel.close(true);
                } catch (Exception ignored) { }
            }
            try {
                client.close(true);
            } catch (Exception ignored) { }
            log.debug("terminal setup: {}", e.toString());
            ws.sendMessage(new TextMessage("PTY allocation failed: " + e.getMessage()));
            ws.close(CloseStatus.SERVER_ERROR);
        }
    }

    @Override
    protected void handleBinaryMessage(@NonNull WebSocketSession ws, @NonNull BinaryMessage message) {
        SessionState state = (SessionState) ws.getAttributes().get("terminal");
        if (state == null) {
            return;
        }
        ByteBuffer payload = message.getPayload();
        byte[] data = new byte[payload.remaining()];
        payload.get(data);
        forward(ws, state, data);
    }

    @Override
    protected void handleTextMessage(@NonNull WebSocketSession ws, @NonNull TextMessage message) {
        SessionState state = (SessionState) ws.getAttributes().get("terminal");
        if (state == null) {
            return;
        }
        forward(ws, state, message.getPayload().getBytes(StandardCharsets.UTF_8));
    }

    private void forward(WebSocketSession ws, SessionState state, byte[] data) {
        if (data.length == 0) {
            return;
        }
        try {
            if (data[0] == 0x01) {
                handleControl(state, new String(data, 1, data.length - 1, StandardCharsets.UTF_8));
                return;
            }
            synchronized (ws) {
                state.stdin().write(data);
                state.stdin().flush();
            }
        } catch (IOException e) {
            closeQuietly(ws, state.channel());
        }
    }

    private void handleControl(SessionState state, String json) {
        try {
            JsonNode node = JSON.readTree(json);
            if (!"resize".equals(node.path("type").asText())) {
                return;
            }
            int cols = node.path("cols").asInt(0);
            int rows = node.path("rows").asInt(0);
            if (cols > 0 && rows > 0) {
                state.channel().sendWindowChange(cols, rows);
            }
        } catch (Exception e) {
            log.debug("terminal control: {}", e.toString());
        }
    }

    @Override
    public void afterConnectionClosed(@NonNull WebSocketSession ws, @NonNull CloseStatus status) {
        SessionState state = (SessionState) ws.getAttributes().remove("terminal");
        if (state == null) {
            return;
        }
        try {
            state.stdin().close();
        } catch (IOException ignored) { }
        try {
            state.channel().close(true);
        } catch (Exception ignored) { }
        try {
            state.client().close(true);
        } catch (Exception ignored) { }
    }

    private static void closeQuietly(WebSocketSession ws, ChannelShell channel) {
        try {
            channel.close(true);
        } catch (Exception ignored) { }
        try {
            if (ws.isOpen()) {
                ws.close();
            }
        } catch (Exception ignored) { }
    }

    private static String queryParam(WebSocketSession ws, String name) {
        URI uri = ws.getUri();
        if (uri == null || uri.getQuery() == null) {
            return null;
        }
        for (String pair : uri.getQuery().split("&")) {
            int idx = pair.indexOf('=');
            if (idx > 0 && pair.substring(0, idx).equals(name)) {
                return URLDecoder.decode(pair.substring(idx + 1), StandardCharsets.UTF_8);
            }
        }
        return null;
    }

}
