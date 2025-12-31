# FlatStorage

## 簡述
這是一個基於 vue3 及 express.js 開發的 GCP Storage 檔案瀏覽器。
所有檔案的命名、路徑都被儲存在 records 當中，真實存放在 GCP 上的檔案只是一堆 UUID。
所以重新命名檔案時，不會真的發生複製成本。

## User Story
1. 使用者進入 UI 時，需要提供連線資訊
2. 第一次連線到 storage 時，會製作 metadata.json
3. 接下來的移動、刪除、複製操作都只發生在 metadata.json 中


## how to develop site
熟悉 nx 指令


## before deploy
1. create GCP project and storage bucket, set IAM permission
2. login gCloud cli
