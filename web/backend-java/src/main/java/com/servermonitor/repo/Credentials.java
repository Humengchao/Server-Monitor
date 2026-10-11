package com.servermonitor.repo;

import java.time.Instant;
import java.util.List;
import java.util.Optional;
import java.util.UUID;

import org.springframework.jdbc.core.JdbcTemplate;
import org.springframework.stereotype.Repository;
import org.springframework.transaction.support.TransactionTemplate;

import com.servermonitor.crypto.FieldCipher;

@Repository
public class Credentials {

    private final JdbcTemplate db;
    private final TransactionTemplate tx;
    private final FieldCipher cipher;

    public Credentials(JdbcTemplate db, TransactionTemplate tx, FieldCipher cipher) {
        this.db = db;
        this.tx = tx;
        this.cipher = cipher;
    }

    /** API-facing row (never carries the secrets themselves). */
    public record Credential(UUID id, UUID userId, String name, String sshUsername,
        boolean hasPassword, boolean hasKey, String credentialType, Instant createdAt) {
    }

    /** Full row including decrypted secrets — collector/internal use only. */
    public record FullCredential(UUID id, UUID userId, String name, String sshUsername,
        String sshPassword, String sshKey, boolean hasPassword, boolean hasKey,
        String credentialType, Instant createdAt) {
    }

    public Optional<FullCredential> getFullCredential(UUID id, UUID userId) {
        List<FullCredential> list = db.query("""
            SELECT id, user_id, name, ssh_username, COALESCE(ssh_password, ''), COALESCE(ssh_key, ''),
             COALESCE(credential_type, 'linux'), created_at
             FROM credentials WHERE id=? AND user_id=?
            """, (rs, n) -> new FullCredential(UUID.fromString(rs.getString(1)), UUID.fromString(rs.getString(2)),
            rs.getString(3), rs.getString(4), cipher.decrypt(rs.getString(5)), cipher.decrypt(rs.getString(6)),
            !rs.getString(5).isEmpty(), !rs.getString(6).isEmpty(), rs.getString(7),
            rs.getTimestamp(8).toInstant()), id, userId);
        return list.isEmpty() ? Optional.empty() : Optional.of(list.get(0));
    }

    public Credential create(UUID userId, String name, String sshUsername, String sshPassword,
        String sshKey, String credentialType) {
        UUID id = UUID.randomUUID();
        String encPassword = cipher.encrypt(sshPassword == null ? "" : sshPassword);
        String encKey = cipher.encrypt(sshKey == null ? "" : sshKey);
        Instant createdAt = db.queryForObject("""
            INSERT INTO credentials (id, user_id, name, ssh_username, ssh_password, ssh_key, credential_type)
             VALUES (?,?,?,?,?,?,?) RETURNING created_at
            """, (rs, n) -> rs.getTimestamp(1).toInstant(),
            id, userId, name, sshUsername, encPassword, encKey, credentialType);
        return new Credential(id, userId, name, sshUsername, !encPassword.isEmpty(), !encKey.isEmpty(),
            credentialType, createdAt);
    }

    public List<Credential> getCredentialsByUserId(UUID userId) {
        return db.query("""
            SELECT id, user_id, name, ssh_username,
                    COALESCE(ssh_password, '') <> '', COALESCE(ssh_key, '') <> '',
                    COALESCE(credential_type, 'linux'), created_at
             FROM credentials WHERE user_id=? ORDER BY created_at DESC
            """, (rs, n) -> new Credential(UUID.fromString(rs.getString(1)), UUID.fromString(rs.getString(2)),
            rs.getString(3), rs.getString(4), rs.getBoolean(5), rs.getBoolean(6), rs.getString(7),
            rs.getTimestamp(8).toInstant()), userId);
    }

    /** Decrypted secrets for linking a server to a credential. */
    public record CredentialSecrets(String sshUsername, String sshPassword, String sshKey,
        String credentialType) {
    }

    public Optional<CredentialSecrets> getCredentialSecrets(UUID id, UUID userId) {
        List<CredentialSecrets> list = db.query("""
            SELECT ssh_username, COALESCE(ssh_password, ''), COALESCE(ssh_key, ''), COALESCE(credential_type, 'linux')
             FROM credentials WHERE id=? AND user_id=?
            """, (rs, n) -> new CredentialSecrets(rs.getString(1), cipher.decrypt(rs.getString(2)),
            cipher.decrypt(rs.getString(3)), rs.getString(4)), id, userId);
        return list.isEmpty() ? Optional.empty() : Optional.of(list.get(0));
    }

    public Optional<Credential> getCredentialSummary(UUID id, UUID userId) {
        List<Credential> list = db.query("""
            SELECT id, user_id, name, ssh_username,
                    COALESCE(ssh_password, '') <> '', COALESCE(ssh_key, '') <> '',
                    COALESCE(credential_type, 'linux'), created_at
             FROM credentials WHERE id=? AND user_id=?
            """, (rs, n) -> new Credential(UUID.fromString(rs.getString(1)), UUID.fromString(rs.getString(2)),
            rs.getString(3), rs.getString(4), rs.getBoolean(5), rs.getBoolean(6), rs.getString(7),
            rs.getTimestamp(8).toInstant()), id, userId);
        return list.isEmpty() ? Optional.empty() : Optional.of(list.get(0));
    }

    /** Full update; secrets change only when supplied. Returns false for a missing or foreign row. */
    public boolean updateCredential(UUID id, UUID userId, String name, String sshUsername,
        String sshPassword, String sshKey, String credentialType) {
        Boolean ok = tx.execute(status -> {
            int rows = db.update(
                "UPDATE credentials SET name=?, ssh_username=?, credential_type=? WHERE id=? AND user_id=?",
                name, sshUsername, credentialType, id, userId);
            if (rows == 0) {
                return false;
            }
            if (sshPassword != null && !sshPassword.isEmpty()) {
                db.update("UPDATE credentials SET ssh_password=? WHERE id=? AND user_id=?",
                    cipher.encrypt(sshPassword), id, userId);
            }
            if ("windows".equals(credentialType)) {
                db.update("UPDATE credentials SET ssh_key='' WHERE id=? AND user_id=?", id, userId);
            } else if (sshKey != null && !sshKey.isEmpty()) {
                db.update("UPDATE credentials SET ssh_key=? WHERE id=? AND user_id=?",
                    cipher.encrypt(sshKey), id, userId);
            }
            return true;
        });
        return Boolean.TRUE.equals(ok);
    }

    public void deleteCredential(UUID id, UUID userId) {
        db.update("DELETE FROM credentials WHERE id=? AND user_id=?", id, userId);
    }
}
