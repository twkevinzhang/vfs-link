# v1 與 v2 差異

本文件記錄 `vfs-link` 從 v1 Node.js / TypeScript 版改寫為 v2 Go 版的差異，以及這次改寫帶來的主要改進。

## 摘要

v1 是基於 Node.js 20、TypeScript、`ftp-srv`、Prisma ORM 與 `@google-cloud/storage` 的 FTP server。v2 保留同一個產品目標與資料模型，但將 FTP runtime 改寫為 Go binary，使用 `github.com/fclairamb/ftpserverlib`、`pgx` 與 `cloud.google.com/go/storage`。

v2 的核心目標不是改變使用者看到的 FTP 行為，而是降低長期服務的 runtime 複雜度、改善連線與串流模型，並移除生產環境對 Node.js、pnpm、Prisma generate / db push 的依賴。

## 架構差異

| 面向 | v1 | v2 |
| --- | --- | --- |
| Runtime | Node.js 20 + TypeScript bundle | Go 1.23+ native binary |
| FTP engine | `ftp-srv` | `github.com/fclairamb/ftpserverlib` |
| Database access | Prisma Client + `@prisma/adapter-pg` + `pg` | `pgx/v5` connection pool |
| GCS access | `@google-cloud/storage` | `cloud.google.com/go/storage` |
| Schema setup | container startup runs `prisma generate` and `prisma db push` | server startup runs `CREATE TABLE IF NOT EXISTS` SQL |
| Upload / download | Node streams to/from GCS | Go `io.Reader` / `io.Writer` streams to/from GCS |
| Docker runtime | Node Alpine image with pnpm and npm dependencies | Alpine image with a single compiled Go binary（目前名為 `file-server`） |
| Rebuild mapping | `npx tsx scripts/rebuild-mapping-table.ts` | `./file-server rebuild-mapping` subcommand |
| Local build | Nx esbuild target | Nx target invoking `apps/file-server/scripts/go.sh` |

## 保留的行為

- FTP 帳號密碼仍使用 `FTP_USER` 與 `FTP_PASS`。
- Passive mode 仍使用 `FTP_PASV_URL`、`FTP_PASV_MIN` 與 `FTP_PASV_MAX`。
- v2 當時保留既有 Docker 命名；目前已統一改為 `file-server` 與 `vfs-link/file-server`。
- PostgreSQL table 仍維持 `"File"`，並保留相同核心欄位：
  - `logicPath`
  - `physicalHash`
  - `size`
  - `isDirectory`
  - `updatedAt`
- 邏輯搬移仍只更新 database path。FTP client 執行 rename 時，不會 rename 或 copy GCS object。
- GCS 仍是實體檔案 bytes 的來源。
- `rebuild-mapping` 仍會根據目前設定 GCS bucket 內的 objects 重建 database mapping。

## v2 改進

### 1. Runtime dependency surface 更小

v1 的 production startup 需要 runtime image 內有 Node.js、pnpm、Prisma CLI、generated Prisma client files 與 npm dependencies。v2 則在 Alpine runtime image 中放入已編譯的 Go binary。

影響：

- Runtime moving parts 較少。
- Container startup 不再需要 Prisma generation。
- Container boot path 更短、更單純。
- package manager 與 runtime dependency 造成的維運面積降低。

### 2. FTP connection handling 更符合 server workload

FTP 會使用長時間存在的 control connection，並針對 upload、download、listing 建立獨立 data connection。v2 將 FTP protocol layer 交給 Go FTP server library，並透過 `afero.Fs` implementation 將 virtual filesystem 接上去。

影響：

- Go 的 concurrency model 更適合同時處理多個 FTP session。
- FTP protocol handling 與 storage logic 分離得更清楚。
- VFS 邊界更明確：`internal/vfs` 將 database metadata 與 GCS object stream 轉接到 FTP library 的 filesystem interface。

### 3. Streaming path 維持 memory-efficient

v2 保留 v1 的重要行為：file bytes 在 FTP data connection 與 GCS 之間以 stream 傳輸。Server 不需要把整個 upload 或 download file 放進記憶體。

影響：

- 大檔案傳輸主要受 stream buffer、網路、GCS 與 client 速度限制，而不是整檔記憶體配置。
- Upload completion 由 GCS writer close 後再 publish database mapping。

### 4. Upload interruption handling 更清楚

v2 的 upload file handle 實作 FTP library 的 transfer error hook。若 server 偵測到 interrupted upload，會刪除新建的 GCS object，而不是把 mapping publish 成完成檔案。

影響：

- 降低 interrupted upload 被 mapping 成有效 logical file 的機率。
- publish step 維持在 stream finalization 之後，upload lifecycle 較容易推理。

### 5. Database schema management 改為 explicit SQL

v1 將 schema synchronization 交給 Prisma。v2 在 startup 時直接用 SQL 建立需要的 table 與 index。

影響：

- Production runtime 移除 Prisma。
- 保留既有 table / field names，以相容既有資料。
- 最小 database contract 直接呈現在 code 與 README。

### 6. Build 與 deployment path 維持熟悉

v2 當時保留既有 Docker 與 Nx 命名，以減少部署變動；目前的高階啟動 target 為 `npx nx up file-server`。

影響：

- CI 目前以 `apps/file-server/Dockerfile` 建置 image，並可發佈不可變的 commit SHA tag 到 GHCR。
- Compose networking、ports、env vars 與 GCS credential mounting 維持不變。
- CD runner 應只 pull 已發佈的 image 並 recreate `file-server` service，不在 production host fetch Git 或 build source。

## 操作注意事項

- `apps/file-server/scripts/go.sh` 的存在是因為目前本機 asdf Go install 暴露出的 `GOROOT` 指向 asdf package root，而不是實際 Go root。這個 wrapper 只會在偵測到這種形狀時調整 `GOROOT`，並提供預設 `GOPROXY` / `GOSUMDB`，讓 local build 可重現。
- `ftpserverlib` 釘在 `v0.26.0`，因為更新版本需要比本次改寫所用 Go 1.23.5 更高的 Go toolchain。
- v2 已用以下方式驗證：
  - `cd apps/file-server && ./scripts/go.sh test ./...`
  - `npx nx build file-server`
  - `docker build -t vfs-link/file-server:test -f apps/file-server/Dockerfile .`

## 仍需實機驗證的相容性風險

正式推廣 v2 前，應使用真實 FTP client、真實 `DATABASE_URL` 與 `GCS_BUCKET` 驗證以下項目：

- `LIST`、`MLSD` 與 nested directory listing 行為。
- `STOR` upload 成功流程與 interrupted upload cleanup。
- `RETR` 從既有 mapping 下載。
- `RNFR` / `RNTO` 對 file 與 directory 的 rename 行為。
- `DELE`、`RMD` 與 recursive directory deletion 行為。
- 透過 `FTP_PASV_URL` 與 passive port range 建立 passive mode connection。
