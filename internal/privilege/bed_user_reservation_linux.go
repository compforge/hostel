//go:build linux

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

package privilege

import (
	"os"
	"syscall"
)

// ReserveOwnedDirectory protects one cold Bed tree from UID reuse after daemon
// restart. The caller resolves workspace layout names to the owning Bed ID.
func (a *BedUserAllocator) ReserveOwnedDirectory(bedID, dataDir string) error {
	if a == nil || a.fixed != nil {
		return nil
	}
	info, err := os.Stat(dataDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.reserveLocked(bedID, int(stat.Uid))
}
