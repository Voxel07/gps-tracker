# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM oven/bun:1.4.4-alpine AS frontend
WORKDIR /build/web
COPY web/package.json web/bun.lock ./
RUN bun install --frozen-lockfile
COPY web/ ./
RUN bun run check && bun run build

FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine AS backend
WORKDIR /build
ARG TARGETOS
ARG TARGETARCH
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
COPY --from=frontend /build/public/dist/ public/dist/
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/gps-tracker .

FROM alpine:3.23
RUN apk add --no-cache ca-certificates \
    && addgroup -S -g 10001 tracker \
    && adduser -S -D -H -u 10001 -G tracker tracker \
    && mkdir /data \
    && chown tracker:tracker /data
COPY --from=backend /out/gps-tracker /usr/local/bin/gps-tracker
USER tracker
WORKDIR /data
ENV DATABASE_PATH=/data/tracker.sqlite LISTEN_ADDR=:8080
EXPOSE 8080
HEALTHCHECK --interval=15s --timeout=4s --start-period=5s CMD ["/usr/local/bin/gps-tracker", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/gps-tracker"]
