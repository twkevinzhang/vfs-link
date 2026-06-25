package blob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
)

type FallbackStore struct {
	primary  Store
	fallback Store
	logger   *slog.Logger
}

func NewFallback(primary Store, fallback Store, logger *slog.Logger) *FallbackStore {
	return &FallbackStore{
		primary:  primary,
		fallback: fallback,
		logger:   logger,
	}
}

func (s *FallbackStore) Close() error {
	err := s.primary.Close()
	if fallbackErr := s.fallback.Close(); err == nil {
		err = fallbackErr
	}
	return err
}

func (s *FallbackStore) Driver() string {
	return s.primary.Driver() + "+fallback-" + s.fallback.Driver()
}

func (s *FallbackStore) Root() string {
	return s.primary.Root()
}

func (s *FallbackStore) NewReader(ctx context.Context, physicalHash string) (io.ReadCloser, error) {
	reader, err := s.primary.NewReader(ctx, physicalHash)
	if err == nil {
		return reader, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	fallbackReader, fallbackErr := s.fallback.NewReader(ctx, physicalHash)
	if fallbackErr != nil {
		return nil, fmt.Errorf("open fallback object: %w", fallbackErr)
	}

	writer, writerErr := s.primary.NewWriter(ctx, physicalHash)
	if writerErr != nil {
		_ = fallbackReader.Close()
		return nil, fmt.Errorf("prepare local backfill: %w", writerErr)
	}

	if _, copyErr := io.Copy(writer, fallbackReader); copyErr != nil {
		_ = writer.Close()
		_ = fallbackReader.Close()
		return nil, fmt.Errorf("backfill local object: %w", copyErr)
	}
	if closeErr := fallbackReader.Close(); closeErr != nil {
		_ = writer.Close()
		return nil, fmt.Errorf("finish fallback read: %w", closeErr)
	}
	if closeErr := writer.Close(); closeErr != nil {
		return nil, fmt.Errorf("commit local backfill: %w", closeErr)
	}

	if s.logger != nil {
		s.logger.Info("backfilled legacy object", "physical", physicalHash, "fallback", s.fallback.Root())
	}

	return s.primary.NewReader(ctx, physicalHash)
}

func (s *FallbackStore) NewWriter(ctx context.Context, physicalHash string) (io.WriteCloser, error) {
	return s.primary.NewWriter(ctx, physicalHash)
}

func (s *FallbackStore) Delete(ctx context.Context, physicalHash string) error {
	return s.primary.Delete(ctx, physicalHash)
}

func (s *FallbackStore) List(ctx context.Context) ([]ObjectInfo, error) {
	return s.primary.List(ctx)
}
