# GCP 預算熔斷器

這個控制面部署在 FinOps project `chromatic-idea-405303`，監看 Billing Account
`01420A-B7869F-A617B2` 中 `storage-403503` 的月曆月實際成本。預算為
TWD 1,000，計算時排除 credits/discounts。

```text
  Cloud Billing（TWD 1,000／月）
                 │ 多次／日、at-least-once
                 ▼
      Pub/Sub: budget-kill-events
                 │ 1 小時有限重試 → 7 日 DLQ
                 ▼
   Cloud Run function（FinOps project）
     min 0 / max 1 / DRY_RUN first ✨
                 │ 僅允許指定 account/budget/project
                 ▼
 Cloud Billing API: projects.updateBillingInfo
                 │ production arm 後才可能執行
                 ▼
       storage-403503 Billing unlink 💥
```

## 安全語意

- Budget notification 不只在 100% 發送，而是每天多次發布目前狀態，也可能重複或亂序。
- Function 只使用 `costAmount >= budgetAmount` 的 actual spend；forecast 不觸發。
- `billingAccountId`、`budgetId`、schema、TWD、固定 TWD 1,000、當期時間窗都必須吻合。
- 修改前先讀取 project Billing 狀態。已 unlink 是成功的冪等 no-op；連到不同帳戶則 fail closed。
- `deploy.sh` 永遠拒絕 `DRY_RUN=false`。正式 arm 必須另做一次具破壞性的明確核准。
- Budget 和通知有入帳延遲，因此 TWD 1,000 是觸發點，不是精準 hard cap；停用前尚未入帳的費用仍會補收。

## 本機驗證

```bash
cd deploy/gcp/budget-kill-switch/function
npm ci
npm test

bash -n ../preflight.sh ../deploy.sh ../smoke-test.sh ../disarm.sh
```

測試涵蓋低於、剛好及超過門檻、forecast-only、錯誤 account/budget/currency/schema、
舊月份、malformed payload、重複通知、already-unlinked、帳戶不符與 Billing API 暫時錯誤。

## 部署前盤點

所有命令都顯式使用 `twkevinzhang@gmail.com`，不依賴本機 default project：

```bash
deploy/gcp/budget-kill-switch/preflight.sh
```

輸出會寫到 ignored 的 `tmp/gcp-budget-kill-switch-preflight-*.txt`。閱讀並確認內容後，
才可把該路徑交給部署腳本：

```bash
PREFLIGHT_FILE=tmp/gcp-budget-kill-switch-preflight-YYYYMMDD-HHMMSS.txt \
CONFIRM_DEPLOY=chromatic-idea-405303:DRY_RUN \
deploy/gcp/budget-kill-switch/deploy.sh
```

部署固定使用 `asia-east1`、Node.js 24、request-based billing、CPU throttling、
1 vCPU、512MiB、service/revision min 0、service/revision max 1、concurrency 1、
timeout 60 秒、private ingress、無 URL tag。
腳本只允許一次 source build；若已存在 service，會停止，避免未核准的付費重建。
舊版 gcloud 尚未提供 service-level `--max` 時，腳本會在 Eventarc 啟用前透過 Cloud Run
v2 API 把平台預設的 service max 20 降為 1；這可能建立一個相同 image 的額外 revision，
但所有 revisions 都必須保持 min 0、revision max 1、CPU throttling，且不得帶 tag。

## DRY_RUN 整合驗收

從 Budget read-back 取得 UUID 後，只發布兩筆 synthetic 訊息：

```bash
BUDGET_ID=actual-budget-uuid \
CONFIRM_SMOKE=chromatic-idea-405303:DRY_RUN \
deploy/gcp/budget-kill-switch/smoke-test.sh
```

預期 Logging 事件為 `budget_below_limit` 與 `billing_disable_simulated`。測試前後必須確認
`storage-403503` 仍連到原 Billing Account。

## 立即 disarm

```bash
CONFIRM_DISARM=chromatic-idea-405303 \
deploy/gcp/budget-kill-switch/disarm.sh
```

這會先明確把 `DRY_RUN=true` 寫回新 revision，再刪除 Eventarc trigger。Budget、topics、
DLQ 與 Function 保留供調查；完整移除前先匯出 DLQ 與 Audit Logs。

## 完整清除順序

確認不需要保留證據後，依序刪除 Eventarc trigger、Budget、Cloud Run service、DLQ
subscription、兩個 topics 與三個專用 service accounts，並移除 target project 上 runtime SA
的 `roles/billing.projectManager`、`roles/browser`。如果 FinOps project 不再有其他 source
deploy，還要刪除 `cloud-run-source-deploy` Artifact Registry repository 和
`run-sources-chromatic-idea-405303-asia-east1` bucket；先確認沒有共用 image/source。
所有 delete 命令都必須顯式帶
`--account=twkevinzhang@gmail.com` 與正確 `--project`／`--billing-account`。

## 成本上界

不扣 free tier，以 Tier 1 價格及 `1 USD = NT$32.56`：

```text
3600 × (1 × USD 0.000024 + 0.5 × USD 0.0000025)
+ 60 requests × USD 0.40 / 1,000,000
= USD 0.090924 / hour
= 約 NT$2.96/h、NT$71.05/day、NT$2,131.55/30d
```

這是假設 max=1 每秒都處於 request-active 的極端上界。正常每天數次通知、min=0，
預期 compute 接近零；另有少量 Artifact Registry image、Cloud Build、Pub/Sub 與 logs 費用。

## 正式 arm 前仍須完成

1. 在 Console 確認 `storage-403503` 的 Billing link 沒有 lock。
2. 確認沒有 active/pending CUD 阻止 unlink。
3. 由 project 外的管理身分驗證可重新 link，並接受停機、資源刪除與最長約 24 小時復原風險。
4. 重新執行完整 preflight、Budget read-back、IAM simulation 與 dry-run exact-threshold 測試。
5. 另取得 `DRY_RUN=false` 的具破壞性明確核准；本 repo 目前刻意不提供一鍵 arm 腳本。

官方文件：

- [Programmatic budget notifications](https://docs.cloud.google.com/billing/docs/how-to/budgets-programmatic-notifications)
- [Disable billing with notifications](https://docs.cloud.google.com/billing/docs/how-to/disable-billing-with-notifications)
- [Cloud Run functions deployment](https://docs.cloud.google.com/run/docs/deploy-functions)
- [Eventarc retry and DLQ](https://docs.cloud.google.com/eventarc/docs/retry-events)

可靠度分數：96%

- 依據：官方 Cloud Billing／Pub/Sub／Cloud Run／Eventarc 文件、本 repo 的實際部署慣例與自動化測試。
- 假設：Billing Account 保持 TWD/open，Budget payload 維持 schema 1.0，兩 project 維持同一 Billing Account。
- 驗證方式：部署前後 read-back Budget、IAM、service/revisions、Eventarc subscription 與 Billing link；再核對 Logging 和延遲後的 Billing metrics。
