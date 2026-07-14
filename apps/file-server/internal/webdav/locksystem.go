package webdav

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
	xwebdav "golang.org/x/net/webdav"
)

type LockSystem struct {
	store          lockStore
	defaultTimeout time.Duration
}

type lockStore interface {
	CreateDAVLock(context.Context, db.DAVLockRecord) (db.DAVLockRecord, error)
	RefreshDAVLock(context.Context, string, time.Time) (db.DAVLockRecord, bool, error)
	DeleteDAVLock(context.Context, string) (bool, error)
	ClaimDAVLocks(context.Context, []string, []string, string, time.Time) (bool, error)
	ReleaseDAVLockClaim(context.Context, string) error
}

func NewLockSystem(store lockStore, defaultTimeout time.Duration) *LockSystem {
	if defaultTimeout <= 0 {
		defaultTimeout = 30 * time.Minute
	}
	return &LockSystem{store: store, defaultTimeout: defaultTimeout}
}

func (ls *LockSystem) Confirm(now time.Time, name0, name1 string, conditions ...xwebdav.Condition) (func(), error) {
	paths := make([]string, 0, 2)
	if name0 != "" {
		paths = append(paths, cleanDAVPath(name0))
	}
	if name1 != "" {
		paths = append(paths, cleanDAVPath(name1))
	}
	tokens := make([]string, 0, len(conditions))
	for _, condition := range conditions {
		if !condition.Not && condition.Token != "" {
			tokens = append(tokens, condition.Token)
		}
	}
	claimID := uuid.NewString()
	ok, err := ls.store.ClaimDAVLocks(context.Background(), paths, tokens, claimID, now.Add(ls.defaultTimeout))
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, xwebdav.ErrConfirmationFailed
	}
	return func() {
		_ = ls.store.ReleaseDAVLockClaim(context.Background(), claimID)
	}, nil
}

func (ls *LockSystem) Create(now time.Time, details xwebdav.LockDetails) (string, error) {
	duration := details.Duration
	if duration <= 0 || duration > ls.defaultTimeout {
		duration = ls.defaultTimeout
	}
	depth := -1
	if details.ZeroDepth {
		depth = 0
	}
	token := "urn:uuid:" + uuid.NewString()
	_, err := ls.store.CreateDAVLock(context.Background(), db.DAVLockRecord{
		Token: token, Path: cleanDAVPath(details.Root), Owner: details.OwnerXML,
		Depth: depth, ExpiresAt: now.Add(duration),
	})
	if errors.Is(err, db.ErrDAVLockConflict) {
		return "", xwebdav.ErrLocked
	}
	if err != nil {
		return "", err
	}
	return token, nil
}

func (ls *LockSystem) Refresh(now time.Time, token string, duration time.Duration) (xwebdav.LockDetails, error) {
	if duration <= 0 || duration > ls.defaultTimeout {
		duration = ls.defaultTimeout
	}
	record, found, err := ls.store.RefreshDAVLock(context.Background(), token, now.Add(duration))
	if err != nil {
		return xwebdav.LockDetails{}, err
	}
	if !found {
		return xwebdav.LockDetails{}, xwebdav.ErrNoSuchLock
	}
	return detailsFromRecord(record, now), nil
}

func (ls *LockSystem) Unlock(_ time.Time, token string) error {
	deleted, err := ls.store.DeleteDAVLock(context.Background(), token)
	if err != nil {
		return err
	}
	if !deleted {
		return xwebdav.ErrNoSuchLock
	}
	return nil
}

func detailsFromRecord(record db.DAVLockRecord, now time.Time) xwebdav.LockDetails {
	duration := record.ExpiresAt.Sub(now)
	if duration < 0 {
		duration = 0
	}
	return xwebdav.LockDetails{
		Root: record.Path, Duration: duration, OwnerXML: record.Owner, ZeroDepth: record.Depth == 0,
	}
}

var _ xwebdav.LockSystem = (*LockSystem)(nil)
