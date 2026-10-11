package com.servermonitor.security;

import jakarta.servlet.http.HttpServletRequest;

/**
 * Resolves the caller's IP, honoring X-Forwarded-For/X-Real-IP (set
 * server.forward-headers-strategy=framework so the proxy-facing attributes are
 * already rewritten when the request gets here). IPv6 loopback normalizes to
 * IPv4, matching the Go login-history convention.
 */
public final class ClientIps {

    private ClientIps() {
    }

    public static String clientIp(HttpServletRequest req) {
        String ip = req.getRemoteAddr();
        if (ip != null && ip.startsWith("[") && ip.endsWith("]")) {
            ip = ip.substring(1, ip.length() - 1);
        }
        if ("::1".equals(ip) || "0:0:0:0:0:0:0:1".equals(ip)) {
            return "127.0.0.1";
        }
        return ip == null ? "" : ip;
    }
}
