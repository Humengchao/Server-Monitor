package com.servermonitor.api;

import java.time.Duration;
import java.time.Instant;
import java.time.ZoneOffset;
import java.time.format.DateTimeFormatter;
import java.time.temporal.ChronoUnit;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.UUID;

import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RequestParam;
import org.springframework.web.bind.annotation.RestController;

import com.servermonitor.repo.Servers;
import com.servermonitor.repo.Uptime;
import com.servermonitor.service.UptimeService;

import jakarta.servlet.http.HttpServletRequest;

/** /api/servers/uptime + /api/servers/{id}/uptime — observed availability. */
@RestController
@RequestMapping("/api/servers")
public class UptimeController {

    private static final Duration FLEET_CACHE_TTL = Duration.ofSeconds(60);

    private final Servers servers;
    private final Uptime uptime;

    private record CacheEntry(Map<String, Object> payload, Instant expires) {
    }

    private final Map<UUID, CacheEntry> fleetCache = new HashMap<>();

    public UptimeController(Servers servers, Uptime uptime) {
        this.servers = servers;
        this.uptime = uptime;
    }

    private record WindowBounds(String key, int expected, boolean partial) {
    }

    /** Each window's clamped start and bucket expectation for a server of a given age. */
    private static List<WindowBounds> windowBounds(Instant now, Instant createdAt) {
        List<WindowBounds> results = new ArrayList<>();
        for (UptimeService.UptimeWindowSpec spec : UptimeService.UPTIME_WINDOWS) {
            Instant end = UptimeService.tierEnd(now, spec.bucketSeconds());
            var start = UptimeService.windowStart(end, spec.span(), createdAt);
            int expected = UptimeService.expectedBuckets(start.start(), end, spec.bucketSeconds());
            results.add(new WindowBounds(spec.key(), expected, start.partial()));
        }
        return results;
    }

    /** Observed availability per server for every reporting window (cached briefly per user). */
    @GetMapping("/uptime")
    public Map<String, Object> fleet(HttpServletRequest req) {
        UUID userId = AuthSupport.userId(req);
        synchronized (fleetCache) {
            CacheEntry entry = fleetCache.get(userId);
            if (entry != null && Instant.now().isBefore(entry.expires())) {
                return entry.payload();
            }
        }
        Instant now = Instant.now();
        Instant end1m = UptimeService.tierEnd(now, 60);
        Instant end15m = UptimeService.tierEnd(now, Uptime.DAILY_STRIP_BUCKET_SECONDS);
        var counts = uptime.getFleetUptimeCounts(userId, new Uptime.UptimeCensusRange(
            end1m.minus(Duration.ofHours(24)), end1m.minus(Duration.ofDays(7)), end1m,
            end15m.minus(Duration.ofDays(30)), end15m));

        List<Map<String, Object>> fleet = new ArrayList<>();
        for (Uptime.UptimeCounts item : counts) {
            var bounds = windowBounds(now, item.createdAt());
            Map<String, Integer> observed = Map.of(
                "24h", item.minute24h(), "7d", item.minute7d(), "30d", item.quarter30d());
            List<Map<String, Object>> windows = new ArrayList<>();
            for (WindowBounds b : bounds) {
                int obs = observed.getOrDefault(b.key(), 0);
                Map<String, Object> w = new LinkedHashMap<>();
                w.put("window", b.key());
                w.put("percent", UptimeService.availabilityPercent(obs, b.expected()));
                w.put("observed_buckets", obs);
                w.put("expected_buckets", b.expected());
                w.put("partial", b.partial());
                // NoData marks a window that expected nothing at all — a server
                // added minutes ago reads as 0%, which looks like a total outage.
                w.put("no_data", b.expected() == 0);
                windows.add(w);
            }
            fleet.add(Map.of("server_id", item.serverId(), "windows", windows));
        }
        Map<String, Object> payload = new LinkedHashMap<>();
        payload.put("servers", fleet);
        payload.put("basis", "observed");
        payload.put("basis_note",
            "Share of expected metric samples that were collected. Panel downtime counts against every server.");
        payload.put("generated_at", Jsons.timestamp(now));
        synchronized (fleetCache) {
            fleetCache.put(userId, new CacheEntry(payload, Instant.now().plus(FLEET_CACHE_TTL)));
        }
        return payload;
    }

    /** One server's availability plus a daily strip and the outage episodes behind it. */
    @GetMapping("/{id}/uptime")
    public Map<String, Object> detail(@PathVariable String id, HttpServletRequest req,
        @RequestParam(required = false) Integer days) {
        UUID userId = AuthSupport.userId(req);
        UUID serverId = UuidParser.parse(id);
        int daysNum = days == null ? 30 : days;
        if (daysNum < 1 || daysNum > 30) {
            daysNum = 30;
        }
        Instant createdAt = servers.getServerCreatedAt(serverId, userId);
        if (createdAt == null) {
            throw ApiException.notFound("server not found");
        }
        Instant now = Instant.now();
        // The strip reads the fifteen-minute tier, the outage list the one-minute tier.
        Instant stripEnd = UptimeService.tierEnd(now, Uptime.DAILY_STRIP_BUCKET_SECONDS);
        Instant outageEnd = UptimeService.tierEnd(now, 60);
        Duration span = Duration.ofDays(daysNum);
        var start = UptimeService.windowStart(stripEnd, span, createdAt);
        var outageStart = UptimeService.windowStart(outageEnd, span, createdAt);

        var daily = uptime.getDailyAvailability(serverId, start.start(), stripEnd);
        var gapRows = uptime.getMetricGaps(serverId, outageStart.start(), outageEnd,
            (int) UptimeService.UPTIME_OUTAGE_GAP.toSeconds(), UptimeService.UPTIME_MAX_OUTAGES);
        Instant lastSample = uptime.getLastMetricBucket(serverId, outageEnd);
        var outages = UptimeService.buildOutages(gapRows, lastSample, outageEnd);

        List<Map<String, Object>> outageViews = new ArrayList<>();
        for (UptimeService.Outage o : outages) {
            Map<String, Object> row = new LinkedHashMap<>();
            row.put("started_at", Jsons.timestamp(o.startedAt()));
            row.put("ended_at", Jsons.timestamp(o.endedAt()));
            row.put("seconds", o.seconds());
            row.put("ongoing", o.ongoing());
            outageViews.add(row);
        }
        Map<String, Object> out = new LinkedHashMap<>();
        out.put("server_id", serverId);
        out.put("since", Jsons.timestamp(start.start()));
        out.put("until", Jsons.timestamp(stripEnd));
        out.put("days", buildDailyStrip(daily, start.start(), stripEnd, createdAt));
        out.put("outages", outageViews);
        out.put("basis", "observed");
        out.put("generated_at", Jsons.timestamp(now));
        return out;
    }

    private record DailyStripEntry(String day, int expected, int observed) {
    }

    /** Fills every calendar day in the range, including days with no rows at all. */
    private static List<Map<String, Object>> buildDailyStrip(List<Uptime.DailyAvailability> daily,
        Instant start, Instant end, Instant createdAt) {
        Map<String, Integer> observedByDay = new HashMap<>();
        DateTimeFormatter dayFmt = DateTimeFormatter.ofPattern("yyyy-MM-dd").withZone(ZoneOffset.UTC);
        for (Uptime.DailyAvailability item : daily) {
            observedByDay.put(dayFmt.format(item.day()), item.observed());
        }
        List<Map<String, Object>> entries = new ArrayList<>(31);
        Instant cursor = start.atZone(ZoneOffset.UTC).truncatedTo(ChronoUnit.DAYS).toInstant();
        while (cursor.isBefore(end)) {
            Instant dayStart = cursor;
            Instant dayEnd = cursor.plus(Duration.ofDays(1));
            // Clip the first and last days to the window and to the server age,
            // so a partial day is not scored against a whole one.
            Instant effectiveStart = dayStart;
            if (start.isAfter(effectiveStart)) {
                effectiveStart = start;
            }
            if (createdAt.isAfter(effectiveStart)) {
                effectiveStart = createdAt;
            }
            Instant effectiveEnd = end.isBefore(dayEnd) ? end : dayEnd;
            String key = dayFmt.format(dayStart);
            int expected = UptimeService.expectedBuckets(effectiveStart, effectiveEnd,
                Uptime.DAILY_STRIP_BUCKET_SECONDS);
            int observed = observedByDay.getOrDefault(key, 0);
            Map<String, Object> row = new LinkedHashMap<>();
            row.put("day", key);
            row.put("percent", UptimeService.availabilityPercent(observed, expected));
            row.put("observed_buckets", observed);
            row.put("expected_buckets", expected);
            // NoData distinguishes "the server did not exist yet" from "down all day".
            row.put("no_data", expected == 0);
            entries.add(row);
            cursor = dayEnd;
        }
        return entries;
    }
}
