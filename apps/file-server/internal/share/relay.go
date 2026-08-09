package share

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
)

const (
	defaultDispatchLease      = 30 * time.Second
	defaultRelayInterval      = 5 * time.Second
	defaultRedeliveryInterval = 5 * time.Minute
	defaultRelayBatch         = 25
	maximumDispatchAge        = 24 * time.Hour
)

type RelayOption func(*Relay)

func WithRelayClock(now func() time.Time) RelayOption {
	return func(relay *Relay) {
		if now != nil {
			relay.now = now
		}
	}
}

func WithRelayInterval(interval time.Duration) RelayOption {
	return func(relay *Relay) {
		if interval > 0 {
			relay.interval = interval
		}
	}
}

type Relay struct {
	store      MetadataStore
	dispatcher Dispatcher
	logger     *slog.Logger
	owner      string
	now        func() time.Time
	lease      time.Duration
	interval   time.Duration
	redelivery time.Duration
	batch      int
}

func NewRelay(store MetadataStore, dispatcher Dispatcher, logger *slog.Logger, options ...RelayOption) *Relay {
	if logger == nil {
		logger = slog.Default()
	}
	relay := &Relay{
		store: store, dispatcher: dispatcher, logger: logger, owner: uuid.NewString(),
		now: func() time.Time { return time.Now().UTC() }, lease: defaultDispatchLease,
		interval: defaultRelayInterval, redelivery: defaultRedeliveryInterval, batch: defaultRelayBatch,
	}
	for _, option := range options {
		option(relay)
	}
	return relay
}

func (r *Relay) Run(ctx context.Context) error {
	if err := r.RunOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
		r.logger.Error("share relay pass failed", "error", err)
	}
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := r.RunOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
				r.logger.Error("share relay pass failed", "error", err)
			}
		}
	}
}

func (r *Relay) RunOnce(ctx context.Context) error {
	now := r.now()
	records, err := r.store.ClaimPendingShareDispatch(ctx, r.owner, now, now.Add(r.lease), r.batch)
	if err != nil {
		return fmt.Errorf("claim pending share dispatch: %w", err)
	}
	var joined error
	for _, record := range records {
		if err := r.dispatchClaim(ctx, record); err != nil {
			joined = errors.Join(joined, err)
		}
	}
	return joined
}

// DispatchOne is the request fast path. Claims remain backend-atomic; if a
// concurrent relay wins, this call simply observes no work and returns.
func (r *Relay) DispatchOne(ctx context.Context, shareID string) error {
	now := r.now()
	records, err := r.store.ClaimPendingShareDispatch(ctx, r.owner, now, now.Add(r.lease), r.batch)
	if err != nil {
		return fmt.Errorf("claim share dispatch: %w", err)
	}
	var targetFound bool
	var joined error
	for _, record := range records {
		dispatchErr := r.dispatchClaim(ctx, record)
		if record.ID == shareID {
			targetFound = true
			joined = errors.Join(joined, dispatchErr)
		} else if dispatchErr != nil {
			r.logger.Error("share fast-path dispatched another pending job", "share_id", record.ID, "error", dispatchErr)
		}
	}
	if !targetFound {
		return nil
	}
	return joined
}

func (r *Relay) dispatchClaim(ctx context.Context, record db.ShareRecord) error {
	err := r.dispatcher.Dispatch(ctx, Job{Version: JobVersion, ShareID: record.ID})
	if err == nil {
		if markErr := r.store.MarkShareDispatched(ctx, record.ID, r.owner, r.now().Add(r.redelivery)); markErr != nil {
			return fmt.Errorf("mark share %s dispatched: %w", record.ID, markErr)
		}
		return nil
	}

	message := err.Error()
	if IsPermanent(err) || dispatchExpired(record, r.now()) {
		if markErr := r.store.FailShareDispatch(ctx, record.ID, r.owner, message); markErr != nil {
			return errors.Join(err, fmt.Errorf("mark permanent dispatch failure: %w", markErr))
		}
		return err
	}
	next := r.now().Add(dispatchBackoff(record.ID, record.DispatchAttempts))
	if retryErr := r.store.RetryShareDispatch(ctx, record.ID, r.owner, next, message); retryErr != nil {
		return errors.Join(err, fmt.Errorf("schedule dispatch retry: %w", retryErr))
	}
	return err
}

func dispatchExpired(record db.ShareRecord, now time.Time) bool {
	return record.StartRequestedAt != nil && !record.StartRequestedAt.IsZero() && now.Sub(*record.StartRequestedAt) >= maximumDispatchAge
}

func dispatchBackoff(id string, attempt int) time.Duration {
	steps := []time.Duration{0, 5 * time.Second, 30 * time.Second, 2 * time.Minute, 10 * time.Minute, 30 * time.Minute}
	index := attempt
	if index < 0 {
		index = 0
	}
	if index >= len(steps) {
		index = len(steps) - 1
	}
	base := steps[index]
	if base == 0 {
		return 0
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(fmt.Sprintf("%s:%d", id, attempt)))
	// Stable +/-20% jitter avoids synchronized retries without shared RNG state.
	percent := int(h.Sum32()%41) - 20
	return base + time.Duration(int64(base)*int64(percent)/100)
}
