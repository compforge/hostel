package handler

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/qiankunli/hostel/protocol"
)

// +case:id=bed_public_identity,desc=`Create, read, retry and recreate the same Bed name`,expect=`Creation, detail and inventory agree on ID and Name; recreation changes only the lifetime ID`
func TestBedPublicIdentity(t *testing.T) {
	s := newTestServer(t)
	const name = "caller-bed"
	create := func() protocol.Bed {
		t.Helper()
		rec := do(t, s, http.MethodPost, "/v1/beds", strings.NewReader(`{"id":"`+name+`"}`), map[string]string{"Content-Type": "application/json"})
		if rec.Code != http.StatusAccepted && rec.Code != http.StatusOK {
			t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
		}
		var b protocol.Bed
		if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
			t.Fatal(err)
		}
		if b.Name != name || b.ID == "" || b.ID.String() == name {
			t.Fatalf("creation identity: %+v", b)
		}
		return b
	}
	first := create()
	rec := waitBedReady(t, s, name)
	var detail protocol.BedDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.ID != first.ID || detail.Name != first.Name {
		t.Fatalf("detail identity: %+v; created: %+v", detail.Bed, first)
	}
	if again := create(); again.ID != first.ID {
		t.Fatalf("retry replaced identity: %s -> %s", first.ID, again.ID)
	}
	rec = do(t, s, http.MethodGet, "/v1/beds", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("inventory: %d %s", rec.Code, rec.Body.String())
	}
	var inventory struct {
		Beds []protocol.Bed `json:"beds"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &inventory); err != nil {
		t.Fatal(err)
	}
	if len(inventory.Beds) != 1 || inventory.Beds[0].ID != first.ID || inventory.Beds[0].Name != name {
		t.Fatalf("inventory identity: %+v", inventory.Beds)
	}
	rec = do(t, s, http.MethodDelete, "/v1/beds/"+name+"?purge=true", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("purge: %d %s", rec.Code, rec.Body.String())
	}
	next := create()
	if next.ID == first.ID {
		t.Fatalf("recreation reused identity: %s", next.ID)
	}
	waitBedReady(t, s, name)
}
