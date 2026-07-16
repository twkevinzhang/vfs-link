FROM node:22-alpine AS web-builder

WORKDIR /src

RUN corepack enable

COPY package.json pnpm-lock.yaml pnpm-workspace.yaml nx.json tsconfig.json tsconfig.base.json ./
COPY apps/web/package.json apps/web/package.json
COPY apps/file-server/package.json apps/file-server/package.json
RUN pnpm install --frozen-lockfile --filter @vfs-link/web...

COPY apps/web apps/web
ARG VITE_BASE_PATH=/
ARG VITE_API_BASE_URL=
ENV VITE_BASE_PATH=$VITE_BASE_PATH
ENV VITE_API_BASE_URL=$VITE_API_BASE_URL
RUN pnpm --dir apps/web build

FROM golang:1.23-alpine AS builder

WORKDIR /src

COPY apps/file-server/go.mod apps/file-server/go.sum* ./
RUN go mod download

COPY apps/file-server ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/file-server ./cmd/file-server \
    && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/physical-health ./cmd/physical-health

FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

COPY --from=builder /out/file-server ./file-server
COPY --from=builder /out/physical-health ./physical-health
COPY --from=web-builder /src/apps/web/build/client ./web
COPY apps/file-server/docker-entrypoint.sh ./docker-entrypoint.sh
RUN chmod +x ./docker-entrypoint.sh

ENV FTP_PORT=21
ENV WEB_STATIC_ROOT=/app/web

EXPOSE 21 8080 30000-30100

ENTRYPOINT ["./docker-entrypoint.sh"]
