// Package pillow supervises declared Bed workloads through the Bed executor.
package pillow

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/configuration"
)

var identifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$`)

// Normalize validates caller-owned pillow units, applies stable defaults and
// pins the resulting desired state for local recovery.
func Normalize(specs []bed.PillowSpec) ([]bed.PillowSpec, error) {
	if len(specs) == 0 {
		return nil, fmt.Errorf("pillows must not be empty")
	}
	out := bed.ClonePillows(specs)
	seen := make(map[string]bool)
	for i := range out {
		s := &out[i]
		var err error
		s.Mounts, err = bed.NormalizeMounts(s.Mounts)
		if err != nil {
			return nil, fmt.Errorf("pillow %s mounts: %w", s.Name, err)
		}
		providedDigest := s.SpecDigest
		s.SpecDigest = ""
		if !identifier.MatchString(s.Name) || seen[s.Name] {
			return nil, fmt.Errorf("invalid or duplicate pillow %q", s.Name)
		}
		seen[s.Name] = true
		if s.Workdir == "" {
			s.Workdir = "/workspace"
		}
		if !path.IsAbs(s.Workdir) || strings.ContainsRune(s.Workdir, 0) {
			return nil, fmt.Errorf("pillow %s requires absolute workdir", s.Name)
		}
		s.Workdir = path.Clean(s.Workdir)
		if len(s.Command) == 0 {
			if s.Command != nil || len(s.Args) > 0 || s.RestartPolicy != "" || s.Required || s.MaxRestarts != 0 || s.StartupSeconds != 0 || s.StopSeconds != 0 || s.HTTP != nil || s.PortMapping != "" {
				return nil, fmt.Errorf("pillow %s process settings require command", s.Name)
			}
		} else {
			if s.Command[0] == "" {
				return nil, fmt.Errorf("pillow %s command is empty", s.Name)
			}
			for _, arg := range append(slices.Clone(s.Command), s.Args...) {
				if strings.ContainsRune(arg, 0) {
					return nil, fmt.Errorf("pillow %s command contains NUL", s.Name)
				}
			}
			if s.RestartPolicy == "" {
				s.RestartPolicy = "never"
			}
			if !slices.Contains([]string{"never", "on-failure", "always"}, s.RestartPolicy) || s.MaxRestarts < 0 || s.MaxRestarts > 100 {
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
				return nil, fmt.Errorf("invalid process deadlines for %s", s.Name)
			}
		}
		if err := configuration.Validate(bed.PillowConfiguration(*s)); err != nil {
			return nil, err
		}
		if s.HTTP != nil {
			if s.PortMapping == "" {
				return nil, fmt.Errorf("HTTP pillow %s requires a port_mapping", s.Name)
			}
			if !strings.HasPrefix(s.HTTP.ReadyPath, "/") || strings.HasPrefix(s.HTTP.ReadyPath, "//") {
				return nil, fmt.Errorf("HTTP pillow %s requires readiness path", s.Name)
			}
			if err := validateAuthentication(*s); err != nil {
				return nil, err
			}
		}
		raw, _ := json.Marshal(s)
		digest := fmt.Sprintf("%x", sha256.Sum256(raw))
		if providedDigest != "" && providedDigest != digest {
			return nil, fmt.Errorf("pillow %s definition changed since local creation", s.Name)
		}
		s.SpecDigest = digest
	}
	return out, nil
}

func validateAuthentication(s bed.PillowSpec) error {
	auth := s.HTTP.Authentication
	if auth == nil {
		return nil
	}
	if auth.Scheme != "bearer" || !configuration.ValidName(auth.TokenEnv) {
		return fmt.Errorf("HTTP pillow %s requires bearer authentication and a valid token env", s.Name)
	}
	value, hasEnv := s.Env[auth.TokenEnv]
	_, hasFile := s.EnvFiles[auth.TokenEnv]
	_, hasRef := s.EnvValueFrom[auth.TokenEnv]
	switch auth.TokenSource {
	case bed.TokenSourceGenerated:
		if hasEnv || hasFile || hasRef {
			return fmt.Errorf("HTTP pillow %s generated token conflicts with configured env %s", s.Name, auth.TokenEnv)
		}
	case bed.TokenSourceEnvironment:
		if !hasFile && !hasRef && len(s.EnvFrom) == 0 && value == "" {
			return fmt.Errorf("HTTP pillow %s requires token env %s from Env or EnvFiles", s.Name, auth.TokenEnv)
		}
	default:
		return fmt.Errorf("HTTP pillow %s requires an explicit supported token source", s.Name)
	}
	return nil
}
