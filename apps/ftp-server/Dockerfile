FROM node:20-alpine

RUN apk add --no-cache openssl libc6-compat && npm install -g pnpm

WORKDIR /app

COPY dist/apps/ftp-server/index.cjs ./dist/index.js

COPY apps/ftp-server/prisma ./prisma
COPY apps/ftp-server/prisma.config.ts ./
COPY apps/ftp-server/scripts ./scripts

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

COPY apps/ftp-server/docker-entrypoint.sh ./
RUN chmod +x ./docker-entrypoint.sh

ENV NODE_ENV=production
ENV FTP_PORT=21

EXPOSE 21 30000-30005

ENTRYPOINT ["./docker-entrypoint.sh"]
