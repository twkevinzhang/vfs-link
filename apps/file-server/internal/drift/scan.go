package drift

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/twkevinzhang/vfs-link/apps/file-server/internal/db"
)

const (
	ScanPhaseQueued   = "queued"
	ScanPhaseMetadata = "metadata"
	ScanPhaseObjects  = "objects"
	ScanPhaseSaving   = "saving"
	ScanPhaseComplete = "completed"
	ScanPhaseFailed   = "failed"
)

const scanLeaseDuration = 30 * time.Minute

type Scan struct {
	ID          string     `json:"id"`
	Status      string     `json:"status"`
	Phase       string     `json:"phase"`
	Error       string     `json:"error,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
	CompletedAt *time.Time `json:"completedAt,omitempty"`
	LeaseUntil  *time.Time `json:"leaseUntil,omitempty"`
	Version     int64      `json:"-"`
}

func (s Scan) NeedsKick(now time.Time) bool {
	return s.Status == "pending" || (s.Status == "running" && (s.LeaseUntil == nil || !s.LeaseUntil.After(now)))
}

func (s *Service) StartScan(ctx context.Context) (Scan, bool, error) {
	now := time.Now().UTC()
	scan := Scan{ID: "scan-" + uuid.NewString(), Status: "pending", Phase: ScanPhaseQueued, CreatedAt: now, UpdatedAt: now}
	payload, err := json.Marshal(scan)
	if err != nil {
		return Scan{}, false, err
	}
	record, created, err := s.state.StartDriftScan(ctx, db.DriftScanRecord{
		ID: scan.ID, Status: scan.Status, Phase: scan.Phase, Payload: payload,
		CreatedAt: scan.CreatedAt, UpdatedAt: scan.UpdatedAt,
	})
	if err != nil {
		return Scan{}, false, err
	}
	if err := json.Unmarshal(record.Payload, &scan); err != nil {
		return Scan{}, false, err
	}
	scan.Version = record.Version
	if scan.NeedsKick(now) {
		s.KickScan(scan.ID)
	}
	return scan, created, nil
}

func (s *Service) GetScan(ctx context.Context) (Scan, bool, error) {
	record, ok, err := s.state.FindDriftScan(ctx)
	if err != nil || !ok {
		return Scan{}, ok, err
	}
	var scan Scan
	if err := json.Unmarshal(record.Payload, &scan); err != nil {
		return Scan{}, false, err
	}
	scan.Version = record.Version
	return scan, true, nil
}

func (s *Service) KickScan(id string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
		defer cancel()
		_, _ = s.ResumeScan(ctx, id)
	}()
}

func (s *Service) ResumeScan(ctx context.Context, id string) (Scan, error) {
	scan, ok, err := s.GetScan(ctx)
	if err != nil || !ok || scan.ID != id {
		return scan, err
	}
	if scan.Status == "completed" || scan.Status == "failed" {
		return scan, nil
	}
	now := time.Now().UTC()
	if scan.Status == "running" && scan.LeaseUntil != nil && scan.LeaseUntil.After(now) {
		return scan, nil
	}
	scan.Status = "running"
	scan.Phase = ScanPhaseMetadata
	scan.Error = ""
	lease := now.Add(scanLeaseDuration)
	scan.LeaseUntil = &lease
	scan, err = s.saveScan(ctx, scan)
	if errors.Is(err, db.ErrDriftStateConflict) {
		current, _, getErr := s.GetScan(ctx)
		return current, getErr
	}
	if err != nil {
		return scan, err
	}

	_, err = s.refresh(ctx, func(phase string) error {
		if scan.Phase == phase {
			return nil
		}
		scan.Phase = phase
		lease := time.Now().UTC().Add(scanLeaseDuration)
		scan.LeaseUntil = &lease
		var saveErr error
		scan, saveErr = s.saveScan(ctx, scan)
		return saveErr
	})
	if err != nil {
		return s.failScan(ctx, scan, err), nil
	}
	now = time.Now().UTC()
	scan.Status = "completed"
	scan.Phase = ScanPhaseComplete
	scan.CompletedAt = &now
	scan.LeaseUntil = nil
	scan, err = s.saveScan(ctx, scan)
	return scan, err
}

func (s *Service) saveScan(ctx context.Context, scan Scan) (Scan, error) {
	expected := scan.Version
	scan.UpdatedAt = time.Now().UTC()
	payload, err := json.Marshal(scan)
	if err != nil {
		return scan, err
	}
	record, err := s.state.UpdateDriftScan(ctx, db.DriftScanRecord{
		ID: scan.ID, Status: scan.Status, Phase: scan.Phase, Payload: payload,
		CreatedAt: scan.CreatedAt, UpdatedAt: scan.UpdatedAt,
	}, expected)
	if err != nil {
		return scan, err
	}
	scan.Version = record.Version
	return scan, nil
}

func (s *Service) failScan(ctx context.Context, scan Scan, cause error) Scan {
	now := time.Now().UTC()
	scan.Status = "failed"
	scan.Phase = ScanPhaseFailed
	scan.Error = cause.Error()
	scan.CompletedAt = &now
	scan.LeaseUntil = nil
	saved, err := s.saveScan(ctx, scan)
	if err != nil {
		scan.Error = fmt.Sprintf("%s; persist failure: %v", scan.Error, err)
		return scan
	}
	return saved
}
