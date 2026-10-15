package view

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/qiankunli/hostel/internal/bed/executor"
	"github.com/qiankunli/hostel/internal/bed/service"
	"github.com/qiankunli/hostel/protocol"
)

func TestServiceStatusPreservesWireShapeAndHidesTransportDetail(t *testing.T) {
	outcome := executor.Lost("executor-1", nil)
	outcome.Detail = "private transport detail"
	status := service.Status{ServiceStatus: protocol.ServiceStatus{Name: "worker", Phase: "failed"}, Outcome: &outcome}
	snapshot := ServiceStatusFrom(status)
	if outcome.Detail != "private transport detail" {
		t.Fatal("snapshot mutated internal outcome")
	}
	var observed executor.ProcessOutcome
	if err := json.Unmarshal(snapshot.Outcome, &observed); err != nil {
		t.Fatal(err)
	}
	if observed.Detail != "" || observed.Kind != outcome.Kind || observed.Error != outcome.Error {
		t.Fatalf("public outcome: %+v", observed)
	}
	// Before extraction, the API serialized the internal status after clearing Detail.
	sanitized := outcome
	sanitized.Detail = ""
	status.Outcome = &sanitized
	oldWire, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	newWire, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var oldValue, newValue any
	if err := json.Unmarshal(oldWire, &oldValue); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(newWire, &newValue); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(oldValue, newValue) {
		t.Fatalf("service wire shape changed: before=%s after=%s", oldWire, newWire)
	}
}
