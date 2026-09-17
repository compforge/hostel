// Package service supervises declared Bed workloads through the Bed executor.
package service

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/configuration"
)

var identifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$`)

// Normalize validates caller-owned service units, applies stable defaults and
// pins the resulting desired state for local recovery.
func Normalize(specs []bed.ServiceSpec) ([]bed.ServiceSpec, error) {
	out := bed.CloneServices(specs)
	seen := make(map[string]bool)
	for i := range out {
		s := &out[i]
		providedDigest := s.SpecDigest
		s.SpecDigest = ""
		if !identifier.MatchString(s.Name) || seen[s.Name] || len(s.Command) == 0 || s.Command[0] == "" {
			return nil, fmt.Errorf("invalid or duplicate service %q", s.Name)
		}
		seen[s.Name] = true
		if s.Restart == "" {
			s.Restart = "never"
		}
		if !slices.Contains([]string{"never", "on-failure", "always"}, s.Restart) || s.MaxRestarts < 0 || s.MaxRestarts > 100 {
			return nil, fmt.Errorf("invalid restart policy for %s", s.Name)
		}
		if s.MaxRestarts == 0 {
			s.MaxRestarts = 5
		}
		if s.StartupSeconds == 0 {
			s.StartupSeconds = 30
		}
		if s.StopSeconds == 0 {
			s.StopSeconds = 5
		}
		if s.StartupSeconds < 1 || s.StartupSeconds > 120 || s.StopSeconds < 1 || s.StopSeconds > 30 {
			return nil, fmt.Errorf("invalid service deadlines for %s", s.Name)
		}
		if s.Directory == "" {
			s.Directory = "/workspace"
		}
		if err := configuration.Validate(s.Configuration()); err != nil {
			return nil, err
		}
		if s.HTTP != nil {
			if s.PortMapping == "" {
				return nil, fmt.Errorf("HTTP service %s requires a port_mapping", s.Name)
			}
			if !strings.HasPrefix(s.HTTP.ReadyPath, "/") || strings.HasPrefix(s.HTTP.ReadyPath, "//") {
				return nil, fmt.Errorf("HTTP service %s requires readiness path", s.Name)
			}
			if err := validateAuthentication(*s); err != nil {
				return nil, err
			}
		}
		raw, _ := json.Marshal(s)
		digest := fmt.Sprintf("%x", sha256.Sum256(raw))
		if providedDigest != "" && providedDigest != digest {
			return nil, fmt.Errorf("service %s definition changed since local creation", s.Name)
		}
		s.SpecDigest = digest
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func validateAuthentication(s bed.ServiceSpec) error {
	auth := s.HTTP.Authentication
	if auth == nil {
		return nil
	}
	if auth.Scheme != "bearer" || !configuration.ValidName(auth.TokenEnv) {
		return fmt.Errorf("HTTP service %s requires bearer authentication and a valid token env", s.Name)
	}
	value, hasEnv := s.Env[auth.TokenEnv]
	_, hasFile := s.EnvFiles[auth.TokenEnv]
	_, hasRef := s.EnvValueFrom[auth.TokenEnv]
	switch auth.TokenSource {
	case bed.TokenSourceGenerated:
		if hasEnv || hasFile || hasRef {
			return fmt.Errorf("HTTP service %s generated token conflicts with configured env %s", s.Name, auth.TokenEnv)
		}
	case bed.TokenSourceEnvironment:
		if !hasFile && !hasRef && len(s.EnvFrom) == 0 && value == "" {
			return fmt.Errorf("HTTP service %s requires token env %s from Env or EnvFiles", s.Name, auth.TokenEnv)
		}
	default:
		return fmt.Errorf("HTTP service %s requires an explicit supported token source", s.Name)
	}
	return nil
}
