package com.servermonitor.repo;

import java.time.Instant;
import java.util.List;
import java.util.Optional;
import java.util.UUID;

import org.springframework.dao.EmptyResultDataAccessException;
import org.springframework.jdbc.core.JdbcTemplate;
import org.springframework.stereotype.Repository;

@Repository
public class LoginHistory {

    private final JdbcTemplate db;

    public LoginHistory(JdbcTemplate db) {
        this.db = db;
    }

    public record LoginRecord(long id, UUID userId, String ip, String userAgent, boolean success,
        Instant loggedAt) {
    }

    public void insertLoginRecord(UUID userId, String ip, String userAgent, boolean success) {
        db.update("INSERT INTO login_history (user_id, ip, user_agent, success) VALUES (?, ?, ?, ?)",
            userId, ip, userAgent, success);
    }

    public Optional<LoginRecord> getLastLogin(UUID userId) {
        try {
            return Optional.ofNullable(db.queryForObject(
                "SELECT id, user_id, ip, user_agent, success, logged_at FROM login_history"
                    + " WHERE user_id=? AND success=TRUE ORDER BY logged_at DESC LIMIT 1",
                (rs, n) -> map(rs.getLong("id"), UUID.fromString(rs.getString("user_id")), rs, n),
                userId));
        } catch (EmptyResultDataAccessException e) {
            return Optional.empty();
        }
    }

    public List<LoginRecord> getLoginHistory(UUID userId, int limit, int offset) {
        return db.query(
            "SELECT id, user_id, ip, user_agent, success, logged_at FROM login_history"
                + " WHERE user_id=? ORDER BY logged_at DESC LIMIT ? OFFSET ?",
            (rs, n) -> map(rs.getLong("id"), UUID.fromString(rs.getString("user_id")), rs, n),
            userId, limit, offset);
    }

    public int countLoginHistory(UUID userId) {
        Integer count = db.queryForObject("SELECT COUNT(*) FROM login_history WHERE user_id=?",
            Integer.class, userId);
        return count == null ? 0 : count;
    }

    private LoginRecord map(long id, UUID userId, java.sql.ResultSet rs, int rowNum) throws java.sql.SQLException {
        return new LoginRecord(id, userId, rs.getString("ip"), rs.getString("user_agent"),
            rs.getBoolean("success"), rs.getTimestamp("logged_at").toInstant());
    }
}
