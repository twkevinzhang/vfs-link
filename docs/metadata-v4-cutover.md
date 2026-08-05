# GCS metadata v4 production cutover

Metadata v4 removes the normal mutation path's global tree lease. File paths
remain strongly consistent through GCS generation preconditions and scoped
resource coordination. Folder aggregates and global statistics are derived and
may lag by up to five seconds. Payload objects stay in the existing primary GCS
bucket and are not copied by the metadata migration.

This procedure is intentionally a maintenance-window migration. Never migrate
from an actively mutating v3 prefix, never import into a reused v4 prefix, and
never delete v3 during the rollback window.

## Fixed production parameters

Use the following values for the first production cutover:

```dotenv
METADATA_PREFIX=_vfs-link-v4
METADATA_SHARD_COUNT=64
METADATA_REDUCER_INTERVAL=2s
METADATA_MUTATION_MODE=scoped
```

`METADATA_SHARD_COUNT` is part of the v4 on-disk contract. Changing it after
the target prefix is initialized makes directory entries unreachable. `global`
is the emergency serialization fallback; v1-v3 reject `scoped`.

## Preflight and maintenance

1. Record the active Cloud Run image, revision, traffic allocation, metadata
   bucket, prefix, and Pub/Sub subscription state. Confirm the ipproxy image
   digest and compose environment as well. Do not print credential values.
2. Confirm `_vfs-link-v4/` does not exist in the metadata bucket. A failed or
   abandoned v4 import must use a fresh prefix/build; do not merge imports.
3. Deploy the current production image with `MAINTENANCE_MODE=true`, route all
   traffic to it, suspend the share-worker Pub/Sub subscription, and wait until
   in-flight upload/share/metadata operations reach a business terminal state.
4. Enable `MAINTENANCE_MODE=true` on ipproxy before taking the protocol test
   fixture snapshot. HTTP mutation requests will return `503`; stop FTP accepts
   at the proxy/firewall during the write-free snapshot because maintenance
   middleware covers HTTP and WebDAV, not FTP.
5. Run a v3 export/import dry-run from the operator host:

```bash
./metadata-migrate \
  --source-tree-driver=gcs \
  --source-tree-gcs-bucket=storage-403503-vfs-link-metadata \
  --source-tree-prefix=_vfs-link-v3 \
  --target-driver=gcs \
  --target-gcs-bucket=storage-403503-vfs-link-metadata \
  --target-prefix=_vfs-link-v4 \
  --target-shard-count=64 \
  --target-reducer-interval=2s \
  --target-mutation-mode=global \
  --dry-run
```

The dry-run must report the expected record/entity counts and source SHA-256.
It does not initialize or mutate the target prefix.

## Import and no-traffic validation

Keep the rollback journal outside an ephemeral container filesystem. It
contains bucket/prefix names and validation counts but no credentials.

```bash
./metadata-migrate \
  --source-tree-driver=gcs \
  --source-tree-gcs-bucket=storage-403503-vfs-link-metadata \
  --source-tree-prefix=_vfs-link-v3 \
  --target-driver=gcs \
  --target-gcs-bucket=storage-403503-vfs-link-metadata \
  --target-prefix=_vfs-link-v4 \
  --target-shard-count=64 \
  --target-reducer-interval=2s \
  --target-mutation-mode=global \
  --rollback-journal="$PWD/metadata-v4-rollback.json" \
  --yes
```

The journal is written with mode `0600` in `prepared` state before target
initialization and changed to `completed` only after post-import validation.
If the command exits while it remains `prepared`, keep maintenance enabled and
investigate the target; do not retry into the same prefix.

Build a Cloud Run no-traffic candidate with v4 settings. Start it in `global`
mode for read-only and single-client validation, then deploy the same image in
`scoped` mode for concurrency validation. Verify record counts, direct-child
listings, path lookups, trash, shares, active uploads/locks, thumbnail links,
root statistics, and physical-object references before assigning production
traffic.

## Traffic cutover and acceptance

1. Route 100% Cloud Run traffic to the v4 `scoped` revision while maintenance
   is still enabled.
2. Disable maintenance, resume Pub/Sub, and run the agreed GCP HTTP scenario:
   resumable upload, complete, immediate lookup/list, download, rename, and
   delete.
3. Deploy the identical content-addressed image to ipproxy. Its existing
   PostgreSQL metadata configuration is not a substitute for or dependency of
   the GCS v4 design; it is retained only as that host's current backend. Enable
   the temporary WebDAV test credential through the host credential store,
   never in this document or command history.
4. Run ipproxy HTTP plus FTP `mkdir/upload/rename/download/delete` and WebDAV
   `LOCK/PUT/MOVE/UNLOCK`. Restore the previous WebDAV enablement state and
   remove the test fixture afterward.
5. Run 12 clients at two metadata mutations per second for 30 minutes, then a
   48 mutations/second five-minute burst. Confirm no lost update, duplicate live
   path, missing path, or exhausted metadata-conflict retry. Confirm p95 below
   one second, p99 below two seconds, unexpected errors below 0.1%, and derived
   aggregates 99% converged within five seconds and 100% within 30 seconds.

Do not call deployment complete until both GCP and ipproxy protocol acceptance
passes. An HTTP `202`, process exit code, or successful image rollout is not a
business terminal status.

## Rollback

If a correctness or latency gate fails:

1. Re-enable maintenance and suspend Pub/Sub. Block new FTP mutations on
   ipproxy and wait for in-flight operations to terminate.
2. First fall back in place to a known v4 revision configured with
   `METADATA_PREFIX=_vfs-link-v4` and `METADATA_MUTATION_MODE=global`. This
   removes scoped concurrency without discarding mutations accepted after the
   cutover.
3. Restore the prior ipproxy image and compose environment only if the image
   change affected its protocol behavior. Its database is not rewritten by the
   GCS metadata migration.
4. Resume writes on v4/global only after it passes a read/list/download and
   same-path-conflict smoke test. Preserve `_vfs-link-v4/` and the rollback
   journal for investigation.
5. Reverting the metadata source to v3 is a separate disaster-recovery action,
   not the first rollback step. Keep maintenance enabled, produce and validate
   a reverse journal of every mutation accepted by v4 since the migration
   snapshot, then either replay those changes into a fresh recovery prefix or
   obtain an explicit data-loss decision. Only after record/path/physical
   reference reconciliation may traffic use `_vfs-link-v3` again.
6. Never automatically copy v4 metadata over v3, and never delete either prefix
   until the incident and data-retention decision are complete.
