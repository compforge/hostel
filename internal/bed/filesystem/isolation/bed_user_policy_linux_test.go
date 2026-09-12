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

//go:build linux

package isolation

import (
	"context"
	"github.com/qiankunli/hostel/internal/bed"
	"testing"

	"github.com/qiankunli/hostel/internal/bed/privilege"
)

func TestUIDIsolationSelectsPerBedAllocator(t *testing.T) {
	configured, err := privilege.NewBedUser(1000, 1000)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := privilege.NewManager(DescribeBedUser(&resolved{boundary: &uidIso{}}, configured), configured, 0, bed.NewOwners().Privilege, nil)
	if err != nil {
		t.Fatal(err)
	}
	a, err := acquireTestUser(manager, "a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := acquireTestUser(manager, "b")
	if err != nil {
		t.Fatal(err)
	}
	if a.UID() == b.UID() {
		t.Fatalf("sample beds resolved the same uid %d", a.UID())
	}
	for _, user := range []privilege.BedUser{a, b} {
		if user.UID() < uidBase || user.UID() >= uidBase+uidRange || user.GID() != user.UID() {
			t.Fatalf("resolved user = %d:%d", user.UID(), user.GID())
		}
	}
	report := DescribeBedUser(&resolved{boundary: &uidIso{}}, configured)
	if report.Strategy != "per_bed" || report.UIDMin != uidBase || report.UIDMax != uidBase+uidRange-1 {
		t.Fatalf("report = %+v", report)
	}
}

func acquireTestUser(manager *privilege.Manager, id string) (privilege.BedUser, error) {
	b := bed.New(id, "", bed.Spec{})
	err := manager.Prepare(context.Background(), b)
	return manager.User(b), err
}
