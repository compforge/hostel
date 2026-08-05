// Copyright 2026 Li Qiankun
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package bed

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/qiankunli/hostel/internal/amenity"
)

// ErrInvalidEnvironment marks a caller- or deployment-supplied environment
// that cannot safely become part of a bed process.
var ErrInvalidEnvironment = errors.New("bed: invalid environment")

var envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// processEnv is the immutable carrier-software environment shared by every bed.
// It deliberately contains only deployment-selected standard variables: the
// hostel daemon's own HOSTEL_*/credential environment is never a child default.
type processEnv struct {
	carrier map[string]string
}

func newProcessEnv(hostEnv, passthrough []string) (processEnv, error) {
	wanted := make(map[string]struct{}, len(passthrough))
	for _, name := range passthrough {
		if err := validateExternalEnvName("passthrough", name); err != nil {
			return processEnv{}, err
		}
		wanted[name] = struct{}{}
	}

	carrier := make(map[string]string, len(wanted))
	for _, entry := range hostEnv {
		name, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, ok := wanted[name]; ok {
			carrier[name] = value
		}
	}
	return processEnv{carrier: carrier}, nil
}

// SetBedEnvPassthrough selects the carrier-software variables inherited by bed
// processes. It is startup configuration and must be called before serving.
func (m *Manager) SetBedEnvPassthrough(hostEnv, keys []string) error {
	env, err := newProcessEnv(hostEnv, keys)
	if err != nil {
		return err
	}
	m.processEnv = env
	return nil
}

// ValidateRequestEnv enforces the public env namespace contract before a web
// adapter starts work. buildBedEnv repeats the check at the core boundary so
// non-HTTP callers cannot bypass it.
func ValidateRequestEnv(env map[string]string) error {
	keys := make([]string, 0, len(env))
	for name := range env {
		keys = append(keys, name)
	}
	slices.Sort(keys)
	for _, name := range keys {
		if err := validateExternalEnvName("request", name); err != nil {
			return err
		}
		if strings.ContainsRune(env[name], '\x00') {
			return fmt.Errorf("%w: request variable %q contains NUL", ErrInvalidEnvironment, name)
		}
	}
	return nil
}

func validateExternalEnvName(scope, name string) error {
	if !envNameRe.MatchString(name) {
		return fmt.Errorf("%w: %s variable name %q is invalid", ErrInvalidEnvironment, scope, name)
	}
	if strings.HasPrefix(name, "HOSTEL_") || strings.HasPrefix(name, "BED_") {
		return fmt.Errorf("%w: %s variable %q uses a reserved namespace", ErrInvalidEnvironment, scope, name)
	}
	if name == "PLAYWRIGHT_MCP_CDP_ENDPOINT" {
		return fmt.Errorf("%w: %s variable %q is managed by the bed", ErrInvalidEnvironment, scope, name)
	}
	return nil
}

// buildBedEnv composes the only environment shape used to spawn bed code:
// carrier software + bed-owned context + one invocation's explicit overlay.
func (m *Manager) buildBedEnv(b *Bed, requestEnv map[string]string) ([]string, error) {
	if err := ValidateRequestEnv(requestEnv); err != nil {
		return nil, err
	}
	env := make(map[string]string, len(m.processEnv.carrier)+len(requestEnv)+7)
	for name, value := range m.processEnv.carrier {
		env[name] = value
	}

	home := b.Workspace
	if mount := m.iso.MountPoint(); mount != "" {
		home = mount
	}
	env["BED_ID"] = b.ID
	env["HOME"] = home
	env["TMPDIR"] = "/tmp"
	env["USER"] = "hostel-bed"
	env["LOGNAME"] = "hostel-bed"
	env["SHELL"] = m.shellPath

	if endpoint := m.bedCDPEndpoint(b.ID); endpoint != "" {
		env["PLAYWRIGHT_MCP_CDP_ENDPOINT"] = endpoint
	}
	for name, value := range requestEnv {
		env[name] = value
	}

	keys := make([]string, 0, len(env))
	for name := range env {
		keys = append(keys, name)
	}
	slices.Sort(keys)
	out := make([]string, 0, len(keys))
	for _, name := range keys {
		out = append(out, name+"="+env[name])
	}
	return out, nil
}

// SetCDPAdvertise enables per-bed browser endpoint injection. addr is the
// host:port beds can reach hostel on (normally loopback in the shared net ns).
func (m *Manager) SetCDPAdvertise(addr string) { m.cdpAdvertise = addr }

func (m *Manager) bedCDPEndpoint(bedID string) string {
	if m.cdpAdvertise == "" || m.amenities == nil {
		return ""
	}
	a := m.amenities.Find("chromium")
	br, ok := a.(amenity.Browser)
	if a == nil || !ok {
		return ""
	}
	token, err := br.CDPToken(bedID)
	if err != nil {
		// Honest absence: tooling may fall back to its own browser.
		return ""
	}
	u := url.URL{Scheme: "ws", Host: m.cdpAdvertise, Path: "/v1/cdp",
		RawQuery: url.Values{"bed": {bedID}, "t": {token}}.Encode()}
	return u.String()
}
