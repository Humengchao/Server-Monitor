package com.servermonitor.api;

import java.util.Map;

import org.springframework.http.HttpStatus;
import org.springframework.http.ResponseEntity;
import org.springframework.jdbc.core.JdbcTemplate;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.RestController;

/** Liveness and readiness, mirroring /api/health and /api/ready. */
@RestController
public class HealthController {

    private final JdbcTemplate db;

    public HealthController(JdbcTemplate db) {
        this.db = db;
    }

    @GetMapping("/api/health")
    public Map<String, Object> health() {
        return Map.of("status", "ok");
    }

    @GetMapping("/api/ready")
    public ResponseEntity<Map<String, Object>> ready() {
        try {
            db.queryForObject("SELECT 1", Integer.class);
            return ResponseEntity.ok(Map.of("status", "ready"));
        } catch (Exception e) {
            return ResponseEntity.status(HttpStatus.SERVICE_UNAVAILABLE)
                .body(Map.of("status", "unavailable"));
        }
    }
}
