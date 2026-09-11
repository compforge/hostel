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

package sync

// Kind selects how files are synchronized and organized remotely.
// Bed Sync controls automatic persistence; Transfer Sync selects one explicit operation.
type Kind string

const (
	KindNoop   Kind = "noop"
	KindAuto   Kind = "auto"
	KindCAS    Kind = "cas"
	KindPack   Kind = "pack"
	KindTar    Kind = "tar"
	KindRestic Kind = "restic"
	KindCopy   Kind = "copy" // Explicit file transfers only.
)
