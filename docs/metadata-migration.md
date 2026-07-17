# Distributed metadata tree v1 to v2 migration

The production source of truth is the distributed tree under `_vfs-link`, not
the historical monolithic `metadata.json`. Tree v2 stores rebuilt, deepest-first
directory indexes with persisted subtree summaries. Migrate into the new
`_vfs-link-v2` prefix and leave `_vfs-link` untouched for rollback.

## Safety sequence

1. Enter a write-free maintenance window. If the active revision supports it,
   deploy `MAINTENANCE_MODE=true`, suspend the Pub/Sub push subscription, and
   wait for in-flight requests and operations to finish. Otherwise suspend the
   subscription and temporarily remove the Cloud Run `allUsers` invoker binding.
2. Run the tree export dry-run. Export takes the distributed mutation lease and
   refuses a source with pending or running operations. Maintenance is still
   required so shares and upload sessions cannot change during export.
3. Bulk-import into the empty `_vfs-link-v2` prefix. The command rejects any
   non-empty target prefix; it never overwrites `_vfs-link`.
4. Verify record counts, bytes, IDs, trash, entities, index pages, and the root
   folder summary. The root summary must exactly equal active `stats.json`
   logical files, directories, and bytes.
5. Deploy a no-traffic candidate with `METADATA_PREFIX=_vfs-link-v2`. Test it
   through its tagged URL using Cloud Run authorization plus application Basic
   Auth. Test listing, folder summaries, upload, move, trash, restore, permanent
   deletion, sharing, and operation polling.
6. Route 100% traffic to the candidate, disable maintenance mode, restore the
   invoker binding when applicable, and resume Pub/Sub.

## Tree-to-tree migration

The source and target may be prefixes in the same Standard-class metadata
bucket. Always run the dry-run first:

```bash
./metadata-migrate \
  --source-tree-driver=gcs \
  --source-tree-gcs-bucket=PROJECT_ID-vfs-link-metadata \
  --source-tree-prefix=_vfs-link \
  --target-driver=gcs \
  --target-gcs-bucket=PROJECT_ID-vfs-link-metadata \
  --target-prefix=_vfs-link-v2 \
  --dry-run

./metadata-migrate \
  --source-tree-driver=gcs \
  --source-tree-gcs-bucket=PROJECT_ID-vfs-link-metadata \
  --source-tree-prefix=_vfs-link \
  --target-driver=gcs \
  --target-gcs-bucket=PROJECT_ID-vfs-link-metadata \
  --target-prefix=_vfs-link-v2 \
  --yes
```

The export includes active and trash records, shares, unexpired DAV locks and
uploads, and `nextFileId`. It produces a deterministic SHA-256 fingerprint.
Import preserves IDs, writes entities and indexes, publishes the completion
manifest last, and then verifies root folder aggregates against active stats.

For a local tree, use `--source-tree-driver=local`,
`--source-tree-local-root=...`, `--target-driver=local`, and
`--target-local-root=...`. Source and target may share the root because their
prefixes differ.

## Rollback

Do not delete or mutate `_vfs-link` during the v2 rollout. To roll back, restore
the previous Cloud Run image/configuration with `METADATA_PREFIX=_vfs-link`,
route traffic back, and resume writes only after the rollback revision is ready.
The incomplete or rejected `_vfs-link-v2` prefix must not be reused; remove it
only after investigating and while the service is in maintenance.

## Historical monolithic JSON import

`--source-file` and `--source-gcs-bucket` remain available only for importing a
verified historical `metadata.json` backup into a fresh `_vfs-link-v2` target.
They are not suitable for the current production migration because they do not
contain changes made after the distributed tree went live. For a GCS object,
pass `--source-gcs-generation` to pin the reviewed generation.
