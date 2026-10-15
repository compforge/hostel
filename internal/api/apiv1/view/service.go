package view

import (
	"encoding/json"

	"github.com/qiankunli/hostel/internal/bed/service"
	"github.com/qiankunli/hostel/protocol"
)

// ServiceStatusFrom exposes the same service facts without exporting executor
// implementation types or private transport diagnostics.
func ServiceStatusFrom(status service.Status) protocol.ServiceStatus {
	snapshot := status.ServiceStatus
	if status.Outcome != nil {
		// ProcessOutcome consists only of scalar fields and is always JSON encodable.
		outcome := *status.Outcome
		outcome.Detail = ""
		snapshot.Outcome, _ = json.Marshal(outcome)
	}
	return snapshot
}
