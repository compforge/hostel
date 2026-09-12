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

package bedfs

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

// PathProjection gives one BedFS subtree a stable path in the Executor. It is
// deliberately business-neutral: callers choose both the BedFS path and the
// process path; Hostel only validates and projects them.
type PathProjection struct {
	BedPath     string
	ProcessPath string
}

// NewPathProjection validates and normalizes one projection. Workspace is a
// built-in Hostel contract, so configured projections must remain disjoint
// from it. Host-passthrough kernel trees are kept unavailable, matching
// pathshim's safety boundary.
func NewPathProjection(bedPath, processPath string) (PathProjection, error) {
	bedPath = path.Clean(strings.TrimSpace(bedPath))
	processPath = path.Clean(strings.TrimSpace(processPath))
	if !path.IsAbs(bedPath) || bedPath == "/" {
		return PathProjection{}, fmt.Errorf("bed path %q must be an absolute non-root path", bedPath)
	}
	if !path.IsAbs(processPath) || processPath == "/" {
		return PathProjection{}, fmt.Errorf("process path %q must be an absolute non-root path", processPath)
	}
	if pathsOverlap(bedPath, WorkspacePath) || pathsOverlap(processPath, WorkspacePath) {
		return PathProjection{}, fmt.Errorf("projection %q=%q overlaps built-in %s", bedPath, processPath, WorkspacePath)
	}
	for _, reserved := range []string{"/dev", "/proc", "/sys"} {
		if pathsOverlap(processPath, reserved) {
			return PathProjection{}, fmt.Errorf("process path %q overlaps reserved path %s", processPath, reserved)
		}
	}
	return PathProjection{BedPath: bedPath, ProcessPath: processPath}, nil
}

// ValidatePathProjections rejects ambiguous nested mappings. Treating the
// complete set atomically keeps pathshim and mount views from exposing subtly
// different precedence rules.
func ValidatePathProjections(projections []PathProjection) error {
	for i, current := range projections {
		for _, previous := range projections[:i] {
			if pathsOverlap(current.BedPath, previous.BedPath) {
				return fmt.Errorf("bed paths %q and %q overlap", previous.BedPath, current.BedPath)
			}
			if pathsOverlap(current.ProcessPath, previous.ProcessPath) {
				return fmt.Errorf("process paths %q and %q overlap", previous.ProcessPath, current.ProcessPath)
			}
		}
	}
	return nil
}

// CarrierPath returns the per-Bed source directory for this projection.
func (p PathProjection) CarrierPath(bedHome string) string {
	return filepath.Join(bedHome, filepath.FromSlash(strings.TrimPrefix(p.BedPath, "/")))
}

func pathsOverlap(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}
