FROM golang:1.26-bookworm AS builder

WORKDIR /src

RUN apt-get update \
    && apt-get install -y --no-install-recommends build-essential git \
    && rm -rf /var/lib/apt/lists/*

COPY cpa-source/ ./
COPY server/cpa-codex/patches/0002-madapi-passthrough-authorization.patch /patches/
COPY server/cpa-codex-full/patches/ /patches/

RUN git apply --check /patches/0002-madapi-passthrough-authorization.patch \
    && git apply /patches/0002-madapi-passthrough-authorization.patch \
    && git apply --check /patches/0003-madapi-responses-session-replay.patch \
    && git apply /patches/0003-madapi-responses-session-replay.patch \
    && git apply --check /patches/0004-openai-chat-root-tool-schema.patch \
    && git apply /patches/0004-openai-chat-root-tool-schema.patch \
    && gofmt -w sdk/api/handlers/openai/madapi_responses_session.go sdk/api/handlers/openai/madapi_responses_session_test.go sdk/api/handlers/openai/openai_responses_handlers.go internal/config/config_types.go internal/runtime/executor/openai_compat_executor.go internal/translator/openai/openai/responses/openai_openai-responses_request_test.go internal/translator/openai/openai/responses/openai_openai-responses_tools.go internal/watcher/synthesizer/config.go \
    && go mod download \
    && go test ./internal/translator/openai/openai/responses ./sdk/api/handlers/openai \
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
