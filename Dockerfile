FROM node:22-alpine AS web
WORKDIR /src/web/admin
COPY web/admin/package.json web/admin/package-lock.json ./
RUN npm ci
COPY web/admin/ ./
RUN npm run build

FROM golang:1.26.4-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . ./
COPY --from=web /src/web/admin/dist ./web/admin/dist

ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/blitterserver ./cmd/blitterserver

FROM alpine:3.22
RUN apk add --no-cache ca-certificates ffmpeg tzdata \
    && addgroup -S -g 10001 blitterserver \
    && adduser -S -D -H -u 10001 -G blitterserver blitterserver \
    && install -d -o blitterserver -g blitterserver /data

ARG VERSION=dev
ARG REVISION=unknown
ARG CREATED=unknown
LABEL org.opencontainers.image.title="BlitterServer" \
      org.opencontainers.image.description="Self-hosted music backend for BlitterAmp" \
      org.opencontainers.image.url="https://github.com/BlitterAmp/BlitterServer" \
      org.opencontainers.image.source="https://github.com/BlitterAmp/BlitterServer" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.created="${CREATED}" \
      org.opencontainers.image.licenses="MIT"

COPY --from=build --chown=blitterserver:blitterserver /out/blitterserver /usr/local/bin/blitterserver

ENV BLITTER_LISTEN=0.0.0.0:8484 \
    BLITTER_DATA_DIR=/data \
    BLITTER_LOG_FORMAT=json

VOLUME ["/data"]
EXPOSE 8484
USER blitterserver
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
    CMD wget -q --spider http://127.0.0.1:8484/v1/ping || exit 1
ENTRYPOINT ["/usr/local/bin/blitterserver"]
