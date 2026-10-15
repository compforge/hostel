package protocol_test

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/qiankunli/hostel/protocol"
)

// +case:id=public_bed_contract,desc=`A caller imports only protocol and reads a Bed detail with separate ID and Name`,expect=`Lifecycle, services, ports and diagnostic JSON survive without changing field paths or integer precision`
func TestBedDetailWireContract(t *testing.T) {
	wire, err := os.ReadFile("testdata/bed.json")
	if err != nil {
		t.Fatal(err)
	}
	var bed protocol.BedDetail
	if err := json.Unmarshal(wire, &bed); err != nil {
		t.Fatal(err)
	}
	if bed.ID != "bed-00000000000000000000000000000001" || bed.Name != "caller-bed" || bed.Status.Lifecycle.Phase != protocol.PhaseResident || !bed.Status.Lifecycle.Readiness.Ready {
		t.Fatalf("Bed identity/readiness: %+v", bed)
	}
	if bed.Generation != 9007199254740993 || bed.Status.Components.Store.SnapshotGeneration != 9007199254740992 {
		t.Fatal("data generation lost integer precision")
	}
	if len(bed.Status.Services) != 1 || len(bed.Status.PortMappings) != 1 {
		t.Fatal("missing service or port mapping")
	}
	service, port := bed.Status.Services[0], bed.Status.PortMappings[0]
	if !service.Ready || service.PortMapping != port.Name || service.ExecutionID != port.ExecutionID || port.State != "listening" || port.BedPort != 8080 || port.HostPort != 24000 {
		t.Fatalf("service/port association: %+v %+v", service, port)
	}
	if bed.Activity.Operations[protocol.OpExec] != 1 || bed.Activity.Sessions[protocol.SessionKindCDP] != 1 {
		t.Fatal("activity kinds lost")
	}
	encoded, err := json.Marshal(bed)
	if err != nil {
		t.Fatal(err)
	}
	decode := func(raw []byte) any {
		t.Helper()
		var value any
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	if !reflect.DeepEqual(decode(wire), decode(encoded)) {
		t.Fatalf("wire contract changed: %s", encoded)
	}
}
