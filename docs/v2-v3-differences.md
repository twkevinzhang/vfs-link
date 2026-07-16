# v2 與 v3 差異

本文件記錄 `vfs-link` 從 v2 Go + GCS 版改為 v3 storage-adapter 版的差異。

> 現行版本已將 runtime 統一命名為 `file-server`，並新增 WebDAV over HTTPS；
> FTP 僅作為遷移期可選協定。本文件以下內容保留 v2/v3 當時的歷史脈絡。

## 摘要

v2 已將 FTP server 從 Node.js 改寫為 Go，但 physical file bytes 固定存放於 Google Cloud Storage。v3 保留 Go FTP server 與 PostgreSQL mapping，新增可選擇 local 或 GCS 的 storage adapter，並加入 read-only HTTP API 與 React browser。預設 driver 為 local。

## 架構差異

| 面向 | v2 | v3 |
| --- | --- | --- |
| Storage driver | 固定 GCS | `local` 或 `gcs` |
| Required storage env | `GCS_BUCKET`, `GOOGLE_APPLICATION_CREDENTIALS` | local 使用 `LOCAL_STORAGE_ROOT`；GCS 使用 `GCS_BUCKET` 與 ADC |
| Physical bytes | GCS object | active store object |
| HTTP API | none | `GET /api/status`, `/api/files`, `/api/tree`, `/api/download` |
| Browser UI | none | `apps/web` React browser |
| File sharing | none | active store object export to independent GCS bucket |
| Compose volume | GCS key mount | local driver 使用 `objectdata:/data/objects` |
| Runtime image | Go binary | Go binary, plus HTTP API port |

## 保留的行為

- PostgreSQL `"File"` table 與核心欄位保留。
- FTP login、passive mode 與 port range 設定保留。
- `logicPath` 仍是使用者透過檔案協定看到的 path；現行 WebDAV 與過渡期 FTP 共用。
- `physicalHash` 仍是實體 bytes 的 object key。
- Rename 仍只更新 database path，不搬動 physical object。
- `rebuild-mapping` 仍從實體 object store 重建 database mapping。

## v3 改進

### 1. Selectable primary storage

v3 以 `STORAGE_DRIVER` 選擇 primary object store。`local` 使用 `LOCAL_STORAGE_ROOT`；`gcs` 使用 `GCS_BUCKET`。兩者都在 upload writer 成功 close 後才 publish database mapping。

影響：

- local 模式不依賴 GCS bucket 或 service account。
- 傳輸路徑更容易在本機 debug。
- Docker Compose 可直接用 named volume 保存 object bytes。
- 同一路徑重新上傳成功後，會清理 active store 中被取代的舊 object，避免覆寫產生孤兒檔。

### 2. Storage adapter boundary

v3 將原本直接依賴 GCS client 的 VFS 改為依賴 `blob.Store` interface。

影響：

- File protocol/VFS logic 與 storage implementation 解耦。
- GCS 與 local 都透過相同的 `blob.Store` contract；後續增加 S3、R2 等 driver 時不必重寫 FTP path。
- v3 不在不同 storage driver 之間自動同步或回填；切換 driver 前應自行確認目標 store 已具備資料庫 mapping 所指向的 objects。

### 3. Read-only HTTP API

v3 新增 HTTP API，讓 browser 可以用同一份 PostgreSQL mapping 瀏覽 FTP 檔案樹。

影響：

- 可直接用瀏覽器檢查目前 virtual file tree。
- 可下載 logical file，實際 bytes 由 active object store stream。
- API 初版保持 read-only，避免 UI 一開始就具有破壞性操作。

### 4. React browser

v3 新增 `apps/web`，用 React Router、TypeScript、Tailwind、Radix/shadcn-style components 建立 browser。

影響：

- 使用者可透過網頁瀏覽目錄、檔案、metadata 與 storage status。
- 前端只讀取 vfs-link API，不直接存取 primary object store。
- 預設 API endpoint 使用瀏覽器目前 origin，可用 `VITE_API_BASE_URL` 覆寫。

啟動方式：

```bash
pnpm --dir apps/web dev
```

建置方式：

```bash
pnpm --dir apps/web build
```

### 5. File sharing via GCS

v3 新增「檔案分享」流程。分享時由 Go server 從 active store 讀取指定 logical file，再上傳到獨立的 `SHARE_GCS_BUCKET`，產生分享連結，並透過 Telegram Bot API 發送通知。

影響：

- `GCS_BUCKET` 是 GCS driver 的 primary bucket；分享目的 bucket 仍由 `SHARE_GCS_BUCKET` 獨立決定。
- 前端檔案列表新增 `Share` action，會開啟 `/share/:id` 新頁面。
- 分享頁顯示目的 GCS URL、檔案資訊、Telegram 目標 chat、上傳狀態與完成連結。
- 後端以 `"Share"` table 持久化狀態，支援 `draft`、`uploading`、`completed`、`notified`、`notification_failed`、`failed`，並保留舊 `email_failed` 狀態重試相容性。
- Telegram 設定缺漏或 bot 無法送到目標 chat 時，GCS 上傳仍可完成，但通知狀態會標示為 `notification_failed`。

## 實機驗證重點

- FTP `STOR` 後 active store object 是否產生，DB mapping 是否一致。
- FTP `RETR` 是否能從 active store 下載。
- FTP rename 是否只改 DB，不改 physical object file name。
- `/api/files?path=/` 是否與 FTP `LIST /` 一致。
- `/api/download?path=...` 是否能下載同一個 logical file。
- `apps/web` 是否能顯示 `/api/status`、`/api/tree` 與 `/api/files` 的資料。
- 分享 draft 是否能由檔案 row 建立並開啟 `/share/:id`。
- `POST /api/shares/{id}/start` 是否能將 active store object 上傳到 `SHARE_GCS_BUCKET`。
- 完成上傳後是否透過 Telegram 送出包含分享連結的通知。
- Docker Compose volume `objectdata` 是否能跨 container restart 保留 object bytes。
