// Copyright 2026 Li Qiankun
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/aws/smithy-go"
	"github.com/qiankunli/go-stdx/randx"
	"github.com/qiankunli/hostel/internal/bedfs"
	"github.com/qiankunli/hostel/internal/tracing"
)

var (
	ErrTransferInvalid     = errors.New("invalid transfer")
	ErrTransferUnavailable = errors.New("S3 transfer unavailable")
	ErrTransferConflict    = errors.New("transfer conflict")
	ErrTransferNotFound    = errors.New("transfer unknown or expired")
	ErrTransferCapacity    = errors.New("transfer capacity exceeded")
)

const (
	transferLimit          = 4
	transferHistoryLimit   = 1024
	transferRetention      = 24 * time.Hour
	DefaultTransferTimeout = 5 * time.Minute
	MaxTransferTimeout     = 2 * time.Hour
)

var transferIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type TransferEndpoint struct {
	Type string
	Path string
	Key  string
}

type TransferRequest struct {
	ID          string
	InstanceID  string
	Source      TransferEndpoint
	Destination TransferEndpoint
	Overwrite   bool
	Timeout     time.Duration
}

type TransferState string

const (
	TransferRunning   TransferState = "running"
	TransferSucceeded TransferState = "succeeded"
	TransferFailed    TransferState = "failed"
	TransferCanceled  TransferState = "canceled"
)

// Transfer is an instance-local observation, not a durable business record.
// Completed counters include only fully published files, never in-flight bytes.
type Transfer struct {
	ID         string
	BedID      string
	InstanceID string
	State      TransferState
	Files      int64
	Bytes      int64
	Error      string
	StartedAt  time.Time
	FinishedAt *time.Time
}

type transferRun struct {
	request TransferRequest
	status  Transfer
	cancel  context.CancelFunc
	done    chan struct{}
}

type transferRegistry struct {
	mu         sync.Mutex
	instanceID string
	runs       map[string]*transferRun
	closed     bool
}

func newTransferRegistry() *transferRegistry {
	return &transferRegistry{instanceID: "transfer-" + randx.Hex(16), runs: make(map[string]*transferRun)}
}

func (s *Manager) TransferInstanceID() string { return s.transfers.instanceID }
func (s *Manager) TransfersConfigured() bool  { return s.cfg.Bucket != "" }

func validateTransfer(req TransferRequest) (TransferRequest, error) {
	invalid := func(message string) (TransferRequest, error) {
		return req, fmt.Errorf("%w: %s", ErrTransferInvalid, message)
	}
	if !transferIDPattern.MatchString(req.ID) {
		return invalid("id must be 1-128 safe identifier characters")
	}
	if req.Timeout == 0 {
		req.Timeout = DefaultTransferTimeout
	}
	if req.Timeout < time.Millisecond || req.Timeout > MaxTransferTimeout {
		return invalid("timeout must be within 1ms and 2h")
	}
	if req.Source.Type == req.Destination.Type {
		return invalid("one endpoint must be bed and the other s3")
	}
	for _, endpoint := range []TransferEndpoint{req.Source, req.Destination} {
		switch endpoint.Type {
		case "bed":
			if endpoint.Path == "" || endpoint.Key != "" {
				return invalid("bed endpoint requires path only")
			}
		case "s3":
			if endpoint.Path != "" || !validTransferKey(endpoint.Key) {
				return invalid("s3 endpoint requires a relative key without empty, dot or parent segments")
			}
		default:
			return invalid("endpoint type must be bed or s3")
		}
	}
	return req, nil
}

func validTransferKey(key string) bool {
	if key == "" || strings.ContainsAny(key, "\\\x00") {
		return false
	}
	for _, part := range strings.Split(strings.TrimSuffix(key, "/"), "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

// StartTransfer admits a complete copy independently of the initiating HTTP
// request. acquire pins the selected Bed only for a new operation, under the
// registry lock, so teardown cannot miss an admitted but unregistered transfer.
// +spec=`Transfers copy files without changing Bed persistence identity; noop disables automatic persistence only.`
func (s *Manager) StartTransfer(ctx context.Context, bedID string, req TransferRequest, acquire func() (*bedfs.FS, func(), error)) (Transfer, error) {
	req, err := validateTransfer(req)
	if err != nil {
		return Transfer{}, err
	}
	r := s.transfers
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return Transfer{}, ErrTransferUnavailable
	}
	if req.InstanceID != "" && req.InstanceID != r.instanceID {
		return Transfer{}, fmt.Errorf("%w: instance changed", ErrTransferConflict)
	}
	// Normalize an omitted guard so guarded retries compare the actual operation.
	req.InstanceID = r.instanceID
	key := bedID + "\x00" + req.ID
	r.pruneLocked()
	if run := r.runs[key]; run != nil {
		if run.request != req {
			return Transfer{}, ErrTransferConflict
		}
		return run.status, nil
	}
	active := 0
	for _, run := range r.runs {
		if run.status.State == TransferRunning {
			active++
		}
	}
	// Reject admission rather than evict history inside its advertised retry window.
	if active >= transferLimit || len(r.runs) >= transferHistoryLimit {
		return Transfer{}, ErrTransferCapacity
	}
	s.mu.Lock()
	remote, err := s.remoteLocked(ctx)
	s.mu.Unlock()
	if err != nil {
		return Transfer{}, err
	}
	fs, release, err := acquire()
	if err != nil {
		return Transfer{}, err
	}
	transferCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), req.Timeout)
	run := &transferRun{request: req, status: Transfer{ID: req.ID, BedID: bedID, InstanceID: r.instanceID, State: TransferRunning, StartedAt: time.Now()}, cancel: cancel, done: make(chan struct{})}
	r.runs[key] = run
	tracing.InfoContext(ctx, "hostel transfer started", "bed", bedID, "transfer_id", req.ID, "source_type", req.Source.Type)
	go func() {
		err := copyTransfer(transferCtx, remote, path.Join(s.cfg.Prefix, ".transfers"), fs, req, func(bytes int64) {
			r.mu.Lock()
			run.status.Files++
			run.status.Bytes += bytes
			r.mu.Unlock()
		})
		// BedFS and its activity watermark are released before terminal publication.
		release()
		r.mu.Lock()
		run.status.State = TransferSucceeded
		if err != nil {
			run.status.State = TransferFailed
			run.status.Error = transferFailure(err)
			if errors.Is(err, context.Canceled) {
				run.status.State = TransferCanceled
			}
		}
		finished := time.Now()
		run.status.FinishedAt = &finished
		status := run.status
		cancel()
		run.cancel = nil // Do not retain the initiating request context with history.
		close(run.done)
		r.mu.Unlock()
		tracing.InfoContext(transferCtx, "hostel transfer finished", "bed", bedID, "transfer_id", req.ID, "state", status.State, "files", status.Files, "bytes", status.Bytes, "error", status.Error)
	}()
	return run.status, nil
}

func (r *transferRegistry) pruneLocked() {
	for key, run := range r.runs {
		if run.status.FinishedAt != nil && time.Since(*run.status.FinishedAt) >= transferRetention {
			delete(r.runs, key)
		}
	}
}

// TransferStatus and CancelTransfer never create or reinitialize a Bed.
func (s *Manager) TransferStatus(bedID, id string) (Transfer, error) {
	r := s.transfers
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneLocked()
	if run := r.runs[bedID+"\x00"+id]; run != nil {
		return run.status, nil
	}
	return Transfer{}, ErrTransferNotFound
}

func (s *Manager) CancelTransfer(bedID, id, instanceID string) (Transfer, error) {
	r := s.transfers
	r.mu.Lock()
	defer r.mu.Unlock()
	if instanceID != "" && instanceID != r.instanceID {
		return Transfer{}, fmt.Errorf("%w: instance changed", ErrTransferConflict)
	}
	r.pruneLocked()
	run := r.runs[bedID+"\x00"+id]
	if run == nil {
		return Transfer{}, ErrTransferNotFound
	}
	if run.cancel != nil {
		run.cancel()
	}
	return run.status, nil
}

// StopTransfers joins active copies before BedFS teardown. An empty bedID
// closes admission for the daemon. A timed-out join must leave BedFS intact.
func (s *Manager) StopTransfers(ctx context.Context, bedID string) error {
	r := s.transfers
	r.mu.Lock()
	if bedID == "" {
		r.closed = true
	}
	var pending []<-chan struct{}
	for _, run := range r.runs {
		if run.status.State == TransferRunning && (bedID == "" || run.status.BedID == bedID) {
			run.cancel()
			pending = append(pending, run.done)
		}
	}
	r.mu.Unlock()
	for _, done := range pending {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// Return actionable categories without exposing SDK URLs, credentials or
// carrier paths from wrapped transport and filesystem errors.
func transferFailure(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline exceeded"
	case errors.Is(err, ErrTransferConflict):
		return "destination already exists"
	case errors.Is(err, ErrTransferInvalid):
		return "unsupported file or object path"
	case errors.Is(err, os.ErrNotExist):
		return "source or destination parent not found"
	case errors.Is(err, os.ErrPermission):
		return "file access denied"
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "AccessDenied", "InvalidAccessKeyId", "SignatureDoesNotMatch":
			return "S3 access denied"
		case "NoSuchKey", "NoSuchBucket", "NotFound":
			return "S3 source or bucket not found"
		}
		return "S3 request failed"
	}
	return "copy failed"
}
