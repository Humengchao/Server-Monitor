package com.servermonitor.api;

import java.io.IOException;

import org.springframework.boot.web.servlet.FilterRegistrationBean;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.http.HttpHeaders;
import org.springframework.web.filter.OncePerRequestFilter;

import jakarta.servlet.FilterChain;
import jakarta.servlet.ServletException;
import jakarta.servlet.http.HttpServletRequest;
import jakarta.servlet.http.HttpServletResponse;

/** API responses must never be cached (mirrors the Gin no-store middleware). */
@Configuration
public class CacheControlFilter extends OncePerRequestFilter {

    @Bean
    public FilterRegistrationBean<CacheControlFilter> cacheControlRegistration() {
        FilterRegistrationBean<CacheControlFilter> registration = new FilterRegistrationBean<>(this);
        registration.setOrder(1);
        registration.addUrlPatterns("/api/*");
        return registration;
    }

    @Override
    protected void doFilterInternal(HttpServletRequest req, HttpServletResponse res, FilterChain chain)
        throws IOException, ServletException {
        res.setHeader(HttpHeaders.CACHE_CONTROL, "no-store, no-cache, must-revalidate");
        res.setHeader(HttpHeaders.PRAGMA, "no-cache");
        chain.doFilter(req, res);
    }
}
