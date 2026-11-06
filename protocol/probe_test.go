package protocol_test

import (
	"encoding/json"
	"github.com/qiankunli/hostel/protocol"
	"testing"
)

func TestProbePortWireContract(t *testing.T) {
	for _, input := range []string{`"hictld"`, `18089`} {
		var action protocol.HTTPGetAction
		if err := json.Unmarshal([]byte(`{"path":"/ready","port":`+input+`}`), &action); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(action.Port)
		if err != nil || string(encoded) != input {
			t.Fatalf("port=%s err=%v", encoded, err)
		}
	}
	for _, input := range []string{`true`, `1.5`, `{}`, `[]`, `null`, ` null `} {
		var port protocol.ProbePort
		if err := json.Unmarshal([]byte(input), &port); err == nil {
			t.Fatalf("accepted %s", input)
		}
	}
}
