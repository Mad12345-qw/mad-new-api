FROM golang:1.26.1-alpine@sha256:2389ebfa5b7f43eeafbd6be0c3700cc46690ef842ad962f6c5bd6be49ed82039 AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/mad-cpa-sdk-host .

FROM alpine:3.23@sha256:fd791d74b68913cbb027c6546007b3f0d3bc45125f797758156952bc2d6daf40
RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /out/mad-cpa-sdk-host /usr/local/bin/mad-cpa-sdk-host
EXPOSE 18417
HEALTHCHECK --interval=30s --timeout=5s --retries=3 CMD wget -q -O - http://127.0.0.1:18417/healthz >/dev/null || exit 1
ENTRYPOINT ["/usr/local/bin/mad-cpa-sdk-host"]
