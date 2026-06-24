# v2 與 v3 差異

本文件記錄 `vfs-link` 從 v2 Go + GCS 版改為 v3 local-first 版的差異。

## 摘要

v2 已將 FTP server 從 Node.js 改寫為 Go，但 physical file bytes 仍存放於 Google Cloud Storage。v3 保留 Go FTP server 與 PostgreSQL mapping，將預設 storage 改成本機 object store，並新增 read-only HTTP API 與 local React browser。

## 架構差異

| 面向 | v2 | v3 |
| --- | --- | --- |
| Storage driver | GCS | local filesystem object store |
| Required storage env | `GCS_BUCKET`, `GOOGLE_APPLICATION_CREDENTIALS` | `LOCAL_STORAGE_ROOT` |
| Physical bytes | GCS object | local object file |
| HTTP API | none | `GET /api/status`, `/api/files`, `/api/tree`, `/api/download` |
| Browser UI | none | `apps/web` React local browser |
| Compose volume | GCS key mount | `objectdata:/data/objects` |
| Runtime image | Go binary | Go binary, plus HTTP API port |

## 保留的行為

- PostgreSQL `"File"` table 與核心欄位保留。
- FTP login、passive mode 與 port range 設定保留。
- `logicPath` 仍是使用者看到的 FTP path。
- `physicalHash` 仍是實體 bytes 的 object key。
- Rename 仍只更新 database path，不搬動 physical object。
- `rebuild-mapping` 仍從實體 object store 重建 database mapping。

## v3 改進

### 1. Local-first storage

v3 使用 `LOCAL_STORAGE_ROOT` 做為本機 object store。Upload 會先寫入 temp file，成功 close 後再 rename 成正式 object file，最後 publish database mapping。

影響：

- 開發與內網部署不再依賴 GCS bucket 或 service account。
- 傳輸路徑更容易在本機 debug。
- Docker Compose 可直接用 named volume 保存 object bytes。
- 同一路徑重新上傳成功後，會清理被取代的舊 local object，避免覆寫產生孤兒檔。

### 2. Storage adapter boundary

v3 將原本直接依賴 GCS client 的 VFS 改為依賴 `blob.Store` interface。

影響：

- FTP/VFS logic 與 storage implementation 解耦。
- 後續若要恢復 GCS、增加 S3、R2 或 hybrid sync，可新增 adapter 而不是重寫 FTP path。

### 3. Read-only HTTP API

v3 新增 HTTP API，讓 local browser 可以用同一份 PostgreSQL mapping 瀏覽 FTP 檔案樹。

影響：

- 可直接用瀏覽器檢查目前 virtual file tree。
- 可下載 logical file，實際 bytes 仍由 local object store stream。
- API 初版保持 read-only，避免 UI 一開始就具有破壞性操作。

### 4. Local React browser

v3 新增 `apps/web`，用 React Router、TypeScript、Tailwind、Radix/shadcn-style components 建立 local browser。

影響：

- 使用者可透過網頁瀏覽目錄、檔案、metadata 與 storage status。
- 前端只讀取本機 API，不需要 cloud API、外部 CDN 或登入服務。
- 預設 API endpoint 是 `http://localhost:8080`，可用 `VITE_API_BASE_URL` 覆寫。

啟動方式：

```bash
pnpm --dir apps/web dev
```

建置方式：

```bash
pnpm --dir apps/web build
```

## 實機驗證重點

- FTP `STOR` 後 local object file 是否產生，DB mapping 是否一致。
- FTP `RETR` 是否能從 local object file 下載。
- FTP rename 是否只改 DB，不改 physical object file name。
- `/api/files?path=/` 是否與 FTP `LIST /` 一致。
- `/api/download?path=...` 是否能下載同一個 logical file。
- `apps/web` 是否能顯示 `/api/status`、`/api/tree` 與 `/api/files` 的資料。
- Docker Compose volume `objectdata` 是否能跨 container restart 保留 object bytes。
