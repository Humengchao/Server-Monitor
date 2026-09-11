FROM golang:1.22-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o server ./cmd/server

FROM alpine:3.19
# ping (from iputils-ping) needs raw sockets. Hand just CAP_NET_RAW to the
# binary — stripping any setuid bit the package may ship — so the server
# process itself can run as an unprivileged user.
RUN apk --no-cache add ca-certificates wget iputils-ping libcap \
    && chmod u-s /bin/ping \
    && setcap cap_net_raw+ep /bin/ping \
    && adduser -S -D -H monitor
WORKDIR /app
COPY --from=builder /app/server .
USER monitor
EXPOSE 8080
CMD ["./server"]
