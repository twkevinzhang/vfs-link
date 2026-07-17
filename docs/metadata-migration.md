# Legacy JSON metadata migration

The file-server runtime does not read the legacy monolithic
`_vfs-link/metadata.json`. Use `metadata-migrate` once to create the tree
layout, then keep the source object or local file only as an offline backup.

## Safety sequence

1. Suspend the Pub/Sub push subscription, then temporarily remove the Cloud Run
   `allUsers` invoker binding. This creates a full maintenance window for a
   production revision that predates `MAINTENANCE_MODE`.
2. Wait for in-flight requests to finish. Pin and back up the live legacy GCS
   object generation, then run a dry-run against that exact generation.
3. Migrate into a new, empty Standard-class metadata bucket in the same region
   as Cloud Run.
4. Verify file/directory counts, total bytes, IDs, directory indexes, and a
   sample of physical objects.
5. Deploy the tree-enabled revision with `--no-traffic`. Smoke-test it through
   its tagged URL using both `X-Serverless-Authorization` and application Basic
   Auth. Test listing, upload, move, trash, restore, and permanent deletion.
6. Route 100% traffic to the verified revision, restore the `allUsers` invoker
   binding, and resume the Pub/Sub subscription.

`MAINTENANCE_MODE=true` remains useful for future migrations after every active
revision includes that middleware: downloads and other GET/HEAD requests remain
available, while uploads, moves, trash operations, and Pub/Sub pushes return
`503`. It cannot make an older revision read-only when that revision does not
recognize the variable, and a tree revision must never be started against an
empty target merely to obtain the middleware.

Live GCS source, pinned to the generation observed when the command starts:

```bash
./metadata-migrate \
  --source-gcs-bucket=PROJECT_ID-archive \
  --source-gcs-object=_vfs-link/metadata.json \
  --target-driver=gcs \
  --target-gcs-bucket=PROJECT_ID-vfs-link-metadata \
  --dry-run

./metadata-migrate \
  --source-gcs-bucket=PROJECT_ID-archive \
  --source-gcs-object=_vfs-link/metadata.json \
  --target-driver=gcs \
  --target-gcs-bucket=PROJECT_ID-vfs-link-metadata \
  --yes
```

To guarantee a previously reviewed generation, pass
`--source-gcs-generation=GENERATION`. A local backup is also accepted:

```bash
./metadata-migrate \
  --source-file=./data/archive-metadata.json \
  --target-driver=local \
  --target-local-root=./data/metadata \
  --dry-run
```

The command refuses a non-empty target, validates the legacy schema, unique
IDs, active logical paths, sizes and physical references, imports tree records
and entities, then validates the resulting tree. The migration manifest records
the pinned source GCS generation (when applicable), source SHA-256, legacy
`nextFileId`, counts, bytes, ID range, and completion status. Expired DAV locks
and upload sessions are intentionally skipped because they are ephemeral and
unusable.

There is no runtime legacy fallback. Rollback means deploying the previous
container image with its backed-up `metadata.json`; do not point a new tree
revision at the legacy object.
