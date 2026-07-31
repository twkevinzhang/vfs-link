package db

import "context"

const directoryAggregateVersion = 2

func folderSummaryFromIndex(idx directoryIndex) FolderSummary {
	return FolderSummary{
		Files:       idx.SubtreeFiles,
		Directories: idx.SubtreeDirs,
		Bytes:       idx.SubtreeBytes,
	}
}

func addFolderSummary(dst *FolderSummary, src FolderSummary) {
	dst.Files += src.Files
	dst.Directories += src.Directories
	dst.Bytes += src.Bytes
}

func recordFolderSummary(r FileRecord) FolderSummary {
	if !r.IsDirectory {
		return FolderSummary{Files: 1, Bytes: r.Size}
	}
	result := FolderSummary{Directories: 1}
	if r.FolderSummary != nil {
		addFolderSummary(&result, *r.FolderSummary)
	}
	return result
}

func summarizeIndexPage(records []FileRecord) indexPageDescriptor {
	var d indexPageDescriptor
	for _, r := range records {
		if !r.IsDirectory {
			d.DirectBytes += r.Size
		}
		summary := recordFolderSummary(r)
		d.SubtreeFiles += summary.Files
		d.SubtreeDirs += summary.Directories
		d.SubtreeBytes += summary.Bytes
	}
	return d
}

func summarizeIndexManifest(idx *directoryIndex) {
	idx.Version = 2
	idx.AggregateVersion = directoryAggregateVersion
	idx.DirectBytes = 0
	idx.SubtreeFiles = 0
	idx.SubtreeDirs = 0
	idx.SubtreeBytes = 0
	for _, page := range idx.Pages {
		idx.DirectBytes += page.DirectBytes
		idx.SubtreeFiles += page.SubtreeFiles
		idx.SubtreeDirs += page.SubtreeDirs
		idx.SubtreeBytes += page.SubtreeBytes
	}
	// TotalBytes remains the direct-file byte count for backward compatibility.
	idx.TotalBytes = idx.DirectBytes
}

func (s *TreeStore) hydrateDirectorySummaries(ctx context.Context, records []FileRecord) ([]FileRecord, error) {
	for i := range records {
		if !records[i].IsDirectory {
			records[i].FolderSummary = nil
			continue
		}
		idx, _, ok, err := s.getIndexManifest(ctx, records[i].LogicPath)
		if err != nil {
			return nil, err
		}
		summary := FolderSummary{}
		if ok {
			summary = folderSummaryFromIndex(idx)
		}
		records[i].FolderSummary = &summary
	}
	return records, nil
}

// propagateDirectorySummaryLeaseHeld publishes a directory's absolute
// aggregate into its parent entry, then repeats to root. Absolute values make
// retries idempotent after a partial propagation.
func (s *TreeStore) propagateDirectorySummaryLeaseHeld(ctx context.Context, dir string) error {
	dir = cleanLogicPath(dir)
	if dir == "" {
		return nil
	}
	idx, _, ok, err := s.getIndexManifest(ctx, dir)
	if err != nil {
		return err
	}
	summary := FolderSummary{}
	if ok {
		summary = folderSummaryFromIndex(idx)
	}
	record, found, err := s.Find(ctx, dir)
	if err != nil {
		return err
	}
	if !found {
		// A bulk operation may have already removed the directory marker. Its
		// source parent is updated explicitly by that operation.
		return nil
	}
	record.FolderSummary = &summary
	return s.updateIndexRecordLeaseHeld(ctx, parentLogicPath(dir), record, false, true)
}
