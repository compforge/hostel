package view

import (
	"encoding/json"

	"github.com/qiankunli/hostel/internal/bed/pillow"
	"github.com/qiankunli/hostel/protocol"
)

// PillowStatusFrom exposes the same pillow facts without exporting executor
// implementation types or private transport diagnostics.
func PillowStatusFrom(status pillow.Status) protocol.PillowStatus {
	snapshot := status.PillowStatus
	if status.Outcome != nil {
		// ProcessOutcome consists only of scalar fields and is always JSON encodable.
		outcome := *status.Outcome
		outcome.Detail = ""
		snapshot.Outcome, _ = json.Marshal(outcome)
	}
	return snapshot
}
