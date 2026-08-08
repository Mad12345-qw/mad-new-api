FROM golang:1.26-bookworm AS builder

WORKDIR /src

RUN apt-get update \
    && apt-get install -y --no-install-recommends build-essential git \
    && rm -rf /var/lib/apt/lists/*

COPY cpa-source/ ./

# The pinned MadAPI CPA fork commit contains the complete selected-channel
# runtime. Formatting and tests are mandatory before the binary is built.
RUN set -eu; \
    unformatted="$(gofmt -l \
        internal/api/madapi_native_codex.go \
        internal/api/madapi_native_codex_test.go \
        internal/api/server_routes.go \
        internal/config/config_types.go \
        internal/runtime/executor/claude_executor_execute.go \
        internal/runtime/executor/claude_executor_request.go \
        internal/runtime/executor/claude_executor_stream.go \
        internal/runtime/executor/claude_executor_test.go \
        internal/runtime/executor/claude_executor_tokens.go \
        internal/runtime/executor/codex_executor_imagegen_test.go \
        internal/runtime/executor/codex_executor_request.go \
        internal/runtime/executor/openai_compat_executor.go \
        internal/runtime/executor/openai_compat_executor_madapi_test.go \
        internal/translator/claude/openai/responses/claude_openai-responses_request.go \
        internal/translator/claude/openai/responses/claude_openai-responses_request_test.go \
        internal/translator/claude/openai/responses/claude_openai-responses_response.go \
        internal/translator/claude/openai/responses/claude_openai-responses_response_test.go \
        internal/translator/gemini/openai/responses/gemini_openai-responses_response.go \
        internal/translator/gemini/openai/responses/gemini_openai-responses_response_test.go \
        internal/translator/openai/openai/responses/openai_openai-responses_request_test.go \
        internal/translator/openai/openai/responses/openai_openai-responses_response.go \
        internal/translator/openai/openai/responses/openai_openai-responses_response_test.go \
        internal/translator/openai/openai/responses/openai_openai-responses_tools.go \
        internal/watcher/synthesizer/config.go \
        internal/watcher/synthesizer/config_test.go \
        sdk/api/handlers/openai/madapi_responses_session.go \
        sdk/api/handlers/openai/madapi_responses_session_test.go \
        sdk/api/handlers/openai/openai_responses_handlers.go \
        sdk/cliproxy/auth/conductor_execution.go \
        sdk/cliproxy/auth/madapi_passthrough_test.go)"; \
    test -z "$unformatted"; \
    go mod download; \
    GOMAXPROCS=4 go test -p 4 \
        ./internal/api \
        ./internal/translator/claude/openai/responses \
        ./internal/translator/openai/openai/responses \
        ./internal/watcher/synthesizer \
        ./sdk/api/handlers/openai \
        ./sdk/cliproxy/auth; \
    GOMAXPROCS=4 go test -p 4 \
        -run '^(TestApplyMadAPIPassthroughAuthorization|TestBuildClaudeCompactResponseUsesCompactionContract|TestEnsureImageGenerationTool_MadAPIPassthroughResponsesLiteInjectsTool)$' \
        ./internal/runtime/executor; \
    CGO_ENABLED=1 GOOS=linux go build -buildvcs=false -ldflags="-s -w" -o /out/CLIProxyAPI ./cmd/server/

FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates tzdata \
    && rm -rf /var/lib/apt/lists/* \
    && mkdir -p /data

COPY --from=builder /out/CLIProxyAPI /CLIProxyAPI/CLIProxyAPI
COPY server/cpa-codex-native/entrypoint.sh /usr/local/bin/cpa-codex-native-entrypoint.sh

RUN sed -i 's/\r$//' /usr/local/bin/cpa-codex-native-entrypoint.sh \
    && chmod 0755 /usr/local/bin/cpa-codex-native-entrypoint.sh

WORKDIR /CLIProxyAPI
EXPOSE 8317
ENV TZ=Asia/Shanghai
ENTRYPOINT ["/usr/local/bin/cpa-codex-native-entrypoint.sh"]
