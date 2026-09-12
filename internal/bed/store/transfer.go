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
	"regexp"
	"sync"
	"time"

	"github.com/qiankunli/go-stdx/randx"
	"github.com/qiankunli/hostel/internal/bed/filesystem/bedfs"
	storesync "github.com/qiankunli/hostel/internal/bed/store/sync"
	"github.com/qiankunli/hostel/internal/tracing"
)

var (
	ErrTransferInvalid     = storesync.ErrTransferInvalid
	ErrTransferUnavailable = storesync.ErrTransferUnavailable
	ErrTransferConflict    = storesync.ErrTransferConflict
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

type TransferEndpoint = storesync.TransferEndpoint

type TransferRequest struct {
	ParentRef   string
	Sync        SyncKind
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
	Ref        string
	Sync       SyncKind
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

func (r TransferRequest) syncOptions() storesync.TransferOptions {
	return storesync.TransferOptions{Sync: r.Sync, Source: r.Source, Destination: r.Destination, ParentRef: r.ParentRef, Overwrite: r.Overwrite}
}

func validateTransfer(req TransferRequest) (TransferRequest, error) {
	// Operation validation belongs to the policy; task identity and lifetime belong here.
	options, err := storesync.ValidateTransfer(req.syncOptions())
	if err != nil {
		return req, err
	}
	req.Sync = options.Sync
	if !transferIDPattern.MatchString(req.ID) {
		return req, fmt.Errorf("%w: id must be 1-128 safe identifier characters", ErrTransferInvalid)
	}
	if req.Timeout == 0 {
		req.Timeout = DefaultTransferTimeout
	}
	if req.Timeout < time.Millisecond || req.Timeout > MaxTransferTimeout {
		return req, fmt.Errorf("%w: timeout must be within 1ms and 2h", ErrTransferInvalid)
	}
	return req, nil
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
	if active >= transferLimit {
		return Transfer{}, ErrTransferCapacity
	}
	s.mu.Lock()
	remote, err := s.remoteLocked(ctx)
	s.mu.Unlock()
	if err != nil {
		return Transfer{}, err
	}
	if req.Sync == SyncRestic {
		if err := s.restic.Available(ctx); err != nil {
			return Transfer{}, err
		}
	}
	fs, release, err := acquire()
	if err != nil {
		return Transfer{}, err
	}
	// Only accepted new work evicts history; retries above retain their original result.
	r.makeRoomLocked()
	transferCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), req.Timeout)
	run := &transferRun{request: req, status: Transfer{Sync: req.Sync, ID: req.ID, BedID: bedID, InstanceID: r.instanceID, State: TransferRunning, StartedAt: time.Now()}, cancel: cancel, done: make(chan struct{})}
	r.runs[key] = run
	tracing.InfoContext(ctx, "hostel transfer started", "bed", bedID, "transfer_id", req.ID, "source_type", req.Source.Type, "sync", req.Sync)
	go func() {
		completed := func(bytes int64) {
			r.mu.Lock()
			run.status.Files++
			run.status.Bytes += bytes
			r.mu.Unlock()
		}
		var ref string
		var err error
		if req.Sync == SyncRestic {
			var files, bytes int64
			ref, files, bytes, err = s.restic.Transfer(transferCtx, fs, req.syncOptions(), completed)
			if err == nil && req.Source.Type == "bed" {
				r.mu.Lock()
				run.status.Files = files
				run.status.Bytes = bytes
				r.mu.Unlock()
			}
		} else {
			err = storesync.Copy(transferCtx, remote, s.cfg.Prefix, fs, req.syncOptions(), completed)
		}
		// BedFS and its activity watermark are released before terminal publication.
		release()
		r.mu.Lock()
		run.status.Ref = ref
		run.status.State = TransferSucceeded
		if err != nil {
			run.status.State = TransferFailed
			run.status.Error = storesync.Failure(err)
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
		logTransferFinished(transferCtx, status, err)
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

// Running work is never evicted. Admission has already checked the active limit,
// so a full registry always contains a completed record we can remove.
func (r *transferRegistry) makeRoomLocked() {
	for len(r.runs) >= transferHistoryLimit {
		var oldestKey string
		var oldest *time.Time
		for key, run := range r.runs {
			finished := run.status.FinishedAt
			if finished != nil && (oldest == nil || finished.Before(*oldest)) {
				oldestKey, oldest = key, finished
			}
		}
		if oldest == nil {
			return
		}
		delete(r.runs, oldestKey)
	}
}

func logTransferFinished(ctx context.Context, status Transfer, err error) {
	args := []any{"bed", status.BedID, "transfer_id", status.ID, "state", status.State, "sync", status.Sync, "files", status.Files, "bytes", status.Bytes, "error", status.Error}
	var failure *storesync.OperationError
	if errors.As(err, &failure) {
		// Log only operation-relative paths and the public error category. Raw
		// filesystem/SDK errors may contain carrier paths or signed URLs.
		args = append(args, "stage", failure.Stage, "relative_path", failure.Relative)
	}
	tracing.InfoContext(ctx, "hostel transfer finished", args...)
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
