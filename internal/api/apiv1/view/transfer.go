package view

import (
	"time"

	"github.com/qiankunli/hostel/internal/bed/store"
)

type Transfer struct {
	Ref        string              `json:"ref,omitempty"`
	Sync       store.SyncKind      `json:"sync"`
	ID         string              `json:"id"`
	BedID      string              `json:"bed_id"`
	InstanceID string              `json:"instance_id"`
	State      store.TransferState `json:"state"`
	Files      int64               `json:"files"`
	Bytes      int64               `json:"bytes"`
	Error      string              `json:"error,omitempty"`
	StartedAt  time.Time           `json:"started_at"`
	FinishedAt *time.Time          `json:"finished_at,omitempty"`
}

func TransferFrom(t store.Transfer) Transfer {
	return Transfer{Ref: t.Ref, Sync: t.Sync, ID: t.ID, BedID: t.BedID, InstanceID: t.InstanceID, State: t.State, Files: t.Files, Bytes: t.Bytes, Error: t.Error, StartedAt: t.StartedAt, FinishedAt: t.FinishedAt}
}
