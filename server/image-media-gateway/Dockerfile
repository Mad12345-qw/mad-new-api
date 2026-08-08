FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod main.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/image-media-gateway .

FROM debian:bookworm-slim
RUN useradd --system --uid 987 --home-dir /nonexistent --shell /usr/sbin/nologin imagecompat
COPY --from=build /out/image-media-gateway /usr/local/bin/image-media-gateway
USER imagecompat
ENTRYPOINT ["/usr/local/bin/image-media-gateway"]
