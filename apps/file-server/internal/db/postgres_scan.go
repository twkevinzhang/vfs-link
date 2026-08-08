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
	)
	return record, err
}

func scanUpload(row rowScanner) (UploadRecord, error) {
	var record UploadRecord
	err := row.Scan(&record.ID, &record.LogicPath, &record.PhysicalHash, &record.Driver,
		&record.ContentType, &record.UploadURL, &record.Size, &record.UploadedSize, &record.Overwrite,
		&record.ExpectedPhysicalHash, &record.RequireAbsent, &record.Status, &record.Error,
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
