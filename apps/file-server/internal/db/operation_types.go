package db

import "time"

// OperationType is the stable string representation persisted in tree JSON
// operation manifests and exposed through OperationRecord JSON.
type OperationType string

const (
	OperationTypeMove        OperationType = "move"
	OperationTypeRename      OperationType = "rename"
	OperationTypeTrash       OperationType = "trash"
	OperationTypeRestore     OperationType = "restore"
	OperationTypeDeleteTrash OperationType = "delete-trash"
)

func (operationType OperationType) Valid() bool {
	switch operationType {
	case OperationTypeMove, OperationTypeRename, OperationTypeTrash, OperationTypeRestore, OperationTypeDeleteTrash:
		return true
	default:
		return false
	}
}

func (operationType OperationType) SupportedByTreeV4() bool {
	switch operationType {
	case OperationTypeMove, OperationTypeRename, OperationTypeTrash, OperationTypeRestore:
		return true
	default:
		return false
	}
}

// OperationStatus is the stable string representation persisted in tree JSON
// operation manifests and exposed through OperationRecord JSON.
type OperationStatus string

const (
	OperationStatusPending   OperationStatus = "pending"
	OperationStatusRunning   OperationStatus = "running"
	OperationStatusCompleted OperationStatus = "completed"
	OperationStatusFailed    OperationStatus = "failed"
)

func (status OperationStatus) Valid() bool {
	switch status {
	case OperationStatusPending, OperationStatusRunning, OperationStatusCompleted, OperationStatusFailed:
		return true
	default:
		return false
	}
}

func (status OperationStatus) Terminal() bool {
	return status == OperationStatusCompleted || status == OperationStatusFailed
}

func (status OperationStatus) Runnable() bool {
	return status == OperationStatusPending || status == OperationStatusRunning
}

// CanTransitionTo describes transitions common to the current durable
// operation runners. An unknown legacy status may still start so decoding an
// old manifest does not become a new rejection boundary.
func (status OperationStatus) CanTransitionTo(next OperationStatus) bool {
	if !status.Valid() {
		return next == OperationStatusRunning
	}
	switch status {
	case OperationStatusPending:
		return next == OperationStatusRunning
	case OperationStatusRunning:
		return next == OperationStatusRunning || next == OperationStatusPending || next == OperationStatusCompleted || next == OperationStatusFailed
	default:
		return false
	}
}

func (operation OperationRecord) HasActiveLease(now time.Time) bool {
	return operation.Status == OperationStatusRunning &&
		operation.LeaseUntil != nil && operation.LeaseUntil.After(now)
}

// NeedsKick preserves the service's recovery behavior: pending operations are
// always kicked, while running operations are kicked only after their lease
// expires or when the lease is missing.
func (operation OperationRecord) NeedsKick(now time.Time) bool {
	return operation.Status == OperationStatusPending ||
		(operation.Status == OperationStatusRunning && !operation.HasActiveLease(now))
}
