package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

type davMutationLease struct {
	Owner string    `json:"owner"`
	Until time.Time `json:"until"`
}

func (s *TreeStore) acquireDAVMutationLease(ctx context.Context) (func(), error) {
	key := s.prefix + "/entities/dav-control/mutation.json"
	owner := uuid.NewString()
	for attempt := 0; attempt < treeCASAttempts*4; attempt++ {
		o, ok, e := s.objects.Get(ctx, key)
		if e != nil {
			return nil, e
		}
		lease := davMutationLease{Owner: owner, Until: time.Now().UTC().Add(time.Minute)}
		b, _ := marshalTree(lease)
		g := o.Generation
		if !ok {
			g = 0
		} else {
			var current davMutationLease
			if e = json.Unmarshal(o.Data, &current); e != nil {
				return nil, e
			}
			if current.Until.After(time.Now()) {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(10 * time.Millisecond):
					continue
				}
			}
		}
		newGen, e := s.objects.Put(ctx, key, b, &g)
		if e == nil {
			return func() { _ = s.objects.Delete(context.Background(), key, &newGen) }, nil
		}
		if !errorsIsConflict(e) {
			return nil, e
		}
	}
	return nil, ErrMetadataConflict
}

func (s *TreeStore) entityKey(kind, id string) string {
	return s.prefix + "/entities/" + kind + "/" + encodeTreeSegment(id) + ".json"
}
func (s *TreeStore) putEntity(ctx context.Context, kind, id string, value any, absent bool) error {
	b, e := marshalTree(value)
	if e != nil {
		return e
	}
	var expected *int64
	if absent {
		z := int64(0)
		expected = &z
	}
	_, e = s.objects.Put(ctx, s.entityKey(kind, id), b, expected)
	return e
}
func (s *TreeStore) getEntity(ctx context.Context, kind, id string, out any) (bool, error) {
	_, ok, e := s.getEntityGeneration(ctx, kind, id, out)
	return ok, e
}
func (s *TreeStore) getEntityGeneration(ctx context.Context, kind, id string, out any) (int64, bool, error) {
	o, ok, e := s.objects.Get(ctx, s.entityKey(kind, id))
	if e != nil || !ok {
		return 0, ok, e
	}
	return o.Generation, true, json.Unmarshal(o.Data, out)
}
func (s *TreeStore) putEntityCAS(ctx context.Context, kind, id string, value any, g int64) error {
	b, e := marshalTree(value)
	if e != nil {
		return e
	}
	_, e = s.objects.Put(ctx, s.entityKey(kind, id), b, &g)
	return e
}
func (s *TreeStore) listEntities(ctx context.Context, kind string, newValue func() any) ([]any, error) {
	keys, e := s.objects.List(ctx, s.prefix+"/entities/"+kind+"/")
	if e != nil {
		return nil, e
	}
	out := make([]any, 0, len(keys))
	for _, key := range keys {
		o, ok, e := s.objects.Get(ctx, key)
		if e != nil {
			return nil, e
		}
		if !ok {
			continue
		}
		v := newValue()
		if e = json.Unmarshal(o.Data, v); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, nil
}

func (s *TreeStore) CreateShare(ctx context.Context, r ShareRecord) (ShareRecord, error) {
	now := time.Now().UTC()
	if r.CreatedAt.IsZero() {
		r.CreatedAt = now
	}
	r.UpdatedAt = now
	if strings.TrimSpace(r.DispatchStatus) == "" {
		r.DispatchStatus = "none"
	}
	if e := s.putEntity(ctx, "shares", r.ID, r, true); e != nil {
		return ShareRecord{}, e
	}
	return r, nil
}

// CreateShareFromSnapshot publishes the reference first, then revalidates the
// active mapping. If publication won concurrently, cleanup either observes the
// temporary reference or the stale draft removes itself and returns conflict;
// no successful durable Share can point at the replaced generation.
func (s *TreeStore) CreateShareFromSnapshot(ctx context.Context, r ShareRecord) (ShareRecord, error) {
	created, err := s.CreateShare(ctx, r)
	if err != nil {
		return ShareRecord{}, err
	}
	current, found, findErr := s.Find(ctx, r.LogicPath)
	if findErr == nil && found && !current.IsDirectory && current.PhysicalHash == r.PhysicalHash {
		return created, nil
	}
	var stored ShareRecord
	generation, exists, deleteErr := s.getEntityGeneration(ctx, "shares", r.ID, &stored)
	if deleteErr == nil && exists {
		deleteErr = s.objects.Delete(ctx, s.entityKey("shares", r.ID), &generation)
	}
	if findErr != nil {
		return ShareRecord{}, errors.Join(ErrMetadataConflict, findErr, deleteErr)
	}
	return ShareRecord{}, errors.Join(ErrMetadataConflict, deleteErr)
}
func (s *TreeStore) FindShare(ctx context.Context, id string) (ShareRecord, bool, error) {
	var r ShareRecord
	ok, e := s.getEntity(ctx, "shares", id, &r)
	return r, ok, e
}
func (s *TreeStore) updateShare(ctx context.Context, id string, fn func(*ShareRecord)) (ShareRecord, error) {
	for n := 0; n < treeCASAttempts; n++ {
		var r ShareRecord
		g, ok, e := s.getEntityGeneration(ctx, "shares", id, &r)
		if e != nil {
			return r, e
		}
		if !ok {
			return r, ErrNotFound
		}
		fn(&r)
		r.UpdatedAt = time.Now().UTC()
		if e = s.putEntityCAS(ctx, "shares", id, r, g); e == nil {
			return r, nil
		} else if !errorsIsConflict(e) {
			return r, e
		}
	}
	return ShareRecord{}, ErrMetadataConflict
}

func (s *TreeStore) updateShareIf(ctx context.Context, id string, fn func(*ShareRecord) bool) (ShareRecord, bool, error) {
	for n := 0; n < treeCASAttempts; n++ {
		var r ShareRecord
		g, ok, err := s.getEntityGeneration(ctx, "shares", id, &r)
		if err != nil {
			return r, false, err
		}
		if !ok {
			return r, false, ErrNotFound
		}
		if !fn(&r) {
			return r, false, nil
		}
		r.UpdatedAt = time.Now().UTC()
		if err = s.putEntityCAS(ctx, "shares", id, r, g); err == nil {
			return r, true, nil
		} else if !errorsIsConflict(err) {
			return r, false, err
		}
	}
	return ShareRecord{}, false, ErrMetadataConflict
}

func (s *TreeStore) RequestShareJob(ctx context.Context, id, target string, now time.Time) (ShareRecord, bool, error) {
	dispatchNeeded := false
	r, _, err := s.updateShareIf(ctx, id, func(r *ShareRecord) bool {
		if r.Status == "notified" {
			return false
		}
		r.Email = target
		manualRetry := r.DispatchStatus == "dispatch_failed" || r.DispatchStatus == "dispatch_paused"
		if r.StartRequestedAt == nil || manualRetry {
			requested := now
			r.StartRequestedAt = &requested
		}
		if manualRetry {
			r.DispatchAttempts = 0
		}
		r.LastDispatchError = ""
		dispatchNeeded = r.ProcessingUntil == nil || !r.ProcessingUntil.After(now)
		if dispatchNeeded {
			next := now
			r.DispatchStatus = "pending"
			r.NextDispatchAt = &next
		}
		return true
	})
	return r, dispatchNeeded, err
}

func (s *TreeStore) ClaimPendingShareDispatch(ctx context.Context, owner string, now, until time.Time, limit int) ([]ShareRecord, error) {
	if strings.TrimSpace(owner) == "" || !until.After(now) || limit <= 0 {
		return nil, fmt.Errorf("valid dispatch owner, lease, and limit are required")
	}
	values, err := s.listEntities(ctx, "shares", func() any { return &ShareRecord{} })
	if err != nil {
		return nil, err
	}
	candidates := make([]ShareRecord, 0, len(values))
	for _, value := range values {
		r := *value.(*ShareRecord)
		if shareDispatchDue(r, now) {
			candidates = append(candidates, r)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		left, right := candidates[i].NextDispatchAt, candidates[j].NextDispatchAt
		if left == nil || right == nil {
			return left == nil && right != nil
		}
		return left.Before(*right)
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	claimed := make([]ShareRecord, 0, len(candidates))
	for _, candidate := range candidates {
		r, ok, claimErr := s.updateShareIf(ctx, candidate.ID, func(r *ShareRecord) bool {
			if !shareDispatchDue(*r, now) {
				return false
			}
			r.DispatchStatus = "dispatching"
			r.DispatchAttempts++
			r.DispatchLeaseOwner = &owner
			r.DispatchLeaseUntil = &until
			return true
		})
		if claimErr != nil {
			return nil, claimErr
		}
		if ok {
			claimed = append(claimed, r)
		}
	}
	return claimed, nil
}

func shareDispatchDue(r ShareRecord, now time.Time) bool {
	if r.Status == "notified" || (r.ProcessingUntil != nil && r.ProcessingUntil.After(now)) ||
		(r.NextDispatchAt != nil && r.NextDispatchAt.After(now)) {
		return false
	}
	switch r.DispatchStatus {
	case "pending", "dispatched":
		return true
	case "dispatching":
		return r.DispatchLeaseUntil == nil || !r.DispatchLeaseUntil.After(now)
	default:
		return false
	}
}

func (s *TreeStore) MarkShareDispatched(ctx context.Context, id, owner string, redeliverAt time.Time) error {
	_, ok, err := s.updateShareIf(ctx, id, func(r *ShareRecord) bool {
		if (r.DispatchStatus != "dispatching" && r.DispatchStatus != "dispatch_paused") || r.DispatchLeaseOwner == nil || *r.DispatchLeaseOwner != owner {
			return false
		}
		paused := r.DispatchStatus == "dispatch_paused"
		if !paused {
			r.DispatchStatus = "dispatched"
		}
		if r.Status == "notified" || paused {
			r.NextDispatchAt = nil
		} else {
			r.NextDispatchAt = &redeliverAt
		}
		r.DispatchLeaseOwner = nil
		r.DispatchLeaseUntil = nil
		r.LastDispatchError = ""
		return true
	})
	return expectedMutation(ok, err)
}

func (s *TreeStore) RetryShareDispatch(ctx context.Context, id, owner string, next time.Time, message string) error {
	_, ok, err := s.updateShareIf(ctx, id, func(r *ShareRecord) bool {
		if r.DispatchStatus != "dispatching" || r.DispatchLeaseOwner == nil || *r.DispatchLeaseOwner != owner {
			return false
		}
		r.DispatchStatus = "pending"
		r.NextDispatchAt = &next
		r.DispatchLeaseOwner = nil
		r.DispatchLeaseUntil = nil
		r.LastDispatchError = message
		return true
	})
	return expectedMutation(ok, err)
}

func (s *TreeStore) FailShareDispatch(ctx context.Context, id, owner, message string) error {
	_, ok, err := s.updateShareIf(ctx, id, func(r *ShareRecord) bool {
		if r.DispatchStatus != "dispatching" || r.DispatchLeaseOwner == nil || *r.DispatchLeaseOwner != owner {
			return false
		}
		r.DispatchStatus = "dispatch_failed"
		r.NextDispatchAt = nil
		r.DispatchLeaseOwner = nil
		r.DispatchLeaseUntil = nil
		r.LastDispatchError = message
		return true
	})
	return expectedMutation(ok, err)
}

func expectedMutation(ok bool, err error) error {
	if err != nil {
		return err
	}
	if !ok {
		return ErrMetadataConflict
	}
	return nil
}
func (s *TreeStore) MarkShareUploading(ctx context.Context, id, target string) (ShareRecord, error) {
	return s.updateShare(ctx, id, func(r *ShareRecord) {
		r.Email = target
		r.Status = "uploading"
		r.Error = ""
		r.CompletedAt = nil
		r.NotifiedAt = nil
	})
}
func (s *TreeStore) MarkShareUploaded(ctx context.Context, id string) (ShareRecord, error) {
	return s.updateShare(ctx, id, func(r *ShareRecord) {
		n := time.Now().UTC()
		r.Status = "completed"
		r.Error = ""
		if r.CompletedAt == nil {
			r.CompletedAt = &n
		}
	})
}
func (s *TreeStore) MarkShareNotified(ctx context.Context, id string) (ShareRecord, error) {
	return s.updateShare(ctx, id, func(r *ShareRecord) {
		n := time.Now().UTC()
		r.Status = "notified"
		r.Error = ""
		if r.NotifiedAt == nil {
			r.NotifiedAt = &n
		}
		r.DispatchStatus = "dispatched"
		r.NextDispatchAt = nil
	})
}
func (s *TreeStore) MarkShareFailed(ctx context.Context, id, status, msg string) (ShareRecord, error) {
	return s.updateShare(ctx, id, func(r *ShareRecord) { r.Status = status; r.Error = msg })
}
func (s *TreeStore) ClaimShareJob(ctx context.Context, id, owner string, until time.Time) (ShareRecord, bool, error) {
	if strings.TrimSpace(owner) == "" || !until.After(time.Now()) {
		return ShareRecord{}, false, fmt.Errorf("valid share lease required")
	}
	claimed := false
	r, e := s.updateShare(ctx, id, func(r *ShareRecord) {
		claimed = false
		if r.Status == "notified" || (r.ProcessingUntil != nil && r.ProcessingUntil.After(time.Now()) && (r.ProcessingBy == nil || *r.ProcessingBy != owner)) {
			return
		}
		r.ProcessingBy = &owner
		r.ProcessingUntil = &until
		if r.CompletedAt == nil {
			r.Status = "uploading"
			r.Error = ""
		}
		claimed = true
	})
	return r, claimed, e
}

func (s *TreeStore) MarkShareUploadedBy(ctx context.Context, id, owner string) (ShareRecord, error) {
	r, ok, err := s.updateShareIf(ctx, id, func(r *ShareRecord) bool {
		if r.ProcessingBy == nil || *r.ProcessingBy != owner || r.Status != "uploading" {
			return false
		}
		now := time.Now().UTC()
		r.Status = "completed"
		r.Error = ""
		if r.CompletedAt == nil {
			r.CompletedAt = &now
		}
		return true
	})
	if err = expectedMutation(ok, err); err != nil {
		return ShareRecord{}, err
	}
	return r, nil
}

func (s *TreeStore) MarkShareNotifiedBy(ctx context.Context, id, owner string) (ShareRecord, error) {
	r, ok, err := s.updateShareIf(ctx, id, func(r *ShareRecord) bool {
		if r.ProcessingBy == nil || *r.ProcessingBy != owner || r.CompletedAt == nil || r.Status == "notified" {
			return false
		}
		now := time.Now().UTC()
		r.Status = "notified"
		r.Error = ""
		if r.NotifiedAt == nil {
			r.NotifiedAt = &now
		}
		r.NextDispatchAt = nil
		return true
	})
	if err = expectedMutation(ok, err); err != nil {
		return ShareRecord{}, err
	}
	return r, nil
}

func (s *TreeStore) MarkShareFailedBy(ctx context.Context, id, owner, status, message string) (ShareRecord, error) {
	r, ok, err := s.updateShareIf(ctx, id, func(r *ShareRecord) bool {
		if r.ProcessingBy == nil || *r.ProcessingBy != owner || r.Status == "notified" {
			return false
		}
		r.Status = status
		r.Error = message
		return true
	})
	if err = expectedMutation(ok, err); err != nil {
		return ShareRecord{}, err
	}
	return r, nil
}

func (s *TreeStore) StopShareRedelivery(ctx context.Context, id, owner string) error {
	_, ok, err := s.updateShareIf(ctx, id, func(r *ShareRecord) bool {
		if r.ProcessingBy == nil || *r.ProcessingBy != owner {
			return false
		}
		r.DispatchStatus = "dispatch_paused"
		r.NextDispatchAt = nil
		return true
	})
	return expectedMutation(ok, err)
}
func (s *TreeStore) ReleaseShareJob(ctx context.Context, id, owner string) error {
	_, e := s.updateShare(ctx, id, func(r *ShareRecord) {
		if r.ProcessingBy != nil && *r.ProcessingBy == owner {
			r.ProcessingBy = nil
			r.ProcessingUntil = nil
		}
	})
	return e
}

func (s *TreeStore) CreateUpload(ctx context.Context, r UploadRecord) (UploadRecord, error) {
	now := time.Now().UTC()
	if r.CreatedAt.IsZero() {
		r.CreatedAt = now
	}
	if r.UpdatedAt.IsZero() {
		r.UpdatedAt = now
	}
	if r.Revision <= 0 {
		r.Revision = 1
	}
	if strings.TrimSpace(r.CompletionStatus) == "" {
		r.CompletionStatus = "none"
	}
	if strings.TrimSpace(r.CleanupStatus) == "" {
		r.CleanupStatus = "none"
	}
	if e := s.putEntity(ctx, "uploads", r.ID, r, true); e != nil {
		return UploadRecord{}, e
	}
	return r, nil
}
func (s *TreeStore) FindUpload(ctx context.Context, id string) (UploadRecord, bool, error) {
	var r UploadRecord
	ok, e := s.getEntity(ctx, "uploads", id, &r)
	return r, ok, e
}

func (s *TreeStore) ListNonterminalUploads(ctx context.Context, limit int) ([]UploadRecord, error) {
	if limit <= 0 {
		return []UploadRecord{}, nil
	}
	values, err := s.listEntities(ctx, "uploads", func() any { return &UploadRecord{} })
	if err != nil {
		return nil, err
	}
	records := make([]UploadRecord, 0, min(limit, len(values)))
	for _, value := range values {
		record := *(value.(*UploadRecord))
		switch record.Status {
		case "pending", "uploading", "uploaded", "finalizing", "failed":
			records = append(records, record)
		}
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].CreatedAt.Equal(records[j].CreatedAt) {
			return records[i].ID < records[j].ID
		}
		return records[i].CreatedAt.Before(records[j].CreatedAt)
	})
	if len(records) > limit {
		records = records[:limit]
	}
	return records, nil
}

func (s *TreeStore) ListDueUploadRecoveries(ctx context.Context, now time.Time, limit int) ([]UploadRecord, error) {
	if limit <= 0 {
		return []UploadRecord{}, nil
	}
	values, err := s.listEntities(ctx, "uploads", func() any { return &UploadRecord{} })
	if err != nil {
		return nil, err
	}
	records := make([]UploadRecord, 0, min(limit, len(values)))
	for _, value := range values {
		record := *(value.(*UploadRecord))
		completionDue := (record.Status == "uploaded" || record.Status == "finalizing") &&
			(record.CompletionStatus == "pending" || record.CompletionStatus == "object_ready" || record.CompletionStatus == "published") &&
			(record.CompletionNextAttemptAt == nil || !record.CompletionNextAttemptAt.After(now)) &&
			(record.CompletionLeaseUntil == nil || !record.CompletionLeaseUntil.After(now))
		cleanupDue := record.Status == "complete" && record.CompletionStatus == "complete" && record.CleanupStatus == "pending"
		if completionDue || cleanupDue {
			records = append(records, record)
		}
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].UpdatedAt.Equal(records[j].UpdatedAt) {
			return records[i].ID < records[j].ID
		}
		return records[i].UpdatedAt.Before(records[j].UpdatedAt)
	})
	if len(records) > limit {
		records = records[:limit]
	}
	return records, nil
}
func (s *TreeStore) UpdateUpload(ctx context.Context, r UploadRecord) (UploadRecord, error) {
	updated, ok, err := s.UpdateUploadConditional(ctx, r, r.Revision)
	if err != nil {
		return UploadRecord{}, err
	}
	if !ok {
		return UploadRecord{}, ErrMetadataConflict
	}
	return updated, nil
}

func (s *TreeStore) UpdateUploadConditional(ctx context.Context, next UploadRecord, expectedRevision int64) (UploadRecord, bool, error) {
	return s.updateUploadIf(ctx, next.ID, func(current *UploadRecord) bool {
		if current.Revision != expectedRevision || normalizeCompletionStatus(current.CompletionStatus) != "none" ||
			isUploadTerminalOrTransitioning(current.Status) {
			return false
		}
		createdAt := current.CreatedAt
		*current = next
		current.CreatedAt = createdAt
		return true
	})
}

func (s *TreeStore) updateUploadIf(ctx context.Context, id string, fn func(*UploadRecord) bool) (UploadRecord, bool, error) {
	for attempt := 0; attempt < treeCASAttempts; attempt++ {
		var record UploadRecord
		generation, ok, err := s.getEntityGeneration(ctx, "uploads", id, &record)
		if err != nil {
			return UploadRecord{}, false, err
		}
		if !ok {
			return UploadRecord{}, false, ErrNotFound
		}
		if record.Revision <= 0 {
			record.Revision = 1
		}
		record.CompletionStatus = normalizeCompletionStatus(record.CompletionStatus)
		if !fn(&record) {
			return record, false, nil
		}
		record.Revision++
		record.UpdatedAt = time.Now().UTC()
		if err = s.putEntityCAS(ctx, "uploads", id, record, generation); err == nil {
			return record, true, nil
		} else if !errorsIsConflict(err) {
			return UploadRecord{}, false, err
		}
	}
	return UploadRecord{}, false, ErrMetadataConflict
}

func normalizeCompletionStatus(status string) string {
	if strings.TrimSpace(status) == "" {
		return "none"
	}
	return status
}

func isUploadTerminalOrTransitioning(status string) bool {
	switch status {
	case "complete", "conflict", "cancelling", "cancelled", "expired", "finalizing":
		return true
	default:
		return false
	}
}

func (s *TreeStore) RequestUploadCompletion(ctx context.Context, id string, now time.Time) (UploadRecord, bool, error) {
	record, changed, err := s.updateUploadIf(ctx, id, func(record *UploadRecord) bool {
		if record.Status != "uploaded" || (record.CompletionStatus != "none" && record.CompletionStatus != "retry") {
			return false
		}
		record.CompletionStatus = "pending"
		record.CompletionNextAttemptAt = &now
		record.LastCompletionError = ""
		return true
	})
	if err != nil {
		return UploadRecord{}, false, err
	}
	if changed {
		return record, true, nil
	}
	needed := record.Status != "complete" && record.Status != "cancelled" && record.CompletionStatus != "conflict"
	return record, needed, nil
}

func (s *TreeStore) ClaimUploadCompletion(ctx context.Context, id, owner string, now, until time.Time) (UploadRecord, bool, error) {
	if strings.TrimSpace(owner) == "" || !until.After(now) {
		return UploadRecord{}, false, fmt.Errorf("valid upload completion owner and lease are required")
	}
	return s.updateUploadIf(ctx, id, func(record *UploadRecord) bool {
		if record.Status != "uploaded" && record.Status != "finalizing" {
			return false
		}
		switch record.CompletionStatus {
		case "pending", "object_ready", "published":
		default:
			return false
		}
		if record.CompletionNextAttemptAt != nil && record.CompletionNextAttemptAt.After(now) {
			return false
		}
		if record.CompletionLeaseUntil != nil && record.CompletionLeaseUntil.After(now) &&
			(record.CompletionOwner == nil || *record.CompletionOwner != owner) {
			return false
		}
		record.Status = "finalizing"
		record.CompletionOwner = &owner
		record.CompletionLeaseUntil = &until
		record.CompletionAttempts++
		return true
	})
}

func (s *TreeStore) MarkUploadObjectReady(ctx context.Context, id, owner string, now time.Time) (UploadRecord, error) {
	return s.expectedUploadMutation(s.updateUploadIf(ctx, id, func(record *UploadRecord) bool {
		if !uploadOwnedAtCheckpoint(record, owner, "pending", "object_ready") {
			return false
		}
		record.CompletionStatus = "object_ready"
		if record.FinalizedAt == nil {
			record.FinalizedAt = &now
		}
		return true
	}))
}

func (s *TreeStore) MarkUploadPublished(ctx context.Context, id, owner, previousPhysicalHash, cleanupStatus, cleanupError string, now time.Time) (UploadRecord, error) {
	return s.expectedUploadMutation(s.updateUploadIf(ctx, id, func(record *UploadRecord) bool {
		if !uploadOwnedAtCheckpoint(record, owner, "object_ready", "published") {
			return false
		}
		record.CompletionStatus = "published"
		record.PreviousPhysicalHash = previousPhysicalHash
		record.CleanupStatus = cleanupStatus
		record.CleanupError = cleanupError
		if record.PublishedAt == nil {
			record.PublishedAt = &now
		}
		return true
	}))
}

func (s *TreeStore) MarkUploadComplete(ctx context.Context, id, owner string, now time.Time) (UploadRecord, error) {
	return s.expectedUploadMutation(s.updateUploadIf(ctx, id, func(record *UploadRecord) bool {
		if !uploadOwnedAtCheckpoint(record, owner, "published") {
			return false
		}
		record.Status = "complete"
		record.Error = ""
		record.CompletionStatus = "complete"
		record.CompletionOwner = nil
		record.CompletionLeaseUntil = nil
		record.CompletionNextAttemptAt = nil
		record.LastCompletionError = ""
		if record.CompletedAt == nil {
			record.CompletedAt = &now
		}
		if record.CleanupStatus == "none" || record.CleanupStatus == "" {
			record.CleanupStatus = "pending"
		}
		return true
	}))
}

func (s *TreeStore) MarkUploadCleanupComplete(ctx context.Context, id string, now time.Time) (UploadRecord, error) {
	return s.expectedUploadMutation(s.updateUploadIf(ctx, id, func(record *UploadRecord) bool {
		if record.Status != "complete" || record.CompletionStatus != "complete" || record.CleanupStatus != "pending" {
			return false
		}
		record.CleanupStatus = "complete"
		record.CleanupError = ""
		_ = now
		return true
	}))
}

func (s *TreeStore) RetryUploadCleanup(ctx context.Context, id, message string, now time.Time) (UploadRecord, error) {
	return s.expectedUploadMutation(s.updateUploadIf(ctx, id, func(record *UploadRecord) bool {
		if record.Status != "complete" || record.CompletionStatus != "complete" || record.CleanupStatus != "pending" {
			return false
		}
		record.CleanupError = message
		_ = now
		return true
	}))
}

func (s *TreeStore) RetryUploadCompletion(ctx context.Context, id, owner, message string, nextAttemptAt, now time.Time) (UploadRecord, error) {
	return s.expectedUploadMutation(s.updateUploadIf(ctx, id, func(record *UploadRecord) bool {
		if record.Status != "finalizing" || record.CompletionOwner == nil || *record.CompletionOwner != owner {
			return false
		}
		record.Status = "uploaded"
		record.CompletionStatus = "pending"
		record.CompletionOwner = nil
		record.CompletionLeaseUntil = nil
		record.CompletionNextAttemptAt = &nextAttemptAt
		record.LastCompletionError = message
		record.Error = message
		_ = now
		return true
	}))
}

func (s *TreeStore) MarkUploadCompletionConflict(ctx context.Context, id, owner, message string, now time.Time) (UploadRecord, error) {
	return s.expectedUploadMutation(s.updateUploadIf(ctx, id, func(record *UploadRecord) bool {
		if record.Status != "finalizing" || record.CompletionOwner == nil || *record.CompletionOwner != owner || record.CompletionStatus == "complete" {
			return false
		}
		record.Status = "conflict"
		record.CompletionStatus = "conflict"
		record.CompletionOwner = nil
		record.CompletionLeaseUntil = nil
		record.CompletionNextAttemptAt = nil
		record.LastCompletionError = message
		record.Error = message
		_ = now
		return true
	}))
}

func uploadOwnedAtCheckpoint(record *UploadRecord, owner string, checkpoints ...string) bool {
	if record.Status != "finalizing" || record.CompletionOwner == nil || *record.CompletionOwner != owner {
		return false
	}
	for _, checkpoint := range checkpoints {
		if record.CompletionStatus == checkpoint {
			return true
		}
	}
	return false
}

func (s *TreeStore) expectedUploadMutation(record UploadRecord, changed bool, err error) (UploadRecord, error) {
	if err != nil {
		return UploadRecord{}, err
	}
	if !changed {
		return UploadRecord{}, ErrMetadataConflict
	}
	return record, nil
}

func (s *TreeStore) RequestUploadCancel(ctx context.Context, id string, now time.Time) (UploadRecord, bool, error) {
	record, changed, err := s.updateUploadIf(ctx, id, func(record *UploadRecord) bool {
		switch record.Status {
		case "pending", "uploading", "uploaded", "failed", "expired":
		default:
			return false
		}
		if record.CompletionStatus != "none" && record.CompletionStatus != "pending" {
			return false
		}
		if record.CompletionOwner != nil {
			return false
		}
		record.Status = "cancelling"
		record.CompletionStatus = "cancel_requested"
		if record.CancelRequestedAt == nil {
			record.CancelRequestedAt = &now
		}
		return true
	})
	if err != nil {
		return UploadRecord{}, false, err
	}
	if changed {
		return record, true, nil
	}
	return record, record.Status == "cancelling", nil
}

func (s *TreeStore) MarkUploadCancelled(ctx context.Context, id string, now time.Time) (UploadRecord, error) {
	return s.expectedUploadMutation(s.updateUploadIf(ctx, id, func(record *UploadRecord) bool {
		if record.Status != "cancelling" || record.CompletionStatus != "cancel_requested" {
			return false
		}
		record.Status = "cancelled"
		record.CompletionStatus = "cancelled"
		record.Error = ""
		record.CompletionOwner = nil
		record.CompletionLeaseUntil = nil
		if record.CancelledAt == nil {
			record.CancelledAt = &now
		}
		return true
	}))
}

func (s *TreeStore) ExpireUpload(ctx context.Context, id string, expectedRevision int64, now time.Time) (UploadRecord, bool, error) {
	return s.updateUploadIf(ctx, id, func(record *UploadRecord) bool {
		if record.Revision != expectedRevision || record.ExpiresAt.After(now) || record.CompletionStatus != "none" {
			return false
		}
		switch record.Status {
		case "pending", "uploading", "uploaded", "failed":
			record.Status = "expired"
			record.Error = "upload session expired"
			return true
		default:
			return false
		}
	})
}
func (s *TreeStore) DeleteUpload(ctx context.Context, id string) (bool, error) {
	var r UploadRecord
	g, ok, e := s.getEntityGeneration(ctx, "uploads", id, &r)
	if e != nil || !ok {
		return ok, e
	}
	e = s.objects.Delete(ctx, s.entityKey("uploads", id), &g)
	return e == nil, e
}

func (s *TreeStore) CreateDAVLock(ctx context.Context, r DAVLockRecord) (DAVLockRecord, error) {
	r.Token = strings.TrimSpace(r.Token)
	r.Path = cleanDAVPath(r.Path)
	if r.Token == "" || (r.Depth != 0 && r.Depth != -1) || !r.ExpiresAt.After(time.Now()) {
		return r, fmt.Errorf("invalid DAV lock")
	}
	release, e := s.acquireDAVMutationLease(ctx)
	if e != nil {
		return r, e
	}
	defer release()
	locks, e := s.allDAVLocks(ctx)
	if e != nil {
		return r, e
	}
	for _, x := range locks {
		if x.ExpiresAt.After(time.Now()) && (x.Token == r.Token || davLocksConflict(x, r)) {
			return r, ErrDAVLockConflict
		}
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC()
	}
	e = s.putEntity(ctx, "dav-locks", r.Token, r, true)
	return r, e
}
func (s *TreeStore) FindDAVLock(ctx context.Context, token string) (DAVLockRecord, bool, error) {
	var r DAVLockRecord
	ok, e := s.getEntity(ctx, "dav-locks", strings.TrimSpace(token), &r)
	if ok && !r.ExpiresAt.After(time.Now()) {
		return DAVLockRecord{}, false, nil
	}
	return r, ok, e
}
func (s *TreeStore) allDAVLocks(ctx context.Context) ([]DAVLockRecord, error) {
	values, e := s.listEntities(ctx, "dav-locks", func() any { return &DAVLockRecord{} })
	if e != nil {
		return nil, e
	}
	out := make([]DAVLockRecord, 0, len(values))
	for _, v := range values {
		out = append(out, *v.(*DAVLockRecord))
	}
	return out, nil
}
func (s *TreeStore) ListActiveDAVLocks(ctx context.Context, p string) ([]DAVLockRecord, error) {
	locks, e := s.allDAVLocks(ctx)
	if e != nil {
		return nil, e
	}
	p = cleanDAVPath(p)
	out := locks[:0]
	for _, r := range locks {
		if (r.ExpiresAt.After(time.Now()) || (r.HeldUntil != nil && r.HeldUntil.After(time.Now()))) && (davLockCoversPath(r, p) || davPathIsAncestor(p, r.Path)) {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}
func (s *TreeStore) RefreshDAVLock(ctx context.Context, token string, expiry time.Time) (DAVLockRecord, bool, error) {
	release, leaseErr := s.acquireDAVMutationLease(ctx)
	if leaseErr != nil {
		return DAVLockRecord{}, false, leaseErr
	}
	defer release()
	var r DAVLockRecord
	g, ok, e := s.getEntityGeneration(ctx, "dav-locks", strings.TrimSpace(token), &r)
	if e != nil || !ok {
		return r, ok, e
	}
	if !expiry.After(time.Now()) {
		return r, false, fmt.Errorf("invalid DAV lock expiry")
	}
	r.ExpiresAt = expiry
	e = s.putEntityCAS(ctx, "dav-locks", r.Token, r, g)
	return r, e == nil, e
}
func (s *TreeStore) DeleteDAVLock(ctx context.Context, token string) (bool, error) {
	release, e := s.acquireDAVMutationLease(ctx)
	if e != nil {
		return false, e
	}
	defer release()
	var r DAVLockRecord
	g, ok, e := s.getEntityGeneration(ctx, "dav-locks", strings.TrimSpace(token), &r)
	if e != nil || !ok {
		return ok, e
	}
	if r.HeldUntil != nil && r.HeldUntil.After(time.Now()) {
		return false, nil
	}
	e = s.objects.Delete(ctx, s.entityKey("dav-locks", r.Token), &g)
	return e == nil, e
}
func (s *TreeStore) CleanupExpiredDAVLocks(ctx context.Context) (int64, error) {
	release, e := s.acquireDAVMutationLease(ctx)
	if e != nil {
		return 0, e
	}
	defer release()
	locks, e := s.allDAVLocks(ctx)
	if e != nil {
		return 0, e
	}
	var n int64
	for _, r := range locks {
		if !r.ExpiresAt.After(time.Now()) && (r.HeldUntil == nil || !r.HeldUntil.After(time.Now())) {
			var current DAVLockRecord
			g, ok, getErr := s.getEntityGeneration(ctx, "dav-locks", r.Token, &current)
			if getErr != nil {
				return n, getErr
			}
			if !ok {
				continue
			}
			if e = s.objects.Delete(ctx, s.entityKey("dav-locks", r.Token), &g); e != nil {
				return n, e
			}
			n++
		}
	}
	return n, nil
}
func (s *TreeStore) ClaimDAVLocks(ctx context.Context, paths, tokens []string, claim string, until time.Time) (bool, error) {
	release, e := s.acquireDAVMutationLease(ctx)
	if e != nil {
		return false, e
	}
	defer release()
	provided := map[string]bool{}
	for _, t := range tokens {
		provided[strings.TrimSpace(t)] = true
	}
	locks, e := s.allDAVLocks(ctx)
	if e != nil {
		return false, e
	}
	var affected []DAVLockRecord
	for _, r := range locks {
		for _, p := range paths {
			if r.ExpiresAt.After(time.Now()) && davLockCoversPath(r, cleanDAVPath(p)) {
				if !provided[r.Token] {
					return false, nil
				}
				if r.HeldUntil != nil && r.HeldUntil.After(time.Now()) && (r.HeldBy == nil || *r.HeldBy != claim) {
					return false, nil
				}
				affected = append(affected, r)
				break
			}
		}
	}
	for _, r := range affected {
		var current DAVLockRecord
		g, ok, getErr := s.getEntityGeneration(ctx, "dav-locks", r.Token, &current)
		if getErr != nil {
			return false, getErr
		}
		if !ok {
			return false, ErrNotFound
		}
		r = current
		r.HeldBy = &claim
		r.HeldUntil = &until
		if e = s.putEntityCAS(ctx, "dav-locks", r.Token, r, g); e != nil {
			return false, e
		}
	}
	return true, nil
}
func (s *TreeStore) ReleaseDAVLockClaim(ctx context.Context, claim string) error {
	release, e := s.acquireDAVMutationLease(ctx)
	if e != nil {
		return e
	}
	defer release()
	locks, e := s.allDAVLocks(ctx)
	if e != nil {
		return e
	}
	for _, r := range locks {
		if r.HeldBy != nil && *r.HeldBy == claim {
			var current DAVLockRecord
			g, ok, getErr := s.getEntityGeneration(ctx, "dav-locks", r.Token, &current)
			if getErr != nil {
				return getErr
			}
			if !ok {
				continue
			}
			r = current
			r.HeldBy = nil
			r.HeldUntil = nil
			if e = s.putEntityCAS(ctx, "dav-locks", r.Token, r, g); e != nil {
				return e
			}
		}
	}
	return nil
}
