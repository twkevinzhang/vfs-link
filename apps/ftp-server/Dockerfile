# syntax=docker/dockerfile:1.6

FROM golang:1.23-alpine AS builder

WORKDIR /src

COPY apps/ftp-server/go.mod apps/ftp-server/go.sum* ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY apps/ftp-server ./
RUN --mount=type=cache,target=/go/pkg/mod \
  --mount=type=cache,target=/root/.cache/go-build \
  CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/ftp-server ./cmd/ftp-server

FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

COPY --from=builder /out/ftp-server ./ftp-server
COPY apps/ftp-server/docker-entrypoint.sh ./docker-entrypoint.sh
RUN chmod +x ./docker-entrypoint.sh

ENV FTP_PORT=21

EXPOSE 21 8080 30000-30100

ENTRYPOINT ["./docker-entrypoint.sh"]
