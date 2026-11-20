# syntax=docker/dockerfile:1.29.0@sha256:572cebbc2b15e6f84c077742952674889cb63cd5e9b80abdffcbe587b6f19183
FROM --platform=$BUILDPLATFORM oven/bun:1.4.4-alpine@sha256:4cf82f5686b196504fe119db3fcd9185b191f82ece82c40680e597228ec6070a AS frontend
WORKDIR /build/web
COPY web/package.json web/bun.lock ./
RUN bun install --frozen-lockfile
COPY web/ ./
RUN bun run check && bun run build

FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine@sha256:fa3d596898d9f126af7c01d14ee7597bab3f39cccf392ef36b69fe77cd681f18 AS backend
WORKDIR /build
ARG TARGETOS
ARG TARGETARCH
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
COPY --from=frontend /build/public/dist/ public/dist/
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/gps-tracker .

FROM alpine:3.23.8@sha256:469c9ea410b9215eaee7976ba6c0639039b444be8e5d638f7abbada189971eb2
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
