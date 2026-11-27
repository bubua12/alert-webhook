# Stage 1: Build binary with embedded Web Console assets
FROM golang:1.24-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG TARGETOS=linux
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w" -o /out/alert-webhook .

# Stage 2: Minimal non-root runtime image
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata wget \
    && addgroup -S appgroup \
    && adduser -S appuser -G appgroup

ENV TZ=Asia/Shanghai

WORKDIR /app

RUN mkdir -p /app/config /app/data /app/logs \
    && chown -R appuser:appgroup /app /app/config /app/data /app/logs

COPY --from=builder --chown=appuser:appgroup /out/alert-webhook /app/alert-webhook

USER appuser

EXPOSE 18082

ENV ALERT_WEBHOOK_CONFIG=/app/config/config.yaml \
    ALERT_WEBHOOK_DATA_DIR=/app/data \
    ALERT_WEBHOOK_LOG_DIR=/app/logs

HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD wget -qO- http://127.0.0.1:18082/healthz || exit 1

ENTRYPOINT ["/app/alert-webhook"]
