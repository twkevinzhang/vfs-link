# 📋 vfs-link (ftp-srv 版本)

`vfs-link` 是一個由資料庫（PostgreSQL）驅動的 FTP 伺服器。它的特色在於檔案清單與操作完全與實體儲存（Google Cloud Storage, GCS）解耦，實現了極速的「邏輯移動」操作。

## 1. 專案目標

開發一個 FTP 伺服器，其核心需求為：

- **資料庫驅動**：檔案的虛擬路徑與結構由 PostgreSQL 維管。
- **GCS 整合**：實體檔案儲存於 GCS，並以 Hash 加密格式命名。
- **邏輯移動**：執行 `mv` (rename) 指令時，僅更新資料庫中的 `logicPath`，完全不觸動 GCS 上的物件，實現瞬間移動。

## 2. 技術棧 (Tech Stack)

- **Runtime**: Node.js 20+ (TypeScript)
- **FTP Engine**: `ftp-srv`
- **Database**: PostgreSQL 15 (使用 Prisma ORM)
- **Storage**: Google Cloud Storage SDK (`@google-cloud/storage`)
- **Deployment**: Docker Compose

## 3. 資料庫 Schema (Prisma)

專案使用 Prisma 進行資料庫操作，核心模型如下（參見 `apps/ftp-server/prisma/schema.prisma`）：

```prisma
model File {
  id           Int      @id @default(autoincrement())
  logicPath    String   @unique // 顯示給使用者的虛擬路徑 (例: /docs/resume.pdf)
  physicalHash String   // GCS 上的真實檔名 (例: gs://bucket/abc-123-hash)
  size         BigInt   @default(0)
  isDirectory  Boolean  @default(false)
  updatedAt    DateTime @default(now()) @updatedAt

  @@index([logicPath])
}
```

## 4. 核心功能實作

### A. 自定義 FileSystem 介面

專案繼承了 `ftp-srv` 的 `FileSystem` 類別，並在 `apps/ftp-server/src/vfs/database-vfs.ts` 中覆寫關鍵方法：

1.  **`list(path)`**: 從資料庫查詢該目錄下的所有 `logicPath` 記錄，並過濾出直接子項。
2.  **`rename(from, to)`**: 攔截 FTP `RENAME` 指令，僅執行資料庫 `UPDATE` 操作。**嚴禁**調用 GCS API 移動物件。
3.  **`read(path)`**: 根據 `logicPath` 取得 `physicalHash`，並回傳 `gcs.file(physicalHash).createReadStream()`。
4.  **`write(path)`**: 將上傳流 Pipe 到 GCS，完成後在資料庫建立/更新 Mapping 記錄。

### B. 安全與驗證

- **用戶驗證**：簡單的使用者名稱/密碼比對（預設由環境變數控管）。
- **GCS 認證**：透過 `GOOGLE_APPLICATION_CREDENTIALS` 指向容器內的 `/app/gcp-key.json`。

## 5. Docker 部署指南

### 環境變數 (.env)

請參考 `.env.example` 設定以下變數：

- `FTP_USER`: FTP 登入帳號
- `FTP_PASS`: FTP 登入密碼
- `GCS_BUCKET`: Google Cloud Storage Bucket 名稱
- `DATABASE_URL`: PostgreSQL 連線字串

### 啟動指令

```bash
docker-compose up -d
```

### Docker 服務架構

- **`ftp-server`**: 營運 FTP 服務，映射埠 21 與 30000-30005。
- **`db`**: PostgreSQL 15 資料庫服務。
