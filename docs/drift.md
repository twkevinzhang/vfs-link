# Storage drift

Storage drift is the intentional difference between a file's current logical
path and its stable physical object key. Rename and move operations continue to
change metadata only. The drift domain makes that difference visible and, when
explicitly enabled, can reconcile selected GCS objects to the current index.

Set `DRIFT_ENABLED=true` only for a trusted operator deployment. `GET
/api/drift` reads the last persisted snapshot. A full metadata/object scan runs
only when the operator requests `GET /api/drift?refresh=true`; ordinary page
loads, search, filters, and pagination do not rescan GCS.

The viewer classifies active and trash metadata plus unreferenced objects as:

- `aligned`
- `drifted`
- `object_missing`
- `size_mismatch`
- `target_conflict`
- `shared_object`
- `orphan_object`

Only active `drifted` and `shared_object` records are actionable. An operator
selects paths, creates an immutable plan, reviews its estimated USD range and
warnings, acknowledges the cost, and starts an idempotent action.

For a flat-namespace GCS bucket, reconciliation is checkpointed as:

```text
source generation check
        |
        v
conditional copy to absent target
        |
        v
size + checksum + action-marker verification
        |
        v
conditional metadata update
        |
        v
delete source generation only when no metadata reference remains
```

Plans reject an existing target and also reject two selected logical paths that
sanitize to the same target. Actions persist their lease and checkpoint so a
retry or another Cloud Run instance can resume without overwriting an unrelated
target. Custom source object metadata is preserved; action markers remain on
the reconciled object to make ambiguous copy retries safe.

The snapshot and plan estimates expose an auditable, storage-class-specific
breakdown. The minimum is data retrieval plus estimated Class A and Class B
operations. The maximum adds an object-age-based early-deletion upper bound:

```text
minimum = retrieval + Class A operations + Class B operations
maximum = minimum + early deletion upper bound
```

Each calculation row includes its units, list rate, formula, USD range, pricing
date, and storage class. The estimator currently assumes regional,
flat-namespace list pricing and links to the official
[Cloud Storage pricing](https://cloud.google.com/storage/pricing) and
[storage classes](https://cloud.google.com/storage/docs/storage-classes)
documentation.

It is not a bill. Soft-delete retention storage, taxes, free tiers, negotiated
pricing, network topology, bucket namespace, Autoclass, and later pricing
changes are called out but cannot all be bounded from the object listing.
Always review the estimate immediately before starting an action.
