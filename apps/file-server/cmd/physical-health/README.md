# physical-health

`physical-health` 會讀取資料庫中的檔案 mapping，並檢查每個 `physicalHash` 是否存在於 `STORAGE_DRIVER` 指定的 active object store，以及 object size 是否與資料庫記錄一致。

這個工具一次只檢查一個 active store，不處理不同 storage backend 之間的資料同步、搬移或修復，也不檢查分享用的 `SHARE_GCS_BUCKET`。

## 執行方式

請從 `apps/file-server` 目錄執行：

```sh
./scripts/check-physical-health.sh
```

工具預設載入目前目錄的 `.env`，也可以用 `-env-file` 指定其他檔案。命令列參數的優先順序高於環境變數。

所有模式都需要：

```dotenv
DATABASE_URL=postgres://...
STORAGE_DRIVER=local
```

### Local storage

當 `STORAGE_DRIVER=local` 時，工具會對每個 object path 執行本機檔案 metadata 檢查：

```dotenv
STORAGE_DRIVER=local
LOCAL_STORAGE_ROOT=/data/objects
```

若未設定 `LOCAL_STORAGE_ROOT`，預設為 `./data/objects`。

### GCS storage

當 `STORAGE_DRIVER=gcs` 時，工具會讀取 `GCS_BUCKET` 內各 object 的 metadata：

```dotenv
STORAGE_DRIVER=gcs
GCS_BUCKET=vfs-link-objects
GOOGLE_APPLICATION_CREDENTIALS=/path/to/service-account.json
```

在 Google Cloud runtime 使用 Application Default Credentials 時，可以不設定 `GOOGLE_APPLICATION_CREDENTIALS`。執行身分至少需要讀取 bucket 與 object metadata 的權限。

## 參數

| 參數 | 預設值 | 說明 |
| --- | --- | --- |
| `-env-file` | `.env` | 載入環境變數的檔案；傳入空字串可停用 |
| `-database-url` | `DATABASE_URL` | PostgreSQL connection string |
| `-storage-driver` | `STORAGE_DRIVER`，未設定時為 `local` | active storage driver：`local` 或 `gcs` |
| `-local-root` | `LOCAL_STORAGE_ROOT`，未設定時為 `./data/objects` | local object root |
| `-gcs-bucket` | `GCS_BUCKET` | active GCS bucket 名稱 |
| `-google-credentials` | `GOOGLE_APPLICATION_CREDENTIALS` | service account JSON 路徑 |
| `-prefix` | `/` | 僅檢查此 logical path 與其子路徑 |
| `-csv` | 空 | 將逐檔結果寫入指定 CSV |
| `-fail-on-unhealthy` | `false` | 有 unhealthy 檔案時以 exit code 2 結束 |
| `-workers` | `8` | GCS metadata 檢查的並行數；小於 1 時改為 1 |
| `-timeout` | `30m` | 整次掃描的 timeout |

## 狀態與結束碼

| status | class | 說明 |
| --- | --- | --- |
| `ok` | `healthy` | object 存在，且 size 與資料庫一致 |
| `object_missing` | `unhealthy` | active store 找不到 object |
| `size_mismatch` | `unhealthy` | object size 與資料庫不一致 |
| `storage_error` | `unhealthy` | 權限、連線、本機讀取或其他 storage metadata 錯誤 |

一般執行成功時 exit code 為 0。參數、設定、資料庫等執行錯誤為 1。指定 `-fail-on-unhealthy` 且報告內有 unhealthy 檔案時為 2。

## CSV

指定 `-csv` 後，CSV 每列代表一筆檔案 mapping，欄位如下：

- `logicPath`：VFS logical path。
- `expectedSize`：資料庫記錄的 size。
- `physicalHash`：active store 的 object key。
- `topDir`：logical path 的第一層目錄。
- `status`、`class`：健康狀態與分類。
- `storageDriver`：本次檢查使用的 driver。
- `objectLocation`：本機絕對路徑或 `gs://bucket/object`。
- `objectSize`：實際讀到的 object size；無法取得時為 0。
- `error`：錯誤或 size mismatch 細節。

## 範例

檢查 local active store，並在發現異常時讓 CI 或排程失敗：

```sh
./scripts/check-physical-health.sh \
  -storage-driver local \
  -local-root /data/objects \
  -fail-on-unhealthy
```

檢查 GCS active store 的 `/projects/demo` 範圍並輸出 CSV：

```sh
./scripts/check-physical-health.sh \
  -storage-driver gcs \
  -gcs-bucket vfs-link-objects \
  -prefix /projects/demo \
  -workers 16 \
  -csv /tmp/physical-health.csv
```
