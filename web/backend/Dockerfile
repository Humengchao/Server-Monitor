FROM golang:1.26.10-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG BUILD_VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build -o server ./cmd/server
RUN CGO_ENABLED=0 GOOS=linux go build -o metricsrepair ./cmd/metricsrepair
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "-s -w -X main.version=${BUILD_VERSION}" -o agent-assets/server-monitor-agent-linux-amd64 ./cmd/agent \
    && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags "-s -w -X main.version=${BUILD_VERSION}" -o agent-assets/server-monitor-agent-linux-arm64 ./cmd/agent \
    && CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags "-s -w -X main.version=${BUILD_VERSION}" -o agent-assets/server-monitor-agent-windows-amd64.exe ./cmd/agent \
    && CGO_ENABLED=0 GOOS=windows GOARCH=arm64 go build -ldflags "-s -w -X main.version=${BUILD_VERSION}" -o agent-assets/server-monitor-agent-windows-arm64.exe ./cmd/agent

FROM alpine:3.24
# ping (from iputils-ping) needs raw sockets. Hand just CAP_NET_RAW to the
# binary — stripping any setuid bit the package may ship — so the server
# process itself can run as an unprivileged user.
RUN apk --no-cache add ca-certificates wget iputils-ping libcap \
    && chmod u-s /bin/ping \
    && setcap cap_net_raw+ep /bin/ping \
    && adduser -S -D -H monitor
WORKDIR /app
COPY --from=builder /app/server .
COPY --from=builder /app/metricsrepair .
COPY --from=builder /app/agent-assets ./agent-assets
USER monitor
EXPOSE 8080
CMD ["./server"]
