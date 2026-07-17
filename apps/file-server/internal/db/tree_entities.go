package db

import (
	"context"
	"encoding/json"
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
	if e := s.putEntity(ctx, "shares", r.ID, r, true); e != nil {
		return ShareRecord{}, e
	}
	return r, nil
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
	return s.updateShare(ctx, id, func(r *ShareRecord) { n := time.Now().UTC(); r.Status = "completed"; r.Error = ""; r.CompletedAt = &n })
}
func (s *TreeStore) MarkShareNotified(ctx context.Context, id string) (ShareRecord, error) {
	return s.updateShare(ctx, id, func(r *ShareRecord) { n := time.Now().UTC(); r.Status = "notified"; r.Error = ""; r.NotifiedAt = &n })
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
		claimed = true
	})
	return r, claimed, e
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
func (s *TreeStore) UpdateUpload(ctx context.Context, r UploadRecord) (UploadRecord, error) {
	var current UploadRecord
	g, ok, e := s.getEntityGeneration(ctx, "uploads", r.ID, &current)
	if e != nil {
		return r, e
	}
	if !ok {
		return r, ErrNotFound
	}
	r.UpdatedAt = time.Now().UTC()
	e = s.putEntityCAS(ctx, "uploads", r.ID, r, g)
	return r, e
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
