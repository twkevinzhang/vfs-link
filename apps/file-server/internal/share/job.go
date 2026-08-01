package share

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

const JobVersion = 1

type Job struct {
	Version int    `json:"version"`
	ShareID string `json:"shareId"`
}

func (j Job) Validate() error {
	if j.Version != JobVersion {
		return fmt.Errorf("unsupported share job version: %d", j.Version)
	}
	if strings.TrimSpace(j.ShareID) == "" {
		return errors.New("shareId is required")
	}
	return nil
}

func (j Job) ID() string {
	return fmt.Sprintf("share:%s:v%d", j.ShareID, j.Version)
}

type Dispatcher interface {
	Dispatch(context.Context, Job) error
}

type Processor interface {
	ProcessShareJob(context.Context, Job) error
}

type permanentError struct{ error }

func Permanent(err error) error {
	if err == nil || IsPermanent(err) {
		return err
	}
	return permanentError{error: err}
}

func IsPermanent(err error) bool {
	var target permanentError
	return errors.As(err, &target)
}
