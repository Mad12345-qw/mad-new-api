FROM golang:1.26-bookworm AS builder

WORKDIR /src

RUN apt-get update \
    && apt-get install -y --no-install-recommends build-essential git \
    && rm -rf /var/lib/apt/lists/*

COPY cpa-source/ ./
COPY server/cpa-codex/patches/ /patches/

ARG CPA_PATCHED=0
RUN if [ "$CPA_PATCHED" != "1" ]; then \
		git apply --check /patches/0001-madapi-native-codex-dispatch.patch \
		&& git apply /patches/0001-madapi-native-codex-dispatch.patch; \
	fi \
	&& gofmt -w internal/api/madapi_native_codex.go internal/api/madapi_native_codex_test.go internal/api/server_routes.go \
	&& go mod download \
	&& CGO_ENABLED=1 GOOS=linux go build -buildvcs=false -ldflags="-s -w" -o /out/CLIProxyAPI ./cmd/server/

FROM debian:bookworm-slim

RUN apt-get update \
	&& apt-get install -y --no-install-recommends ca-certificates tzdata \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /out/CLIProxyAPI /CLIProxyAPI/CLIProxyAPI
COPY server/cpa-codex/entrypoint.sh /usr/local/bin/cpa-codex-entrypoint.sh

RUN chmod 0755 /usr/local/bin/cpa-codex-entrypoint.sh \
	&& mkdir -p /data

WORKDIR /CLIProxyAPI
EXPOSE 8317
ENV TZ=Asia/Shanghai
ENTRYPOINT ["/usr/local/bin/cpa-codex-entrypoint.sh"]
