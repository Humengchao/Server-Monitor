package com.servermonitor.security;

import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.time.Instant;
import java.util.Date;
import java.util.UUID;

import javax.crypto.SecretKey;

import org.springframework.stereotype.Service;

import com.servermonitor.config.MonitorConfig;

import io.jsonwebtoken.Claims;
import io.jsonwebtoken.JwtException;
import io.jsonwebtoken.Jwts;
import io.jsonwebtoken.security.Keys;

/** HS256 JWT issuing and parsing, mirroring the Go tokens (72h TTL, user_id + username claims). */
@Service
public class JwtService {

    public static final Duration TOKEN_TTL = Duration.ofHours(72);

    private final SecretKey key;

    public JwtService(MonitorConfig cfg) {
        this.key = Keys.hmacShaKeyFor(cfg.getJwtSecret().getBytes(StandardCharsets.UTF_8));
    }

    /** issuedAt is explicit so a password change can mint the replacement token at the revocation cutoff. */
    public String generateToken(UUID userId, String username, Instant issuedAt) {
        return Jwts.builder()
            .claim("user_id", userId.toString())
            .claim("username", username)
            .issuedAt(Date.from(issuedAt))
            .expiration(Date.from(issuedAt.plus(TOKEN_TTL)))
            .signWith(key, Jwts.SIG.HS256)
            .compact();
    }

    /**
     * Parses and validates a token. Returns null for any invalid token.
     * Only HS256 is accepted, closing algorithm-confusion attacks the same way
     * jwt.WithValidMethods did for the Go server.
     */
    public TokenClaims parse(String token) {
        try {
            Claims claims = Jwts.parser()
                .verifyWith(key)
                .build()
                .parseSignedClaims(token)
                .getPayload();
            String userId = claims.get("user_id", String.class);
            if (userId == null || userId.isBlank()) {
                return null;
            }
            UUID id;
            try {
                id = UUID.fromString(userId);
            } catch (IllegalArgumentException e) {
                return null;
            }
            String username = claims.get("username", String.class);
            Date issuedAt = claims.getIssuedAt();
            return new TokenClaims(id, username == null ? "" : username,
                issuedAt == null ? null : issuedAt.toInstant());
        } catch (JwtException | IllegalArgumentException e) {
            return null;
        }
    }

    public record TokenClaims(UUID userId, String username, Instant issuedAt) {
    }
}
