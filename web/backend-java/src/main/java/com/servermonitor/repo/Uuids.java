package com.servermonitor.repo;

import java.sql.Connection;
import java.sql.SQLException;
import java.util.List;
import java.util.UUID;

import org.springframework.jdbc.core.JdbcTemplate;
import org.springframework.jdbc.datasource.DataSourceUtils;

/**
 * Binds {@code List<UUID>} into "ANY(?)" parameters as a real Postgres uuid[]
 * via pgjdbc's createArrayOf. The array is built on the template's pooled
 * (transaction-aware) connection, so inside a TransactionTemplate callback it
 * sees — and is rolled back with — the surrounding transaction.
 */
final class Uuids {

    private Uuids() {
    }

    static java.sql.Array array(JdbcTemplate db, List<UUID> ids) {
        if (db.getDataSource() == null) {
            throw new IllegalStateException("no DataSource for uuid array binding");
        }
        Connection con = DataSourceUtils.getConnection(db.getDataSource());
        try {
            return con.createArrayOf("uuid", ids.toArray(new UUID[0]));
        } catch (SQLException e) {
            throw new IllegalStateException("bind uuid array: " + e.getMessage(), e);
        } finally {
            DataSourceUtils.releaseConnection(con, db.getDataSource());
        }
    }
}
