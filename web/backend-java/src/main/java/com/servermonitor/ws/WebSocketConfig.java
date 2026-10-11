package com.servermonitor.ws;

import java.nio.charset.StandardCharsets;
import java.util.List;
import java.util.Map;
import java.util.UUID;

import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.context.annotation.Configuration;
import org.springframework.http.HttpHeaders;
import org.springframework.http.server.ServerHttpRequest;
import org.springframework.http.server.ServerHttpResponse;
import org.springframework.lang.NonNull;
import org.springframework.web.socket.WebSocketHandler;
import org.springframework.web.socket.config.annotation.EnableWebSocket;
import org.springframework.web.socket.config.annotation.WebSocketConfigurer;
import org.springframework.web.socket.config.annotation.WebSocketHandlerRegistry;
import org.springframework.web.socket.server.HandshakeInterceptor;

import com.servermonitor.repo.Users;
import com.servermonitor.security.JwtService;

/**
 * WebSocket handshake auth mirroring middleware.WSAuthRequired: the token comes
 * from the "bearer, <jwt>" subprotocol offer (JWTs are valid subprotocol names,
 * so they stay out of URLs and access logs); the legacy ?token= query parameter
 * is still accepted. On success a one-shot ws_id binds the accepted socket to a
 * prepared terminal.
 */
@Configuration
@EnableWebSocket
public class WebSocketConfig implements WebSocketConfigurer {

    private static final Logger log = LoggerFactory.getLogger(WebSocketConfig.class);

    private final TerminalWebSocketHandler terminal;
    private final JwtService jwt;
    private final Users users;

    public WebSocketConfig(TerminalWebSocketHandler terminal, JwtService jwt, Users users) {
        this.terminal = terminal;
        this.jwt = jwt;
        this.users = users;
    }

    @Override
    public void registerWebSocketHandlers(@NonNull WebSocketHandlerRegistry registry) {
        registry.addHandler(terminal, "/api/ssh/{id}", "/api/ws/servers/{id}/docker/containers/{containerId}/exec")
            .setAllowedOriginPatterns("*")
            .addInterceptors(new HandshakeInterceptor() {
                @Override
                public boolean beforeHandshake(@NonNull ServerHttpRequest request,
                    @NonNull ServerHttpResponse response, @NonNull WebSocketHandler wsHandler,
                    @NonNull Map<String, Object> attributes) {
                    String token = protocolToken(request.getHeaders().get("Sec-WebSocket-Protocol"));
                    if (token.isEmpty()) {
                        token = queryParam(request.getURI().getQuery(), "token");
                    }
                    if (token.isEmpty()) {
                        response.setStatusCode(org.springframework.http.HttpStatus.UNAUTHORIZED);
                        return false;
                    }
                    JwtService.TokenClaims claims = jwt.parse(token);
                    if (claims == null) {
                        response.setStatusCode(org.springframework.http.HttpStatus.UNAUTHORIZED);
                        return false;
                    }
                    java.time.Instant validAfter;
                    try {
                        validAfter = users.getTokensValidAfter(claims.userId());
                    } catch (Exception e) {
                        log.warn("ws auth: read token cutoff for {}: {}", claims.userId(), e.toString());
                        response.setStatusCode(org.springframework.http.HttpStatus.SERVICE_UNAVAILABLE);
                        return false;
                    }
                    if (validAfter != null && claims.issuedAt() != null && claims.issuedAt().isBefore(validAfter)) {
                        response.setStatusCode(org.springframework.http.HttpStatus.UNAUTHORIZED);
                        return false;
                    }
                    String path = request.getURI().getPath();
                    UUID serverId;
                    String containerId = null;
                    TerminalWebSocketHandler.Kind kind;
                    try {
                        if (path.startsWith("/api/ssh/")) {
                            kind = TerminalWebSocketHandler.Kind.SSH;
                            serverId = UUID.fromString(path.substring("/api/ssh/".length()));
                        } else {
                            kind = TerminalWebSocketHandler.Kind.DOCKER_EXEC;
                            String prefix = "/api/ws/servers/";
                            String[] parts = path.substring(prefix.length()).split("/");
                            serverId = UUID.fromString(parts[0]);
                            containerId = parts[3];
                        }
                    } catch (IllegalArgumentException | ArrayIndexOutOfBoundsException e) {
                        response.setStatusCode(org.springframework.http.HttpStatus.BAD_REQUEST);
                        return false;
                    }
                    String wsId = UUID.randomUUID().toString();
                    TerminalWebSocketHandler.prepare(wsId, kind, serverId, claims.userId(), containerId);
                    attributes.put("ws_id", wsId);
                    HttpHeaders extra = response.getHeaders();
                    // Echo "bearer" so browsers accept the handshake.
                    extra.put("Sec-WebSocket-Protocol", List.of("bearer"));
                    return true;
                }

                @Override
                public void afterHandshake(@NonNull ServerHttpRequest request,
                    @NonNull ServerHttpResponse response, @NonNull WebSocketHandler wsHandler,
                    Exception exception) {
                }
            });
    }

    private static String protocolToken(List<String> offers) {
        if (offers == null) {
            return "";
        }
        for (String offer : offers) {
            for (String part : offer.split(",")) {
                String trimmed = part.trim();
                if (!trimmed.isEmpty() && !trimmed.equalsIgnoreCase("bearer")) {
                    return trimmed;
                }
            }
        }
        return "";
    }

    private static String queryParam(String query, String name) {
        if (query == null) {
            return "";
        }
        for (String pair : query.split("&")) {
            int idx = pair.indexOf('=');
            if (idx > 0 && pair.substring(0, idx).equals(name)) {
                return java.net.URLDecoder.decode(pair.substring(idx + 1), StandardCharsets.UTF_8);
            }
        }
        return "";
    }
}
