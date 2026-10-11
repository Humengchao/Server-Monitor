package com.servermonitor.config;

import java.io.IOException;

import org.springframework.boot.web.servlet.FilterRegistrationBean;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.web.filter.OncePerRequestFilter;

import jakarta.servlet.FilterChain;
import jakarta.servlet.ServletException;
import jakarta.servlet.http.HttpServletRequest;
import jakarta.servlet.http.HttpServletResponse;

/**
 * Mirrors the Gin CORS middleware exactly: reflect the Origin only when it
 * equals CORS_ORIGIN (or when CORS_ORIGIN is "*"), always emit the method and
 * header lists, and answer OPTIONS with a bare 204.
 */
@Configuration
public class CorsOriginFilter extends OncePerRequestFilter {

    private final MonitorConfig cfg;

    public CorsOriginFilter(MonitorConfig cfg) {
        this.cfg = cfg;
    }

    @Bean
    public FilterRegistrationBean<CorsOriginFilter> corsOriginRegistration() {
        FilterRegistrationBean<CorsOriginFilter> registration = new FilterRegistrationBean<>(this);
        registration.setOrder(0);
        return registration;
    }

    @Override
    protected void doFilterInternal(HttpServletRequest req, HttpServletResponse res, FilterChain chain)
        throws IOException, ServletException {
        String origin = req.getHeader("Origin");
        if (origin != null && (origin.equals(cfg.getCorsOrigin()) || "*".equals(cfg.getCorsOrigin()))) {
            res.setHeader("Access-Control-Allow-Origin", origin);
        }
        res.setHeader("Access-Control-Allow-Methods", "GET,POST,PUT,DELETE,OPTIONS");
        res.setHeader("Access-Control-Allow-Headers", "Authorization,Content-Type,Cache-Control,Pragma");
        if ("OPTIONS".equalsIgnoreCase(req.getMethod())) {
            res.setStatus(204);
            return;
        }
        chain.doFilter(req, res);
    }
}
