FROM golang:1.26-bookworm AS builder

WORKDIR /src

RUN apt-get update \
    && apt-get install -y --no-install-recommends build-essential git \
    && rm -rf /var/lib/apt/lists/*

COPY cpa-source/ ./
COPY server/cpa-codex/patches/0002-madapi-passthrough-authorization.patch /patches/
COPY server/cpa-codex/patches/0005-madapi-native-provider-authorization.patch /patches/
COPY server/cpa-codex/patches/0006-openai-responses-string-input.patch /patches/
COPY server/cpa-codex/patches/0007-claude-web-search-beta.patch /patches/
COPY server/cpa-codex/patches/0008-claude-cloak-auth-attributes.patch /patches/
COPY server/cpa-codex/patches/0009-claude-custom-tool-roundtrip.patch /patches/
COPY server/cpa-codex/patches/0010-claude-native-api-wire-mode.patch /patches/
COPY server/cpa-codex/patches/0011-claude-web-search-events.patch /patches/
COPY server/cpa-codex/patches/0012-claude-compaction-replay.patch /patches/
COPY server/cpa-codex-full/patches/ /patches/

RUN git apply --check /patches/0002-madapi-passthrough-authorization.patch \
    && git apply /patches/0002-madapi-passthrough-authorization.patch \
    && git apply --check /patches/0003-madapi-responses-session-replay.patch \
    && git apply /patches/0003-madapi-responses-session-replay.patch \
    && git apply --check /patches/0004-openai-chat-root-tool-schema.patch \
    && git apply /patches/0004-openai-chat-root-tool-schema.patch \
    && git apply --check /patches/0005-madapi-native-provider-authorization.patch \
    && git apply /patches/0005-madapi-native-provider-authorization.patch \
    && git apply --check /patches/0006-openai-responses-string-input.patch \
    && git apply /patches/0006-openai-responses-string-input.patch \
    && git apply --check /patches/0007-claude-web-search-beta.patch \
    && git apply /patches/0007-claude-web-search-beta.patch \
    && git apply --check /patches/0008-claude-cloak-auth-attributes.patch \
    && git apply /patches/0008-claude-cloak-auth-attributes.patch \
    && git apply --check /patches/0009-claude-custom-tool-roundtrip.patch \
    && git apply /patches/0009-claude-custom-tool-roundtrip.patch \
    && git apply --check /patches/0010-claude-native-api-wire-mode.patch \
    && git apply /patches/0010-claude-native-api-wire-mode.patch \
    && git apply --check /patches/0011-claude-web-search-events.patch \
    && git apply /patches/0011-claude-web-search-events.patch \
    && git apply --check /patches/0012-claude-compaction-replay.patch \
    && git apply /patches/0012-claude-compaction-replay.patch \
    && gofmt -w sdk/api/handlers/openai/madapi_responses_session.go sdk/api/handlers/openai/madapi_responses_session_test.go sdk/api/handlers/openai/openai_responses_handlers.go internal/config/config_types.go internal/runtime/executor/claude_executor_execute.go internal/runtime/executor/claude_executor_request.go internal/runtime/executor/claude_executor_stream.go internal/runtime/executor/claude_executor_test.go internal/runtime/executor/claude_executor_tokens.go internal/runtime/executor/openai_compat_executor.go internal/translator/claude/openai/responses/claude_openai-responses_request.go internal/translator/claude/openai/responses/claude_openai-responses_request_test.go internal/translator/claude/openai/responses/claude_openai-responses_response.go internal/translator/claude/openai/responses/claude_openai-responses_response_test.go internal/translator/openai/openai/responses/openai_openai-responses_request_test.go internal/translator/openai/openai/responses/openai_openai-responses_tools.go internal/watcher/synthesizer/config.go internal/watcher/synthesizer/config_test.go sdk/cliproxy/auth/conductor_execution.go sdk/cliproxy/auth/madapi_passthrough_test.go \
    && go mod download \
    && go test ./internal/runtime/executor ./internal/translator/claude/openai/responses ./internal/translator/openai/openai/responses ./internal/watcher/synthesizer ./sdk/api/handlers/openai ./sdk/cliproxy/auth \
    && CGO_ENABLED=1 GOOS=linux go build -buildvcs=false -ldflags="-s -w" -o /out/CLIProxyAPI ./cmd/server/

FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates python3 tzdata \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /out/CLIProxyAPI /CLIProxyAPI/CLIProxyAPI
COPY server/cpa-codex/catalog_sync.py /usr/local/bin/catalog-sync.py
COPY server/cpa-codex/entrypoint.sh /usr/local/bin/cpa-codex-entrypoint.sh

RUN sed -i 's/\r$//' /usr/local/bin/catalog-sync.py /usr/local/bin/cpa-codex-entrypoint.sh \
    && chmod 0755 /usr/local/bin/catalog-sync.py /usr/local/bin/cpa-codex-entrypoint.sh \
    && mkdir -p /data

WORKDIR /CLIProxyAPI
EXPOSE 8317
ENV TZ=Asia/Shanghai
ENTRYPOINT ["/usr/local/bin/cpa-codex-entrypoint.sh"]
