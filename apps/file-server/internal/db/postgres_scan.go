package db

type rowScanner interface {
	Scan(dest ...any) error
}

func scanFile(row rowScanner) (FileRecord, error) {
	var record FileRecord
	err := row.Scan(
		&record.ID,
		&record.LogicPath,
		&record.PhysicalHash,
		&record.Size,
		&record.IsDirectory,
		&record.UpdatedAt,
	)
	return record, err
}

func scanShare(row rowScanner) (ShareRecord, error) {
	var record ShareRecord
	err := row.Scan(
		&record.ID,
		&record.LogicPath,
		&record.PhysicalHash,
		&record.FileName,
		&record.Size,
		&record.DestinationObject,
		&record.ShareURL,
		&record.Email,
		&record.Status,
		&record.Error,
		&record.CreatedAt,
		&record.UpdatedAt,
		&record.CompletedAt,
		&record.NotifiedAt,
		&record.ProcessingBy,
		&record.ProcessingUntil,
		&record.DispatchStatus,
		&record.DispatchAttempts,
		&record.NextDispatchAt,
		&record.DispatchLeaseOwner,
		&record.DispatchLeaseUntil,
		&record.LastDispatchError,
		&record.StartRequestedAt,
	)
	return record, err
}

func scanUpload(row rowScanner) (UploadRecord, error) {
	var record UploadRecord
	err := row.Scan(&record.ID, &record.LogicPath, &record.PhysicalHash, &record.Driver,
		&record.ContentType, &record.UploadURL, &record.Size, &record.UploadedSize, &record.Overwrite,
		&record.ExpectedPhysicalHash, &record.ExpectedFileID, &record.ExpectedFileUpdatedAt,
		&record.RequireAbsent, &record.Status, &record.Error,
		&record.Revision, &record.CompletionStatus, &record.CompletionOwner, &record.CompletionLeaseUntil,
		&record.CompletionAttempts, &record.CompletionNextAttemptAt, &record.FinalizedAt,
		&record.PublishedAt, &record.CompletedAt, &record.ObjectGeneration, &record.ObjectChecksum,
		&record.LastCompletionError, &record.CancelRequestedAt, &record.CancelledAt,
		&record.CleanupStatus, &record.PreviousPhysicalHash, &record.CleanupError,
		&record.CreatedAt, &record.UpdatedAt, &record.ExpiresAt)
	return record, err
}

func scanDAVLock(row rowScanner) (DAVLockRecord, error) {
	var record DAVLockRecord
	err := row.Scan(
		&record.Token,
		&record.Path,
		&record.Owner,
		&record.Depth,
		&record.ExpiresAt,
		&record.CreatedAt,
		&record.HeldBy,
		&record.HeldUntil,
	)
	return record, err
}
