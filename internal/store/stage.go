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
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// StageInStep is one externally observable part of preparing a Bed's local
// filesystem. It describes data movement only; the Bed manager owns lifecycle
// and readiness.
type StageInStep string

const (
	StageInInspectSnapshot StageInStep = "inspect_snapshot"
	StageInSelectSource    StageInStep = "select_source"
	StageInRestoreSnapshot StageInStep = "restore_snapshot"
)

// StageInSource records which copy supplied the local BedFS.
type StageInSource string

const (
	StageInFresh    StageInSource = "fresh"
	StageInLuggage  StageInSource = "luggage"
	StageInSnapshot StageInSource = "snapshot"
)

// StageInRequest contains the local facts needed to choose between an existing
// luggage copy and the durable snapshot. OnStep is synchronous and must not
// block; callers use it to project progress into Bed status.
type StageInRequest struct {
	BedID           string
	BedDir          string
	LocalPresent    bool
	LocalGeneration int64
	OnStep          func(StageInStep)
}

// StageInResult describes the copy that is ready for BedFS preparation.
type StageInResult struct {
	Source          StageInSource
	Snapshot        *SnapshotInfo
	Restored        bool
	RestoreDuration time.Duration
}

// StageInBedFS prepares a complete local Bed directory. A newer snapshot is
// restored into a sibling staging directory and atomically published, so a
// failed restore never replaces usable luggage with a partial tree.
func StageInBedFS(ctx context.Context, backend Store, request StageInRequest) (StageInResult, error) {
	step := func(value StageInStep) {
		if request.OnStep != nil {
			request.OnStep(value)
		}
	}
	step(StageInInspectSnapshot)
	snapshot, err := backend.Stat(ctx, request.BedID)
	if err != nil {
		return StageInResult{}, fmt.Errorf("inspect snapshot: %w", err)
	}

	step(StageInSelectSource)
	result := StageInResult{Source: StageInFresh, Snapshot: snapshot}
	if snapshot == nil {
		if request.LocalPresent {
			result.Source = StageInLuggage
			return result, nil
		}
		if err := os.MkdirAll(request.BedDir, 0o755); err != nil {
			return StageInResult{}, fmt.Errorf("create fresh bed directory: %w", err)
		}
		return result, nil
	}
	if request.LocalPresent && request.LocalGeneration >= snapshot.Generation {
		result.Source = StageInLuggage
		return result, nil
	}

	step(StageInRestoreSnapshot)
	result.Source = StageInSnapshot
	parent := filepath.Dir(request.BedDir)
	staging, err := os.MkdirTemp(parent, ".stage-in-"+request.BedID+"-")
	if err != nil {
		return StageInResult{}, fmt.Errorf("create staging directory: %w", err)
	}
	defer os.RemoveAll(staging)

	startedAt := time.Now()
	if err := backend.Restore(ctx, request.BedID, staging); err != nil {
		return StageInResult{}, fmt.Errorf("restore snapshot: %w", err)
	}
	result.Restored = true
	result.RestoreDuration = time.Since(startedAt)
	if err := publishStagedBed(request.BedDir, staging); err != nil {
		return StageInResult{}, err
	}
	return result, nil
}

func publishStagedBed(bedDir, staging string) error {
	parent := filepath.Dir(bedDir)
	oldDir := ""
	if _, err := os.Stat(bedDir); err == nil {
		reserved, reserveErr := os.MkdirTemp(parent, ".replaced-bed-")
		if reserveErr != nil {
			return fmt.Errorf("reserve replaced directory: %w", reserveErr)
		}
		if removeErr := os.Remove(reserved); removeErr != nil {
			return fmt.Errorf("release replaced directory reservation: %w", removeErr)
		}
		oldDir = reserved
		if renameErr := os.Rename(bedDir, oldDir); renameErr != nil {
			return fmt.Errorf("move stale luggage aside: %w", renameErr)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect local bed directory: %w", err)
	}

	if err := os.Rename(staging, bedDir); err != nil {
		if oldDir != "" {
			_ = os.Rename(oldDir, bedDir)
		}
		return fmt.Errorf("publish staged bed directory: %w", err)
	}
	if oldDir != "" {
		_ = os.RemoveAll(oldDir)
	}
	return nil
}
