# Browser upload API

Local and GCS storage use the same resumable three-stage contract. The browser
does not need to branch on `STORAGE_DRIVER`.

## 1. Create a session

```http
POST /api/uploads
Content-Type: application/json

{"path":"/reports/2026.pdf","size":12345678,"contentType":"application/pdf","overwrite":false}
```

The response contains `method`, `uploadUrl`, fixed `headers`, `completeUrl`,
`statusUrl`, `uploadedSize`, and an expiry. A local URL points back to
file-server. A GCS URL is a bearer-like resumable session URI and must not be
logged or shared.

## 2. Upload bytes

Split the file into chunks. For every chunk, send `PUT <uploadUrl>` with the
returned fixed headers and a chunk-specific range:

```http
Content-Range: bytes 8388608-16777215/33554432
```

`start` must equal the latest storage-confirmed `uploadedSize`. Local uploads
return `308 Resume Incomplete` until the final chunk and expose a committed
`Range: bytes=0-N` response header. GCS uses the same native `308`/`Range`
semantics. A final local chunk returns `200`; a final GCS chunk returns `200` or
`201`. GCS chunks travel directly from the browser to Cloud Storage and do not
consume Cloud Run request bandwidth.

If a request is paused, disconnected, times out, or has an ambiguous response,
do not assume that the whole chunk was lost. Call `GET <statusUrl>` and resume
from its `uploadedSize`. The server queries the local staging file or GCS
resumable session rather than trusting a client-side counter. An offset mismatch
returns `409`; local responses also include the current committed `Range`.

The status response repeats `uploadUrl` and fixed `headers` for an active
session. This is intentional so an authenticated same-origin client can resume
after a page reload. Complete and expired sessions return empty capability
fields. Expired sessions have `status: "expired"`; further content or complete
requests return `410 Gone`.

## 3. Complete

Call `POST <completeUrl>` only after `uploadedSize` equals `size`. For local
storage, the server verifies the staging file and atomically renames it to the
final key. For GCS, it verifies that the direct-upload object exists and has the
declared size. It then conditionally publishes the logical mapping. Complete is
idempotent. An overwrite succeeds only if the destination still points to the
object observed when the session was created.

The physical key is the NFC-normalized relative logical path. Logical paths and
GCS object keys never begin with a slash; the logical root is the empty string.
Characters unsupported by portable Windows/Unix filenames, control characters,
and trailing dots or spaces are replaced with `_`. Windows device names are
prefixed with `_`. Empty segments, the reserved `_vfs-link*` first segment, and
keys over GCS's 1024-byte limit are rejected. Sanitization collisions are not
silently suffixed; the storage precondition reports a conflict.

GCS writes directly to that final key with a generation precondition. There is
no provisional object and no copy/move after verification. The opaque GCS
resumable session URI is persisted in protected metadata so a different server
instance can query or cancel it; it must never be logged. `DELETE <statusUrl>`
cancels the GCS session or deletes the local staging file. Pausing or aborting an
individual HTTP request does not cancel the upload session.

The default maximum is 50 GiB and the default session lifetime is 24 hours.
Clients should treat `409` on a chunk as an offset mismatch and refresh status;
`409` on create or complete is a path or conditional-update conflict.
