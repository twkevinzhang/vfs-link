package db

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/api/googleapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestRunHedgedTreeWriteKeepsFastPathSingleRequest(t *testing.T) {
	var calls atomic.Int32
	generation, err := runHedgedTreeWrite(context.Background(), time.Second, func(context.Context) (int64, error) {
		calls.Add(1)
		return 42, nil
	})
	if err != nil || generation != 42 || calls.Load() != 1 {
		t.Fatalf("generation=%d calls=%d err=%v", generation, calls.Load(), err)
	}
}

func TestRunHedgedTreeWriteUsesFirstSuccessfulGeneration(t *testing.T) {
	var calls atomic.Int32
	primaryRelease := make(chan struct{})
	generation, err := runHedgedTreeWrite(context.Background(), time.Millisecond, func(context.Context) (int64, error) {
		if calls.Add(1) == 1 {
			<-primaryRelease
			return 0, errors.New("slow primary failed")
		}
		return 84, nil
	})
	close(primaryRelease)
	if err != nil || generation != 84 || calls.Load() != 2 {
		t.Fatalf("generation=%d calls=%d err=%v", generation, calls.Load(), err)
	}
}

func TestRunHedgedTreeWriteReturnsErrorWhenBothAttemptsFail(t *testing.T) {
	want := errors.New("write failed")
	_, err := runHedgedTreeWrite(context.Background(), time.Millisecond, func(context.Context) (int64, error) {
		time.Sleep(2 * time.Millisecond)
		return 0, want
	})
	if !errors.Is(err, want) {
		t.Fatalf("error=%v, want %v", err, want)
	}
}

func TestClassifyGCSSaveErrorRecognizesTransportPreconditions(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "JSON conflict", err: &googleapi.Error{Code: 409}},
		{name: "JSON precondition", err: &googleapi.Error{Code: 412}},
		{name: "gRPC already exists", err: status.Error(codes.AlreadyExists, "exists")},
		{name: "gRPC precondition", err: status.Error(codes.FailedPrecondition, "generation changed")},
		{name: "wrapped gRPC precondition", err: fmt.Errorf("close writer: %w", status.Error(codes.FailedPrecondition, "generation changed"))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := classifyGCSSaveError(test.err); !errors.Is(got, ErrMetadataConflict) {
				t.Fatalf("classifyGCSSaveError() = %v, want ErrMetadataConflict", got)
			}
		})
	}
}

func TestClassifyGCSSaveErrorPreservesNonConflict(t *testing.T) {
	err := status.Error(codes.ResourceExhausted, "quota")
	got := classifyGCSSaveError(err)
	if errors.Is(got, ErrMetadataConflict) {
		t.Fatalf("classifyGCSSaveError() = %v, unexpectedly classified as conflict", got)
	}
	if !errors.Is(got, err) {
		t.Fatalf("classifyGCSSaveError() = %v, want wrapped source error", got)
	}
}
