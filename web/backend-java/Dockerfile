FROM eclipse-temurin:17-jdk-alpine AS builder
WORKDIR /app
RUN apk --no-cache add maven
COPY pom.xml ./
RUN mvn -q -B -DskipTests dependency:go-offline
COPY src ./src
RUN mvn -q -B -DskipTests package

FROM alpine:3.20
# JRE for the app, and iputils-ping for the latency probe. ping needs raw
# sockets; hand just CAP_NET_RAW to the binary (stripping any setuid bit) so
# the server process itself can run as an unprivileged user.
RUN apk --no-cache add ca-certificates wget iputils-ping libcap openjdk17-jre-headless \
    && chmod u-s /bin/ping \
    && setcap cap_net_raw+ep /bin/ping \
    && adduser -S -D -H monitor
WORKDIR /app
COPY --from=builder /app/target/server-monitor-backend.jar ./server-monitor-backend.jar
USER monitor
EXPOSE 8080
CMD ["java", "-jar", "server-monitor-backend.jar"]
