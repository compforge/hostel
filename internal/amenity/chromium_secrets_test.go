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

package amenity

import (
	"testing"

	hostfacts "github.com/qiankunli/hostel/internal/host/facts"
)

// Browser resources may be recycled without replacing Hostel's Tenant identity.
func TestCDPSecretLifecycle(t *testing.T) {
	c := NewChromium(ChromiumConfig{DebugPort: 9222})
	c.state = StateIdle // token operations do not require a browser executable
	reg := NewManager(hostfacts.Collect())
	if err := reg.Register(c); err != nil {
		t.Fatal(err)
	}
	if err := reg.AdmitBed("local-bed-id"); err != nil {
		t.Fatal(err)
	}
	tenant, err := reg.Browser(t.Context(), "local-bed-id")
	if err != nil {
		t.Fatal(err)
	}
	token, err := tenant.CDPToken()
	if err != nil || token == "" {
		t.Fatalf("mint: %v", err)
	}
	if err := tenant.CloseContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if again, _ := tenant.CDPToken(); again != token {
		t.Fatal("context close revoked tenant credential")
	}
	if err := reg.ReleaseBed(t.Context(), "local-bed-id"); err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.CDPToken(); err == nil {
		t.Fatal("closed tenant re-minted credential")
	}
	if len(reg.BedStatus("local-bed-id")) != 0 {
		t.Fatal("released binding remains")
	}
	if err := reg.AdmitBed("new-local-id"); err != nil {
		t.Fatal(err)
	}
	next, err := reg.Browser(t.Context(), "new-local-id")
	if err != nil {
		t.Fatal(err)
	}
	if next.ID() == tenant.ID() {
		t.Fatal("replacement reused tenant identity")
	}
}
