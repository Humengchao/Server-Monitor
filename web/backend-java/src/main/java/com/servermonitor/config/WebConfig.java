package com.servermonitor.config;

import java.util.List;

import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.context.annotation.Configuration;
import org.springframework.lang.NonNull;
import org.springframework.web.servlet.config.annotation.InterceptorRegistry;
import org.springframework.web.servlet.config.annotation.WebMvcConfigurer;

import com.servermonitor.security.AuthInterceptor;
import com.servermonitor.security.RateLimitInterceptor;

/** HTTP wiring: auth enforcement and per-route rate limits. CORS lives in CorsOriginFilter. */
@Configuration
public class WebConfig implements WebMvcConfigurer {

    private final AuthInterceptor auth;
    private final RateLimitInterceptor rateLimits;

    @Autowired
    public WebConfig(AuthInterceptor auth, RateLimitInterceptor rateLimits) {
        this.auth = auth;
        this.rateLimits = rateLimits;
    }

    @Override
    public void addInterceptors(@NonNull InterceptorRegistry registry) {
        registry.addInterceptor(rateLimits.publicStatus())
            .addPathPatterns("/api/public/status");

        List<String> authed = List.of(
            "/api/auth/me", "/api/auth/login-history", "/api/auth/password",
            "/api/servers/**", "/api/tags/**", "/api/credentials/**", "/api/alerts/**",
            "/api/ws/**", "/api/ssh/**");
        registry.addInterceptor(auth).addPathPatterns(authed);

        registry.addInterceptor(rateLimits.authPublic())
            .addPathPatterns("/api/auth/register", "/api/auth/login");
        registry.addInterceptor(rateLimits.passwordChange())
            .addPathPatterns("/api/auth/password");
        registry.addInterceptor(rateLimits.bulkExec())
            .addPathPatterns("/api/servers/bulk/exec");
        registry.addInterceptor(rateLimits.serviceControl())
            .addPathPatterns("/api/servers/*/services/control");
        registry.addInterceptor(rateLimits.pollRetry())
            .addPathPatterns("/api/servers/*/poll");
        registry.addInterceptor(rateLimits.fileWrites())
            .addPathPatterns("/api/servers/*/files/text", "/api/servers/*/files/upload",
                "/api/servers/*/files/change");
        registry.addInterceptor(rateLimits.webhookTest())
            .addPathPatterns("/api/alerts/test");
    }
}
