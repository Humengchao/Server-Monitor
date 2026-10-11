package com.servermonitor.repo;

import java.time.Instant;
import java.util.List;
import java.util.UUID;

import org.springframework.jdbc.core.JdbcTemplate;
import org.springframework.stereotype.Repository;

/** file_operations audit trail. */
@Repository
public class FileOperations {

    private final JdbcTemplate db;

    public FileOperations(JdbcTemplate db) {
        this.db = db;
    }

    public void insert(UUID id, UUID userId, UUID serverId, String containerId, String action, String path) {
        db.update("INSERT INTO file_operations(id,user_id,server_id,container_id,action,path,outcome) VALUES(?,?,?,?,?,?,'pending')",
            id, userId, serverId, containerId, action, path);
    }

    public void complete(UUID id, String action, String target, String backupPath, String outcome, String errorCode) {
        db.update("UPDATE file_operations SET action=?,target=?,backup_path=?,outcome=?,error_code=? WHERE id=?",
            action, target, backupPath, outcome, errorCode, id);
    }

    public record FileOperationEntry(UUID id, String action, String path, String target,
        String backupPath, String outcome, String errorCode, Instant createdAt) {
    }

    public List<FileOperationEntry> history(UUID userId, UUID serverId, String containerId) {
        return db.query("""
            SELECT id,action,path,target,backup_path,outcome,error_code,created_at
             FROM file_operations WHERE user_id=? AND server_id=? AND container_id=?
             ORDER BY created_at DESC LIMIT 100
            """, (rs, n) -> new FileOperationEntry(UUID.fromString(rs.getString(1)), rs.getString(2),
            rs.getString(3), rs.getString(4), rs.getString(5), rs.getString(6), rs.getString(7),
            rs.getTimestamp(8).toInstant()), userId, serverId, containerId);
    }
}
