# Server Monitor — Java backend

Spring Boot 3 port of the Go web backend in [`../backend`](../backend). The
frontend talks to it through the same REST + WebSocket surface, and both
backends can run against the same database (schema, GCM-encrypted secrets and
JWTs are byte-compatible).

## Layout

```
config/       environment config, datasource + migrations, CORS, interceptors
security/     HS256 JWT, per-IP token buckets, trusted-IP helper
repo/         SQL for users, servers, tags, credentials, metrics, alerts, uptime
service/      collector (SSH metric polls + tiered rollups), alert engine,
              webhook notifier, batch exec, processes/services/ports, uptime
ssh/          sshd-core plumbing: dial, connection cache, bounded commands
files/        SFTP and container (`docker exec`) remote-file stores
api/          REST controllers mirroring the Gin handlers' JSON shapes
ws/           SSH web terminal + docker exec WebSocket endpoints
```

## Run

```bash
cp .env.example .env   # set DATABASE_URL / JWT_SECRET / ENCRYPTION_KEY
mvn -DskipTests package
java -jar target/server-monitor-backend.jar
```

Migrations run on startup (the same `migrations.sql` the Go backend embeds).
Everything else — poll/eval intervals, rate limits, backoff, retention — matches
the Go behavior; env vars are identical.

## Docker

```bash
docker build -t server-monitor-backend-java .
```

The image runs as an unprivileged user with `CAP_NET_RAW` granted only to
`ping`, mirroring `../backend/Dockerfile`.
