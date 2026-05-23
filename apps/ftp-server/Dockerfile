# syntax=docker/dockerfile:1.6

# ===== Builder stage: 在 image 內 build TypeScript bundle =====
FROM node:20-alpine AS builder

RUN apk add --no-cache libc6-compat && corepack enable

WORKDIR /workspace

# 先 COPY manifest，最大化 layer cache
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
COPY nx.json tsconfig.base.json tsconfig.json ./
COPY apps/ftp-server/package.json ./apps/ftp-server/package.json

RUN pnpm install --frozen-lockfile

# 再 COPY source 並 build
COPY apps/ftp-server ./apps/ftp-server

# nx build 前先 generate Prisma client，否則 TS 找不到 PrismaClient 型別
RUN pnpm exec prisma generate --schema=apps/ftp-server/prisma/schema.prisma

RUN pnpm exec nx build ftp-server --configuration=production


# ===== Runtime stage: 只帶必要產物 =====
FROM node:20-alpine

RUN apk add --no-cache openssl libc6-compat && npm install -g pnpm

WORKDIR /app

COPY --from=builder /workspace/dist/apps/ftp-server/index.cjs ./dist/index.js

COPY --from=builder /workspace/apps/ftp-server/prisma ./prisma
COPY --from=builder /workspace/apps/ftp-server/prisma.config.ts ./
COPY --from=builder /workspace/apps/ftp-server/scripts ./scripts

RUN echo '{\
  "name": "ftp-server",\
  "dependencies": {\
    "@google-cloud/storage": "^7.18.0",\
    "@prisma/adapter-pg": "^7.5.0",\
    "@prisma/client": "^7.5.0",\
    "dotenv": "^16.4.0",\
    "ftp-srv": "^4.6.3",\
    "pg": "^8.11.0",\
    "prisma": "^7.5.0",\
    "tsx": "^4.19.0",\
    "uuid": "^13.0.0"\
  }\
}' > package.json

RUN pnpm install

COPY --from=builder /workspace/apps/ftp-server/docker-entrypoint.sh ./
RUN chmod +x ./docker-entrypoint.sh

ENV NODE_ENV=production
ENV FTP_PORT=21

EXPOSE 21 30000-30005

ENTRYPOINT ["./docker-entrypoint.sh"]
