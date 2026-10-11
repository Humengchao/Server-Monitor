package com.servermonitor.security;

import java.io.IOException;
import java.time.Duration;

import org.springframework.lang.NonNull;
import org.springframework.stereotype.Component;
import org.springframework.web.servlet.HandlerInterceptor;

import jakarta.servlet.http.HttpServletRequest;
import jakarta.servlet.http.HttpServletResponse;

/** Holds the panel's per-route token buckets, mirroring the Go router's middleware.RateLimit call sites. */
@Component
public class RateLimitInterceptor {

    /** A HandlerInterceptor bound to one limiter's settings. */
    public static final class Bucket implements HandlerInterceptor {
        private final RateLimiter limiter;

        private Bucket(int requests, Duration per) {
            this.limiter = new RateLimiter(requests, per);
        }

        @Override
        public boolean preHandle(@NonNull HttpServletRequest req, @NonNull HttpServletResponse res,
            @NonNull Object handler) throws IOException {
            if (limiter.tryAcquire(ClientIps.clientIp(req))) {
                return true;
            }
            AuthInterceptor.reject(res, 429, "rate limit exceeded");
            return false;
        }
    }

    public Bucket authPublic() { return new Bucket(5, Duration.ofMinutes(1)); }

    public Bucket passwordChange() { return new Bucket(10, Duration.ofMinutes(1)); }

    public Bucket bulkExec() { return new Bucket(10, Duration.ofMinutes(1)); }

    public Bucket serviceControl() { return new Bucket(30, Duration.ofMinutes(1)); }

    public Bucket pollRetry() { return new Bucket(20, Duration.ofMinutes(1)); }

    public Bucket fileWrites() { return new Bucket(60, Duration.ofMinutes(1)); }

    public Bucket webhookTest() { return new Bucket(6, Duration.ofMinutes(1)); }

    public Bucket publicStatus() { return new Bucket(60, Duration.ofMinutes(1)); }
}
