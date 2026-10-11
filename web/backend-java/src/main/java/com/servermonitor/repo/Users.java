package com.servermonitor.repo;

import java.sql.Timestamp;
import java.time.Instant;
import java.util.Optional;
import java.util.UUID;

import org.springframework.dao.EmptyResultDataAccessException;
import org.springframework.jdbc.core.JdbcTemplate;
import org.springframework.stereotype.Repository;

/** users + login_history tables. */
@Repository
public class Users {

    private final JdbcTemplate db;

    public Users(JdbcTemplate db) {
        this.db = db;
    }

    public record User(UUID id, String username, String passwordHash, Instant createdAt) {
    }

    public User createUser(String username, String passwordHash) {
        UUID id = UUID.randomUUID();
        Instant createdAt = db.queryForObject(
            "INSERT INTO users (id, username, password_hash) VALUES (?, ?, ?) RETURNING created_at",
            (rs, n) -> rs.getTimestamp("created_at").toInstant(),
            id, username, passwordHash);
        return new User(id, username, passwordHash, createdAt);
    }

    public Optional<User> getUserByUsername(String username) {
        try {
            return Optional.ofNullable(db.queryForObject(
                "SELECT id, username, password_hash, created_at FROM users WHERE username=?",
                (rs, n) -> new User(UUID.fromString(rs.getString("id")), rs.getString("username"),
                    rs.getString("password_hash"), rs.getTimestamp("created_at").toInstant()),
                username));
        } catch (EmptyResultDataAccessException e) {
            return Optional.empty();
        }
    }

    public Optional<User> getUserById(UUID id) {
        try {
            return Optional.ofNullable(db.queryForObject(
                "SELECT id, username, password_hash, created_at FROM users WHERE id=?",
                (rs, n) -> new User(UUID.fromString(rs.getString("id")), rs.getString("username"),
                    rs.getString("password_hash"), rs.getTimestamp("created_at").toInstant()),
                id));
        } catch (EmptyResultDataAccessException e) {
            return Optional.empty();
        }
    }

    /** Revocation cutoff for JWTs; tokens issued before this instant are rejected. */
    public Instant getTokensValidAfter(UUID id) {
        Timestamp ts = db.queryForObject("SELECT tokens_valid_after FROM users WHERE id=?",
            Timestamp.class, id);
        return ts == null ? null : ts.toInstant();
    }

    /**
     * Stores the new hash and moves the token cutoff to now (truncated to whole
     * seconds, matching JWT iat resolution), returning the exact cutoff for the
     * caller's replacement token.
     */
    public Instant updateUserPassword(UUID id, String passwordHash) {
        Timestamp ts = db.queryForObject(
            "UPDATE users SET password_hash=?, tokens_valid_after=date_trunc('second', NOW())"
                + " WHERE id=? RETURNING tokens_valid_after",
            Timestamp.class, passwordHash, id);
        if (ts == null) {
            throw new EmptyResultDataAccessException(1);
        }
        return ts.toInstant();
    }
}
