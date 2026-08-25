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

import "context"

// Noop is the no-configuration backend. It deliberately satisfies the full
// Store contract so callers need no persistence-specific branches; future
// local-only behavior belongs here rather than in the auto router.
type Noop struct{}

func (Noop) Name() string                                         { return "noop" }
func (Noop) Stat(context.Context, string) (*SnapshotInfo, error)  { return nil, nil }
func (Noop) Restore(context.Context, string, string) error        { return nil }
func (Noop) Persist(context.Context, string, string, int64) error { return nil }
func (Noop) Delete(context.Context, string) error                 { return nil }
