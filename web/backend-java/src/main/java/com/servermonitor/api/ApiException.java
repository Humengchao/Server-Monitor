package com.servermonitor.api;

import org.springframework.http.HttpStatus;

/** Carrier for handler-level error responses; the advice renders {"error": message} plus an optional code. */
public class ApiException extends RuntimeException {

    private final int status;
    private final String code;

    private ApiException(int status, String code, String message) {
        super(message);
        this.status = status;
        this.code = code;
    }

    public int getStatus() {
        return status;
    }

    public String getCode() {
        return code;
    }

    public static ApiException of(HttpStatus status, String message) {
        return new ApiException(status.value(), null, message);
    }

    public static ApiException of(int status, String message) {
        return new ApiException(status, null, message);
    }

    public static ApiException coded(int status, String code, String message) {
        return new ApiException(status, code, message);
    }

    public static ApiException badRequest(String message) {
        return new ApiException(400, null, message);
    }

    public static ApiException notFound(String message) {
        return new ApiException(404, null, message);
    }

    public static ApiException conflict(String message) {
        return new ApiException(409, null, message);
    }

    public static ApiException badGateway(String message) {
        return new ApiException(502, null, message);
    }
}
