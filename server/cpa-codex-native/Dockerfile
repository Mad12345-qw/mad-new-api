FROM golang:1.26-bookworm AS builder

WORKDIR /src

RUN apt-get update \
    && apt-get install -y --no-install-recommends build-essential \
    && rm -rf /var/lib/apt/lists/*

COPY cpa-source/ ./
COPY server/cpa-madapi-dynamic-executor/ ./.madapi-dynamic-executor/

RUN go mod download \
    && go test ./.madapi-dynamic-executor \
    && CGO_ENABLED=1 go build -buildmode=c-shared -o /out/madapi-dynamic.so ./.madapi-dynamic-executor \
    && CGO_ENABLED=1 GOOS=linux go build -buildvcs=false -ldflags="-s -w" -o /out/CLIProxyAPI ./cmd/server/

FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates tzdata \
    && rm -rf /var/lib/apt/lists/* \
    && mkdir -p /app/plugins /data

COPY --from=builder /out/CLIProxyAPI /CLIProxyAPI/CLIProxyAPI
COPY --from=builder /out/madapi-dynamic.so /app/plugins/madapi-dynamic.so
COPY server/cpa-codex-native/entrypoint.sh /usr/local/bin/cpa-codex-native-entrypoint.sh

RUN sed -i 's/\r$//' /usr/local/bin/cpa-codex-native-entrypoint.sh \
    && chmod 0755 /usr/local/bin/cpa-codex-native-entrypoint.sh

WORKDIR /CLIProxyAPI
EXPOSE 8317
ENV TZ=Asia/Shanghai
ENTRYPOINT ["/usr/local/bin/cpa-codex-native-entrypoint.sh"]
