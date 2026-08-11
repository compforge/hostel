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
	"os"
	"path/filepath"
	"testing"
)

type stageTestStore struct {
	snapshot     *SnapshotInfo
	restoreErr   error
	restoreCalls int
}

func (*stageTestStore) Name() string { return "test" }

func (s *stageTestStore) Stat(context.Context, string) (*SnapshotInfo, error) {
	return s.snapshot, nil
}

func (s *stageTestStore) Restore(_ context.Context, _ string, dir string) error {
	s.restoreCalls++
	workspace := filepath.Join(dir, "data", "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(workspace, "marker"), []byte("snapshot"), 0o644); err != nil {
		return err
	}
	return s.restoreErr
}

func (*stageTestStore) Persist(context.Context, string, string, int64) error { return nil }
func (*stageTestStore) Delete(context.Context, string) error                 { return nil }

func TestStageInBedFSUsesCurrentLuggage(t *testing.T) {
	bedDir := filepath.Join(t.TempDir(), "bed-1")
	if err := os.MkdirAll(bedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	backend := &stageTestStore{snapshot: &SnapshotInfo{Generation: 4}}

	result, err := StageInBedFS(t.Context(), backend, StageInRequest{
		BedID:           "bed-1",
		BedDir:          bedDir,
		LocalPresent:    true,
		LocalGeneration: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Source != StageInLuggage || result.Restored || backend.restoreCalls != 0 {
		t.Fatalf("stage-in = %+v, restore calls = %d", result, backend.restoreCalls)
	}
}

func TestStageInBedFSUsesLuggageWithoutDurableStore(t *testing.T) {
	bedDir := filepath.Join(t.TempDir(), "bed-1")
	if err := os.MkdirAll(bedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	backend := &stageTestStore{}

	result, err := StageInBedFS(t.Context(), backend, StageInRequest{
		BedID:        "bed-1",
		BedDir:       bedDir,
		LocalPresent: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Source != StageInLuggage || result.Restored || backend.restoreCalls != 0 {
		t.Fatalf("stage-in = %+v, restore calls = %d", result, backend.restoreCalls)
	}
}

func TestStageInBedFSRestoreFailurePreservesLuggage(t *testing.T) {
	parent := t.TempDir()
	bedDir := filepath.Join(parent, "bed-1")
	workspace := filepath.Join(bedDir, "data", "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(workspace, "marker")
	if err := os.WriteFile(marker, []byte("luggage"), 0o644); err != nil {
		t.Fatal(err)
	}
	backend := &stageTestStore{
		snapshot:   &SnapshotInfo{Generation: 5},
		restoreErr: errors.New("download interrupted"),
	}

	_, err := StageInBedFS(t.Context(), backend, StageInRequest{
		BedID:           "bed-1",
		BedDir:          bedDir,
		LocalPresent:    true,
		LocalGeneration: 4,
	})
	if err == nil {
		t.Fatal("restore failure = nil")
	}
	content, readErr := os.ReadFile(marker)
	if readErr != nil || string(content) != "luggage" {
		t.Fatalf("luggage after failed restore = %q, %v", content, readErr)
	}
	if staging, _ := filepath.Glob(filepath.Join(parent, ".stage-in-bed-1-*")); len(staging) != 0 {
		t.Fatalf("staging directories leaked: %v", staging)
	}
}

func TestStageInBedFSPublishesCompleteSnapshot(t *testing.T) {
	parent := t.TempDir()
	bedDir := filepath.Join(parent, "bed-1")
	workspace := filepath.Join(bedDir, "data", "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "old-only"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	backend := &stageTestStore{snapshot: &SnapshotInfo{Generation: 5}}
	var steps []StageInStep

	result, err := StageInBedFS(t.Context(), backend, StageInRequest{
		BedID:           "bed-1",
		BedDir:          bedDir,
		LocalPresent:    true,
		LocalGeneration: 4,
		OnStep:          func(step StageInStep) { steps = append(steps, step) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Source != StageInSnapshot || !result.Restored || backend.restoreCalls != 1 {
		t.Fatalf("stage-in = %+v, restore calls = %d", result, backend.restoreCalls)
	}
	if got := len(steps); got != 3 || steps[0] != StageInInspectSnapshot ||
		steps[1] != StageInSelectSource || steps[2] != StageInRestoreSnapshot {
		t.Fatalf("steps = %v", steps)
	}
	if content, err := os.ReadFile(filepath.Join(workspace, "marker")); err != nil || string(content) != "snapshot" {
		t.Fatalf("published marker = %q, %v", content, err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "old-only")); !os.IsNotExist(err) {
		t.Fatalf("old luggage survived publish: %v", err)
	}
}
