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

//go:build !linux

package isolation

import (
	hostfacts "github.com/qiankunli/hostel/internal/host/facts"
)

// Non-Linux cannot provide a bwrap boundary. The domain resolver may choose
// direct, but that fallback must not be reported as bwrap being available.
func newBwrap(facts hostfacts.Snapshot, _ string) (Isolator, hostfacts.ProbeReport) {
	return unavailable{name: "bwrap", lvl: Private}, hostfacts.ProbeReport{
		ConfiguredPath: "bwrap",
		ResolvedPath:   facts.BwrapPath,
		Exists:         facts.BwrapPath != "",
		Executable:     facts.BwrapPath != "",
		Error:          "unsupported operating system",
	}
}
