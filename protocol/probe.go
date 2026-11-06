package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
)

// PillowPort declares one process listener. Network owns its runtime allocation.
// +spec=`containerPort is fixed; dynamic explicitly requests allocation. Exactly one must be selected. publish is discovery/forwarding, not a firewall.`
// +link=component://docs/pillow.md
type PillowPort struct {
	Name          string `json:"name"`
	ContainerPort int    `json:"containerPort,omitempty"`
	Protocol      string `json:"protocol,omitempty"`
	Dynamic       bool   `json:"dynamic,omitempty"`
	Publish       bool   `json:"publish,omitempty"`
}

// Probe describes readiness, never application authentication or restart policy.
// Exactly one action is required. Only HTTP GET and TCP socket are supported.
type Probe struct {
	HTTPGet             *HTTPGetAction   `json:"httpGet,omitempty"`
	TCPSocket           *TCPSocketAction `json:"tcpSocket,omitempty"`
	InitialDelaySeconds int              `json:"initialDelaySeconds,omitempty"`
	TimeoutSeconds      int              `json:"timeoutSeconds,omitempty"`
	PeriodSeconds       int              `json:"periodSeconds,omitempty"`
	SuccessThreshold    int              `json:"successThreshold,omitempty"`
	FailureThreshold    int              `json:"failureThreshold,omitempty"`
}

type HTTPGetAction struct {
	Path string    `json:"path"`
	Port ProbePort `json:"port"`
}

type TCPSocketAction struct {
	Port ProbePort `json:"port"`
}

// ProbePort is a named port or numeric containerPort, encoded as a JSON string or integer.
// Numeric references must resolve to a fixed port declared by the same Pillow.
type ProbePort string

func (p ProbePort) MarshalJSON() ([]byte, error) {
	if n, err := strconv.Atoi(string(p)); err == nil {
		return json.Marshal(n)
	}
	return json.Marshal(string(p))
}

func (p *ProbePort) UnmarshalJSON(raw []byte) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fmt.Errorf("probe port must not be null")
	}
	var value string
	if err := json.Unmarshal(raw, &value); err == nil {
		*p = ProbePort(value)
		return nil
	}
	var number int
	if err := json.Unmarshal(raw, &number); err != nil {
		return fmt.Errorf("probe port must be a name or integer: %w", err)
	}
	*p = ProbePort(strconv.Itoa(number))
	return nil
}
