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

package isolation

import (
	"github.com/qiankunli/hostel/internal/bedfs"
	"github.com/qiankunli/hostel/internal/privilege"
)

const (
	uidBase  = 200000
	uidRange = 100000 // dedicated Bed users occupy 200000..299999
)

type bedUserProvider interface {
	bedUser(*bedfs.FS, privilege.BedUser) (privilege.BedUser, error)
}

// BedUserFor resolves the final user once during Bed initialization. Keeping
// the value on Environment makes command and session execution agree exactly.
func BedUserFor(files Isolator, fs *bedfs.FS, configured privilege.BedUser) (privilege.BedUser, error) {
	if provider, ok := files.(bedUserProvider); ok {
		return provider.bedUser(fs, configured)
	}
	return configured, nil
}

// DescribeBedUser reports the instance policy without inventing one concrete
// UID for the per-Bed strategy.
func DescribeBedUser(files Isolator, configured privilege.BedUser) privilege.BedUserReport {
	if provider, ok := files.(interface{ dedicatedBedUsers() bool }); ok && provider.dedicatedBedUsers() {
		return privilege.BedUserReport{Strategy: "per_bed", UIDMin: uidBase, UIDMax: uidBase + uidRange - 1}
	}
	return privilege.BedUserReport{Strategy: "fixed", UID: configured.UID(), GID: configured.GID()}
}
