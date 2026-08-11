FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/madapi-cpa-official-gateway .

FROM alpine:3.22
RUN addgroup -S madapi && adduser -S -G madapi madapi
COPY --from=builder /out/madapi-cpa-official-gateway /usr/local/bin/madapi-cpa-official-gateway
USER madapi
EXPOSE 18317
ENTRYPOINT ["/usr/local/bin/madapi-cpa-official-gateway"]
