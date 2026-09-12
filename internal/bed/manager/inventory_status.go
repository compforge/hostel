package manager

import (
	"github.com/qiankunli/hostel/internal/bed/network"
	"github.com/qiankunli/hostel/internal/bed/resource"
	"time"
)

type InstanceState string

const (
	InstanceRetained   InstanceState = "retained"   // a resident bed is within its retention promise
	InstanceDraining   InstanceState = "draining"   // resident beds all expired, eviction in progress
	InstanceReleasable InstanceState = "releasable" // nothing resident; snapshots (if any) are remote
)

// statusOfInstance folds the scheduler-visible inventory, the compatibility
// default bed into the hostel-layer status. A zero
// RetainUntil (no idle TTL configured) counts as retained — releasable must
// never be concluded from unknown retention.
func statusOfInstance(beds []InventoryBed, defaultBedOccupied bool, now time.Time) InstanceState {
	// The default bed never participates in scheduler inventory or idle GC, so
	// its residency is a separate, unconditional reason to retain this instance.
	if defaultBedOccupied {
		return InstanceRetained
	}
	hasResident, allExpired := false, true
	for _, b := range beds {
		switch b.Status.Phase {
		case PhaseInitializing:
			hasResident = true
			allExpired = false
			continue
		case PhasePurging:
			hasResident = true
			continue
		case PhaseFailed:
			continue
		case PhaseDormant:
			continue
		}
		hasResident = true
		if b.RetainUntil.IsZero() || b.RetainUntil.After(now) {
			allExpired = false
		}
	}
	switch {
	case hasResident && !allExpired:
		return InstanceRetained
	case hasResident:
		return InstanceDraining
	default:
		return InstanceReleasable
	}
}

// InventoryStatus is one captured inventory and all of its derived counts.
type InventoryStatus struct {
	Instance InstanceStatus `json:"instance"`
	Beds     []InventoryBed `json:"beds"`
}

type InstanceStatus struct {
	Status                      InstanceState            `json:"status"`
	Sync                        string                   `json:"sync"`
	BedSyncSelection            bool                     `json:"bed_sync_selection"`
	TransferSyncs               []string                 `json:"transfer_syncs"`
	FileTransfers               bool                     `json:"file_transfers"`
	TransferInstanceID          string                   `json:"transfer_instance_id"`
	Network                     network.Status           `json:"network"`
	NetworkPolicy               bool                     `json:"network_policy"`
	Isolation                   string                   `json:"isolation"`
	OccupiedBeds                int                      `json:"occupied_beds"`
	ResidentBeds                int                      `json:"resident_beds"`
	MaxBeds                     int                      `json:"max_beds"`
	PinnedBeds                  int                      `json:"pinned_beds"`
	MaxPinnedBeds               int                      `json:"max_pinned_beds"`
	BedPressureThresholdPercent int                      `json:"bed_pressure_threshold_percent"`
	BedPressure                 bool                     `json:"bed_pressure"`
	PhaseCounts                 map[string]int           `json:"phase_counts"`
	ActivityCounts              map[string]int           `json:"activity_counts"`
	RetainedUntil               *time.Time               `json:"retained_until"`
	LuggageBytes                int64                    `json:"luggage_bytes"`
	LuggageHighBytes            int64                    `json:"luggage_high_bytes"`
	LuggageLowBytes             int64                    `json:"luggage_low_bytes"`
	ResourceAdmission           resource.AdmissionReport `json:"resource_admission"`
}

func (m *Manager) InventoryStatus() InventoryStatus {
	beds, defaultOccupied := m.captureInventory()
	instance := InstanceStatus{
		Status: statusOfInstance(beds, defaultOccupied, time.Now()),
		Sync:   m.SyncName(), BedSyncSelection: true, TransferSyncs: []string{"copy", "restic"},
		FileTransfers: m.TransfersConfigured(), TransferInstanceID: m.TransferInstanceID(),
		Network: m.NetworkReport(), NetworkPolicy: true, Isolation: m.iso.Level().String(),
		MaxBeds: m.maxBeds, MaxPinnedBeds: m.maxPinnedBeds, BedPressureThresholdPercent: m.pressurePercent,
		LuggageHighBytes: m.luggageHigh, LuggageLowBytes: m.luggageLow, ResourceAdmission: m.ResourceAdmissionReport(),
		PhaseCounts: map[string]int{}, ActivityCounts: map[string]int{string(ActivityIdle): 0, string(ActivityActive): 0},
	}
	for _, phase := range []Phase{PhaseResident, PhaseEvicting, PhasePurging, PhaseDormant, PhaseInitializing, PhaseFailed} {
		instance.PhaseCounts[string(phase)] = 0
	}
	var retainUntil time.Time
	retentionKnown := true
	for _, b := range beds {
		instance.PhaseCounts[string(b.Status.Phase)]++
		if b.Status.Activity != "" {
			instance.ActivityCounts[string(b.Status.Activity)]++
		}
		if b.Pinned {
			instance.PinnedBeds++
		}
		switch b.Status.Phase {
		case PhaseDormant:
			instance.LuggageBytes += b.LocalBytes
			continue
		case PhaseFailed:
			continue
		case PhaseResident, PhaseEvicting:
			instance.ResidentBeds++
		}
		instance.OccupiedBeds++
		if b.RetainUntil.IsZero() {
			retentionKnown = false
		} else if b.RetainUntil.After(retainUntil) {
			retainUntil = b.RetainUntil
		}
	}
	if instance.OccupiedBeds > 0 && retentionKnown {
		instance.RetainedUntil = &retainUntil
	}
	occupiedThreshold := bedPressureThreshold(m.maxBeds, m.pressurePercent)
	pinnedThreshold := bedPressureThreshold(m.maxPinnedBeds, m.pressurePercent)
	instance.BedPressure = (occupiedThreshold > 0 && int64(instance.OccupiedBeds) >= occupiedThreshold) || (pinnedThreshold > 0 && int64(instance.PinnedBeds) >= pinnedThreshold)
	return InventoryStatus{Instance: instance, Beds: beds}
}
