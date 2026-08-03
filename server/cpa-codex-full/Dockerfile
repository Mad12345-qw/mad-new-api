FROM golang:1.26-bookworm AS builder

WORKDIR /src

RUN apt-get update \
    && apt-get install -y --no-install-recommends build-essential git \
    && rm -rf /var/lib/apt/lists/*

COPY cpa-source/ ./
COPY server/cpa-codex/patches/0001-madapi-native-codex-dispatch.patch /patches/
COPY server/cpa-codex/patches/0002-madapi-passthrough-authorization.patch /patches/
COPY server/cpa-codex-full/patches/ /patches/

RUN git apply --check /patches/0001-madapi-native-codex-dispatch.patch \
    && git apply /patches/0001-madapi-native-codex-dispatch.patch \
    && git apply --check /patches/0002-madapi-passthrough-authorization.patch \
    && git apply /patches/0002-madapi-passthrough-authorization.patch \
    && git apply --check /patches/0003-madapi-responses-session-replay.patch \
    && git apply /patches/0003-madapi-responses-session-replay.patch \
    && git apply --check /patches/0004-madapi-native-responses-gateway.patch \
    && git apply /patches/0004-madapi-native-responses-gateway.patch \
    && gofmt -w internal/api/madapi_native_codex.go internal/api/madapi_native_codex_test.go internal/api/server_routes.go internal/config/config_types.go internal/runtime/executor/openai_compat_executor.go internal/runtime/executor/openai_compat_executor_madapi_test.go internal/translator/gemini/openai/responses/gemini_openai-responses_response.go internal/translator/gemini/openai/responses/gemini_openai-responses_response_test.go internal/watcher/synthesizer/config.go sdk/api/handlers/openai/madapi_responses_session.go sdk/api/handlers/openai/madapi_responses_session_test.go sdk/api/handlers/openai/openai_responses_handlers.go \
    && go mod download \
    && go test ./internal/api ./internal/runtime/executor ./sdk/api/handlers/openai \
    && CGO_ENABLED=1 GOOS=linux go build -buildvcs=false -ldflags="-s -w" -o /out/CLIProxyAPI ./cmd/server/

FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates python3 tzdata \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /out/CLIProxyAPI /CLIProxyAPI/CLIProxyAPI
COPY server/cpa-codex/catalog_sync.py /usr/local/bin/catalog-sync.py
COPY server/cpa-codex/entrypoint.sh /usr/local/bin/cpa-codex-entrypoint.sh

RUN chmod 0755 /usr/local/bin/catalog-sync.py /usr/local/bin/cpa-codex-entrypoint.sh \
    && mkdir -p /data

WORKDIR /CLIProxyAPI
EXPOSE 8317
ENV TZ=Asia/Shanghai
ENTRYPOINT ["/usr/local/bin/cpa-codex-entrypoint.sh"]
