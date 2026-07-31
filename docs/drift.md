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

The estimate includes regional Archive retrieval, estimated Class A/Class B
operations, and an object-age-based upper bound for Archive early deletion.
It is not a bill. Soft-delete retention storage, taxes, free tiers, negotiated
pricing, network topology, and later pricing changes are called out but not all
can be bounded from the object listing. Always review the plan immediately
before starting an action.
