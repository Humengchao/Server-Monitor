package com.servermonitor.api;

import java.util.UUID;

/** Parses path ids into UUIDs with the same 400-on-garbage behaviour as the Go handlers' uuid.Parse. */
public final class UuidParser {

    private UuidParser() {
    }

    public static UUID parse(String raw) {
        try {
            return UUID.fromString(raw);
        } catch (IllegalArgumentException | NullPointerException e) {
            throw ApiException.badRequest("invalid id");
        }
    }
}
