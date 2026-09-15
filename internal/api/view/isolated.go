package view

import (
	"time"
)

type IsolatedCreate struct {
	SessionID string    `json:"session_id"`
	CreatedAt time.Time `json:"created_at"`
}

type IsolatedSessionState struct {
	Status               string    `json:"status"`
	CreatedAt            time.Time `json:"created_at"`
	LastRunAt            time.Time `json:"last_run_at"`
	IdleRemainingSeconds *int      `json:"idle_remaining_seconds,omitempty"`
	Profile              string    `json:"profile,omitempty"`
	Workdir              string    `json:"workdir"`
	ShareNet             *bool     `json:"share_net,omitempty"`
}

type IsolatedSessionSummary struct {
	SessionID            string    `json:"session_id"`
	Status               string    `json:"status"`
	CreatedAt            time.Time `json:"created_at"`
	LastRunAt            time.Time `json:"last_run_at"`
	IdleRemainingSeconds *int      `json:"idle_remaining_seconds,omitempty"`
}

type IsolatedList struct {
	Sessions []IsolatedSessionSummary `json:"sessions"`
}
