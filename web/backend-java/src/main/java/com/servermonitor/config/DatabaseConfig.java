package com.servermonitor.config;

import javax.sql.DataSource;

import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.core.io.ClassPathResource;
import org.springframework.jdbc.core.JdbcTemplate;
import org.springframework.jdbc.datasource.init.ScriptUtils;

import com.zaxxer.hikari.HikariConfig;
import com.zaxxer.hikari.HikariDataSource;

/**
 * Builds the DataSource from MonitorConfig (which normalizes the Go/lib-pq
 * DATABASE_URL forms), then applies schema migrations from migrations.sql —
 * the same statements the Go backend runs verbatim.
 */
@Configuration
public class DatabaseConfig {

    @Bean
    public DataSource dataSource(MonitorConfig cfg) {
        HikariConfig hikari = new HikariConfig();
        hikari.setJdbcUrl(cfg.jdbcUrlSanitized());
        if (!cfg.jdbcUser().isEmpty()) {
            hikari.setUsername(cfg.jdbcUser());
        }
        if (!cfg.jdbcPassword().isEmpty()) {
            hikari.setPassword(cfg.jdbcPassword());
        }
        hikari.setMaximumPoolSize(cfg.getDatabasePoolMax());
        hikari.setMinimumIdle(Math.min(10, cfg.getDatabasePoolMax()));
        hikari.setMaxLifetime(300_000);
        hikari.setConnectionTimeout(10_000);
        return new HikariDataSource(hikari);
    }

    @Bean
    public JdbcTemplate jdbcTemplate(DataSource dataSource) {
        return new JdbcTemplate(dataSource);
    }

    @Bean
    public boolean migrationsApplied(DataSource dataSource) {
        var resource = new ClassPathResource("migrations.sql");
        try (var connection = dataSource.getConnection()) {
            ScriptUtils.executeSqlScript(connection, resource);
        } catch (Exception e) {
            throw new IllegalStateException("database migration failed: " + e.getMessage(), e);
        }
        return true;
    }
}
