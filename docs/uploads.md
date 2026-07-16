# Browser upload API

Local and GCS storage use the same three-stage contract. The browser does not
need to branch on `STORAGE_DRIVER`.

## 1. Create a session

```http
POST /api/uploads
Content-Type: application/json

{"path":"/reports/2026.pdf","size":12345678,"contentType":"application/pdf","overwrite":false}
```

The response contains `method`, `uploadUrl`, required `headers`, `completeUrl`,
`statusUrl`, and an expiry. A local URL points back to file-server. A GCS URL is
a bearer-like resumable session URI and must not be logged or shared.

## 2. Upload bytes

Send the `File` directly with `PUT <uploadUrl>` and the returned headers. Local
uploads stream into the local object adapter. GCS uploads travel directly from
the browser to Cloud Storage and do not consume Cloud Run request bandwidth.

## 3. Complete

Call `POST <completeUrl>`. The server verifies that the provisional object
exists and that its size matches the declared size, then conditionally publishes
the logical mapping. An overwrite succeeds only if the destination still points
to the object observed when the session was created.

`GET <statusUrl>` returns persisted status. GCS resumable session URIs are not
persisted because they grant upload capability; after losing the create
response, cancel the session and create a new one. `DELETE <statusUrl>` cancels
an incomplete session and cleans up its provisional object.

The default maximum is 50 GiB and the default session lifetime is 24 hours.
Clients should treat `409` as a path or conditional-update conflict.
