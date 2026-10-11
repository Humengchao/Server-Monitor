package com.servermonitor.api;

import java.util.UUID;

import com.servermonitor.security.AuthInterceptor;

import jakarta.servlet.http.HttpServletRequest;

/** Small shared accessor for the authenticated user id. */
final class AuthSupport {

    private AuthSupport() {
    }

    static UUID userId(HttpServletRequest req) {
        return AuthInterceptor.requireUserId(req);
    }
}
