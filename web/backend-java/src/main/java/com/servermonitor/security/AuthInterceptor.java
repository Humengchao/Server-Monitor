package com.servermonitor.security;

import java.io.IOException;
import java.time.Instant;
import java.util.UUID;

import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.http.HttpHeaders;
import org.springframework.lang.NonNull;
import org.springframework.stereotype.Component;
import org.springframework.web.servlet.HandlerInterceptor;

import com.fasterxml.jackson.databind.ObjectMapper;
import com.servermonitor.repo.Users;

import jakarta.servlet.http.HttpServletRequest;
import jakarta.servlet.http.HttpServletResponse;

/**
 * Validates the Authorization: Bearer JWT for the panel API.
 *
 * Mirrors middleware.AuthRequired: 401 for missing/invalid/expired tokens, but
 * 503 when the revocation-cutoff lookup itself fails — a database blip must not
 * sign every user out, and the client treats 401 as "log out".
 */
@Component
public class AuthInterceptor implements HandlerInterceptor {

    public static final String ATTR_USER_ID = "monitor.userId";
    public static final String ATTR_USERNAME = "monitor.username";

    private static final Logger log = LoggerFactory.getLogger(AuthInterceptor.class);
    private static final ObjectMapper JSON = new ObjectMapper();

    private final JwtService jwt;
    private final Users users;

    public AuthInterceptor(JwtService jwt, Users users) {
        this.jwt = jwt;
        this.users = users;
    }

    @Override
    public boolean preHandle(@NonNull HttpServletRequest req, @NonNull HttpServletResponse res,
        @NonNull Object handler) throws IOException {
        String token = extractBearerToken(req);
        if (token.isEmpty()) {
            reject(res, 401, "missing authorization");
            return false;
        }
        JwtService.TokenClaims claims = jwt.parse(token);
        if (claims == null) {
            reject(res, 401, "invalid token");
            return false;
        }
        if (!issuedAfterCutoff(res, claims)) {
            return false;
        }
        req.setAttribute(ATTR_USER_ID, claims.userId());
        req.setAttribute(ATTR_USERNAME, claims.username());
        return true;
    }

    private boolean issuedAfterCutoff(HttpServletResponse res, JwtService.TokenClaims claims) throws IOException {
        Instant validAfter;
        try {
            validAfter = users.getTokensValidAfter(claims.userId());
        } catch (Exception e) {
            log.warn("auth: read token cutoff for {}: {}", claims.userId(), e.toString());
            reject(res, 503, "authentication temporarily unavailable");
            return false;
        }
        if (validAfter == null) {
            // Tokens minted before "iat" was added carry no issue time and stay
            // valid until their normal expiry.
            return true;
        }
        if (claims.issuedAt() == null) {
            return true;
        }
        if (claims.issuedAt().isBefore(validAfter)) {
            reject(res, 401, "session expired, please sign in again");
            return false;
        }
        return true;
    }

    public static String extractBearerToken(HttpServletRequest req) {
        String header = req.getHeader(HttpHeaders.AUTHORIZATION);
        if (header == null) {
            return "";
        }
        String[] parts = header.split(" ", 2);
        if (parts.length == 2 && "Bearer".equals(parts[0])) {
            return parts[1];
        }
        return "";
    }

    static void reject(HttpServletResponse res, int status, String error) throws IOException {
        res.setStatus(status);
        res.setContentType("application/json; charset=utf-8");
        res.getWriter().write(JSON.writeValueAsString(java.util.Map.of("error", error)));
    }

    /** The authenticated user id, or null when the interceptor did not run (a programming error at call sites). */
    public static UUID requireUserId(HttpServletRequest req) {
        Object value = req.getAttribute(ATTR_USER_ID);
        if (value instanceof UUID id) {
            return id;
        }
        throw new IllegalStateException("no authenticated user on request");
    }
}
