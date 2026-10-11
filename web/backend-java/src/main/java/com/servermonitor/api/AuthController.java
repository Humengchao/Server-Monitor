package com.servermonitor.api;

import java.time.Instant;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.UUID;

import org.springframework.http.HttpStatus;
import org.springframework.http.ResponseEntity;
import org.springframework.security.crypto.bcrypt.BCrypt;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RequestParam;
import org.springframework.web.bind.annotation.RestController;

import com.servermonitor.config.MonitorConfig;
import com.servermonitor.repo.LoginHistory;
import com.servermonitor.repo.Users;
import com.servermonitor.security.AuthInterceptor;
import com.servermonitor.security.ClientIps;
import com.servermonitor.security.JwtService;

import jakarta.servlet.http.HttpServletRequest;

/** /api/auth/* — register, login, session info, login history, password change. */
@RestController
@RequestMapping("/api/auth")
public class AuthController {

    private static final int MIN_PASSWORD_LENGTH = 6;
    /** bcrypt's hard input ceiling; longer inputs fail with a clear 400, not a 500. */
    private static final int BCRYPT_MAX_PASSWORD_BYTES = 72;

    private final MonitorConfig cfg;
    private final Users users;
    private final LoginHistory loginHistory;
    private final JwtService jwt;

    public AuthController(MonitorConfig cfg, Users users, LoginHistory loginHistory, JwtService jwt) {
        this.cfg = cfg;
        this.users = users;
        this.loginHistory = loginHistory;
        this.jwt = jwt;
    }

    public record CredentialBody(String username, String password) {
    }

    public record ChangePasswordBody(String current_password, String new_password) {
    }

    @PostMapping("/register")
    public ResponseEntity<Map<String, Object>> register(@RequestBody(required = false) CredentialBody body) {
        if (!cfg.isAllowRegistration()) {
            throw ApiException.of(HttpStatus.FORBIDDEN, "registration is disabled on this server");
        }
        if (body == null || body.username() == null || body.password() == null
            || body.username().length() < 3 || body.username().length() > 64
            || body.password().length() < MIN_PASSWORD_LENGTH) {
            throw ApiException.badRequest("username must be 3-64 characters and the password at least 6");
        }
        if (tooLong(body.password())) {
            throw ApiException.badRequest("the password must be at most " + BCRYPT_MAX_PASSWORD_BYTES + " bytes");
        }
        String hash = BCrypt.hashpw(body.password(), BCrypt.gensalt(12));
        try {
            users.createUser(body.username(), hash);
        } catch (Exception e) {
            if (String.valueOf(e.getMessage()).contains("duplicate key")) {
                throw ApiException.of(409, "username already exists");
            }
            throw ApiException.of(500, "failed to create user");
        }
        return ResponseEntity.status(HttpStatus.CREATED).body(Map.of("message", "user created"));
    }

    @PostMapping("/login")
    public Map<String, Object> login(@RequestBody(required = false) CredentialBody body,
        HttpServletRequest req) {
        if (body == null || body.username() == null || body.password() == null) {
            throw ApiException.badRequest("username and password are required");
        }
        String ip = ClientIps.clientIp(req);
        String ua = req.getHeader("User-Agent") == null ? "" : req.getHeader("User-Agent");
        var userOpt = users.getUserByUsername(body.username());
        if (userOpt.isEmpty()) {
            throw ApiException.of(401, "invalid credentials");
        }
        var user = userOpt.get();
        if (!BCrypt.checkpw(body.password(), user.passwordHash())) {
            loginHistory.insertLoginRecord(user.id(), ip, ua, false);
            throw ApiException.of(401, "invalid credentials");
        }
        // Read the last login BEFORE inserting the new record so it names the previous visit.
        var lastLogin = loginHistory.getLastLogin(user.id());
        loginHistory.insertLoginRecord(user.id(), ip, ua, true);
        String token = jwt.generateToken(user.id(), user.username(), Instant.now());
        Map<String, Object> resp = new LinkedHashMap<>();
        resp.put("token", token);
        resp.put("user", Map.of("id", user.id(), "username", user.username()));
        lastLogin.ifPresent(record -> resp.put("last_login", Map.of(
            "ip", record.ip() == null ? "" : record.ip(),
            "logged_at", Jsons.timestamp(record.loggedAt()))));
        return resp;
    }

    @GetMapping("/me")
    public Map<String, Object> me(HttpServletRequest req) {
        UUID userId = AuthInterceptor.requireUserId(req);
        var user = users.getUserById(userId).orElseThrow(() -> ApiException.notFound("user not found"));
        Map<String, Object> out = new LinkedHashMap<>();
        out.put("id", user.id());
        out.put("username", user.username());
        out.put("created_at", Jsons.timestamp(user.createdAt()));
        return out;
    }

    @GetMapping("/login-history")
    public Map<String, Object> loginHistory(HttpServletRequest req,
        @RequestParam(defaultValue = "20") int limit, @RequestParam(defaultValue = "0") int offset) {
        UUID userId = AuthInterceptor.requireUserId(req);
        if (limit <= 0 || limit > 100) {
            limit = 20;
        }
        if (offset < 0) {
            offset = 0;
        }
        var records = loginHistory.getLoginHistory(userId, limit, offset);
        int total = loginHistory.countLoginHistory(userId);
        List<Map<String, Object>> out = records.stream().map(Jsons::loginRecord).toList();
        return Map.of("records", out, "total", total);
    }

    /**
     * Rotates the caller's password and signs every other session out: the
     * revocation cutoff moves to now, and the caller gets a replacement token
     * minted exactly at that cutoff so this session survives.
     */
    @PostMapping("/password")
    public Map<String, Object> changePassword(@RequestBody(required = false) ChangePasswordBody body,
        HttpServletRequest req) {
        UUID userId = AuthInterceptor.requireUserId(req);
        if (body == null || body.current_password() == null || body.new_password() == null) {
            throw ApiException.badRequest("current_password and new_password are required");
        }
        if (body.new_password().length() < MIN_PASSWORD_LENGTH) {
            throw ApiException.badRequest("the new password must be at least " + MIN_PASSWORD_LENGTH + " characters");
        }
        if (tooLong(body.new_password())) {
            throw ApiException.badRequest("the new password must be at most " + BCRYPT_MAX_PASSWORD_BYTES + " bytes");
        }
        if (body.new_password().equals(body.current_password())) {
            throw ApiException.badRequest("the new password must differ from the current one");
        }
        var user = users.getUserById(userId).orElseThrow(() -> ApiException.notFound("user not found"));
        if (!BCrypt.checkpw(body.current_password(), user.passwordHash())) {
            // 403, not 401: the session is valid, only the supplied password is
            // wrong. A 401 would sign the user out for a single typo.
            throw ApiException.of(403, "current password is incorrect");
        }
        String hash = BCrypt.hashpw(body.new_password(), BCrypt.gensalt(12));
        Instant validAfter = users.updateUserPassword(userId, hash);
        String token = jwt.generateToken(user.id(), user.username(), validAfter);
        return Map.of("message", "password updated", "token", token);
    }

    private static boolean tooLong(String password) {
        return password.getBytes(java.nio.charset.StandardCharsets.UTF_8).length > BCRYPT_MAX_PASSWORD_BYTES;
    }
}
