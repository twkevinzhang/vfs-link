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

Call `POST <completeUrl>`. The server verifies that the final-key object
exists and that its size matches the declared size, then conditionally publishes
the logical mapping. An overwrite succeeds only if the destination still points
to the object observed when the session was created.

The physical key is the NFC-normalized relative logical path. Logical paths and
GCS object keys never begin with a slash; the logical root is the empty string.
Characters unsupported by portable Windows/Unix filenames, control characters,
and trailing dots or spaces are replaced with `_`. Windows device names are
prefixed with `_`. Empty segments, the reserved `_vfs-link*` first segment, and
keys over GCS's 1024-byte limit are rejected. Sanitization collisions are not
silently suffixed; the storage precondition reports a conflict.

GCS writes directly to that final key with a generation precondition. There is
no provisional object and no copy/move after verification. `GET <statusUrl>`
returns persisted status. The opaque GCS resumable session URI is persisted in
protected metadata so a different server instance can cancel it; it must never
be logged or returned by status responses. `DELETE <statusUrl>` cancels the
resumable session and does not delete the final object key.

The default maximum is 50 GiB and the default session lifetime is 24 hours.
Clients should treat `409` as a path or conditional-update conflict.
