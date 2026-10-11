package com.servermonitor.config;

import java.util.ArrayList;
import java.util.List;

import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.core.env.Environment;
import org.springframework.stereotype.Component;

/**
 * Environment-based configuration, mirroring web/backend internal/config.
 *
 * DATABASE_URL can be given in any of the three forms the Go backend accepts and
 * is normalized to a JDBC URL by {@link #jdbcUrl()}:
 *   "jdbc:postgresql://host:5432/db"                       — passed through
 *   "postgres://user:pass@host:5432/db?sslmode=disable"    — lib/pq URI form
 *   "host=h port=5432 user=u password=p dbname=d ..."      — lib/pq keyword form.
 */
@Component
public class MonitorConfig {

    private final String databaseUrl;
    private final String jwtSecret;
    private final String encryptionKey;
    private final String serverPort;
    private final String tlsCertFile;
    private final String tlsKeyFile;
    private final String corsOrigin;
    private final int pollIntervalSeconds;
    private final int alertIntervalSeconds;
    private final List<String> trustedProxies;
    private final boolean allowPrivateWebhooks;
    private final boolean allowRegistration;
    private final int databasePoolMax;

    @Autowired
    public MonitorConfig(Environment env) {
        this.databaseUrl = required(env, "DATABASE_URL");
        this.jwtSecret = required(env, "JWT_SECRET");
        if (jwtSecret.getBytes(java.nio.charset.StandardCharsets.UTF_8).length < 32) {
            org.slf4j.LoggerFactory.getLogger(MonitorConfig.class).warn(
                "JWT_SECRET is only {} bytes long; tokens signed with a key this small are weak. "
                    + "Use at least 32 random bytes (e.g. `openssl rand -base64 48`)",
                jwtSecret.getBytes(java.nio.charset.StandardCharsets.UTF_8).length);
        }
        this.encryptionKey = required(env, "ENCRYPTION_KEY");
        if (encryptionKey.getBytes(java.nio.charset.StandardCharsets.UTF_8).length != 32) {
            throw new IllegalStateException("ENCRYPTION_KEY must be exactly 32 bytes");
        }
        this.serverPort = value(env, "SERVER_PORT", "8080");
        this.tlsCertFile = value(env, "TLS_CERT_FILE", "");
        this.tlsKeyFile = value(env, "TLS_KEY_FILE", "");
        this.corsOrigin = value(env, "CORS_ORIGIN", "http://localhost:5173");
        this.pollIntervalSeconds = interval(env, "POLL_INTERVAL", 3);
        this.alertIntervalSeconds = interval(env, "ALERT_INTERVAL", 30);
        this.trustedProxies = trustedProxies(env);
        this.allowPrivateWebhooks = bool(env, "ALLOW_PRIVATE_WEBHOOKS", false);
        this.allowRegistration = bool(env, "ALLOW_REGISTRATION", true);
        this.databasePoolMax = interval(env, "DATABASE_POOL_MAX", 25);
    }

    private static String required(Environment env, String key) {
        String v = lookup(env, key);
        if (v == null || v.isEmpty()) {
            throw new IllegalStateException(key + " is required");
        }
        return v;
    }

    private static String lookup(Environment env, String key) {
        String v = System.getenv(key);
        if (v == null || v.isEmpty()) {
            v = env.getProperty(key);
        }
        return v;
    }

    private static String value(Environment env, String key, String fallback) {
        String v = lookup(env, key);
        return v == null || v.isEmpty() ? fallback : v;
    }

    private static int interval(Environment env, String key, int fallback) {
        String v = lookup(env, key);
        if (v == null || v.isEmpty()) {
            return fallback;
        }
        int n;
        try {
            n = Integer.parseInt(v.trim());
        } catch (NumberFormatException e) {
            throw new IllegalStateException(key + " must be a positive number of seconds, got " + v);
        }
        if (n < 1) {
            throw new IllegalStateException(key + " must be a positive number of seconds, got " + v);
        }
        return n;
    }

    private static boolean bool(Environment env, String key, boolean fallback) {
        String v = lookup(env, key);
        if (v == null || v.trim().isEmpty()) {
            return fallback;
        }
        return switch (v.trim().toLowerCase()) {
            case "true", "1", "t" -> true;
            case "false", "0", "f" -> false;
            default -> throw new IllegalStateException(key + " must be a boolean (true/false/1/0), got " + v);
        };
    }

    private static List<String> trustedProxies(Environment env) {
        String v = lookup(env, "TRUSTED_PROXIES");
        if (v == null || v.trim().isEmpty()) {
            return List.of("127.0.0.1/32", "::1/128", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16");
        }
        if (v.trim().equalsIgnoreCase("none")) {
            return List.of();
        }
        List<String> out = new ArrayList<>();
        for (String entry : v.split(",")) {
            entry = entry.trim();
            if (entry.isEmpty()) {
                continue;
            }
            if (!isIpOrCidr(entry)) {
                throw new IllegalStateException("TRUSTED_PROXIES entry \"" + entry + "\" is not an IP or CIDR");
            }
            out.add(entry);
        }
        return out;
    }

    private static boolean isIpOrCidr(String value) {
        String host = value;
        if (value.contains("/")) {
            String[] parts = value.split("/", 2);
            host = parts[0];
            try {
                int bits = Integer.parseInt(parts[1]);
                if (bits < 0 || bits > 128) {
                    return false;
                }
            } catch (NumberFormatException e) {
                return false;
            }
        }
        return host.matches("[0-9a-fA-F:.]+") && !host.isBlank();
    }

    /**
     * Normalizes DATABASE_URL to a JDBC URL. The keyword/value form
     * ("host=... user=...") is how .env.example and most deployments carry it.
     */
    public String jdbcUrl() {
        String url = databaseUrl.trim();
        if (url.startsWith("jdbc:")) {
            return url;
        }
        if (url.startsWith("postgres://") || url.startsWith("postgresql://")) {
            return "jdbc:postgresql://" + url.substring(url.indexOf("://") + 3);
        }
        if (url.contains("=") && !url.contains("://")) {
            String host = "localhost", port = "5432", db = "";
            for (String pair : url.split("\\s+")) {
                int idx = pair.indexOf('=');
                if (idx <= 0) {
                    continue;
                }
                String k = pair.substring(0, idx).toLowerCase();
                String v = pair.substring(idx + 1);
                switch (k) {
                    case "host" -> host = v;
                    case "port" -> port = v;
                    case "dbname" -> db = v;
                    default -> { }
                }
            }
            if (db.isEmpty()) {
                throw new IllegalStateException("DATABASE_URL keyword form must include dbname=");
            }
            return "jdbc:postgresql://" + host + ":" + port + "/" + db;
        }
        return "jdbc:postgresql://" + url;
    }

    /** Username for the JDBC connection when DATABASE_URL carries userinfo or a user= keyword. */
    public String jdbcUser() {
        return component("user");
    }

    public String jdbcPassword() {
        return component("password");
    }

    private String component(String key) {
        String url = databaseUrl.trim();
        if (url.startsWith("jdbc:") || url.startsWith("postgres://") || url.startsWith("postgresql://")) {
            int scheme = url.indexOf("://") + 3;
            String rest = url.substring(scheme);
            int at = rest.lastIndexOf('@');
            if (at > 0) {
                String userinfo = rest.substring(0, at);
                int colon = userinfo.indexOf(':');
                String user = colon >= 0 ? userinfo.substring(0, colon) : userinfo;
                String pass = colon >= 0 ? userinfo.substring(colon + 1) : "";
                return decode("user".equals(key) ? user : pass);
            }
            return "";
        }
        if (url.contains("=")) {
            for (String pair : url.split("\\s+")) {
                int idx = pair.indexOf('=');
                if (idx <= 0) {
                    continue;
                }
                if (pair.substring(0, idx).equalsIgnoreCase(key)) {
                    return pair.substring(idx + 1);
                }
            }
        }
        return "";
    }

    private static String decode(String v) {
        try {
            return java.net.URLDecoder.decode(v, java.nio.charset.StandardCharsets.UTF_8);
        } catch (Exception e) {
            return v;
        }
    }

    /** Strips userinfo from the JDBC URL produced by jdbcUrl(). */
    public String jdbcUrlSanitized() {
        String url = jdbcUrl();
        int scheme = url.indexOf("://") + 3;
        String rest = url.substring(scheme);
        int at = rest.lastIndexOf('@');
        if (at > 0) {
            return url.substring(0, scheme) + rest.substring(at + 1);
        }
        return url;
    }

    public String getDatabaseUrl() { return databaseUrl; }
    public String getJwtSecret() { return jwtSecret; }
    public String getEncryptionKey() { return encryptionKey; }
    public String getServerPort() { return serverPort; }
    public String getTlsCertFile() { return tlsCertFile; }
    public String getTlsKeyFile() { return tlsKeyFile; }
    public String getCorsOrigin() { return corsOrigin; }
    public int getPollIntervalSeconds() { return pollIntervalSeconds; }
    public int getAlertIntervalSeconds() { return alertIntervalSeconds; }
    public List<String> getTrustedProxies() { return trustedProxies; }
    public boolean isAllowPrivateWebhooks() { return allowPrivateWebhooks; }
    public boolean isAllowRegistration() { return allowRegistration; }
    public int getDatabasePoolMax() { return databasePoolMax; }
}
