// Package configuration owns process configuration resolution and Bed lifetime state.
package configuration

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/qiankunli/hostel/internal/bed"
)

var ErrInvalid = errors.New("bed: invalid environment")
var identifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidName excludes names owned by the runtime, for every configuration source.
func ValidName(name string) bool {
	return identifier.MatchString(name) && !strings.HasPrefix(name, "HOSTEL_") &&
		!strings.HasPrefix(name, "BED_") && name != "PLAYWRIGHT_MCP_CDP_ENDPOINT"
}

func ValidateEnv(env map[string]string) error {
	for name, value := range env {
		if !identifier.MatchString(name) {
			return fmt.Errorf("%w: invalid variable %q", ErrInvalid, name)
		}
		if strings.HasPrefix(name, "HOSTEL_") || strings.HasPrefix(name, "BED_") {
			return fmt.Errorf("%w: variable %q uses a reserved namespace", ErrInvalid, name)
		}
		if name == "PLAYWRIGHT_MCP_CDP_ENDPOINT" {
			return fmt.Errorf("%w: variable %q is managed by the bed", ErrInvalid, name)
		}
		if strings.ContainsRune(value, 0) {
			return fmt.Errorf("%w: variable %q contains NUL", ErrInvalid, name)
		}
	}
	return nil
}

// Validate checks declaration shape without reading any configuration contents.
func Validate(s bed.Configuration) error {
	if err := ValidateEnv(s.Env); err != nil {
		return err
	}
	for name, file := range s.EnvFiles {
		if !ValidName(name) || !filepath.IsAbs(file) || strings.ContainsRune(file, 0) {
			return fmt.Errorf("%w: invalid file reference for %q", ErrInvalid, name)
		}
		if _, exists := s.Env[name]; exists {
			return fmt.Errorf("%w: duplicate variable %q", ErrInvalid, name)
		}
	}
	for name, ref := range s.EnvValueFrom {
		if !ValidName(name) || ref.Source == "" || !validKey(ref.Key) {
			return fmt.Errorf("%w: invalid source reference for %q", ErrInvalid, name)
		}
		if _, exists := s.Env[name]; exists {
			return fmt.Errorf("%w: duplicate variable %q", ErrInvalid, name)
		}
		if _, exists := s.EnvFiles[name]; exists {
			return fmt.Errorf("%w: duplicate variable %q", ErrInvalid, name)
		}
	}
	for _, source := range s.EnvFrom {
		if source == "" {
			return fmt.Errorf("%w: empty source name", ErrInvalid)
		}
	}
	return nil
}

func validKey(key string) bool {
	return key != "" && key != "." && !strings.HasPrefix(key, "..") &&
		!strings.ContainsAny(key, "/\\\x00")
}

// HasVariable includes whole-directory imports; the value is checked after resolution.
func HasVariable(s bed.Configuration, name string) bool {
	_, direct := s.Env[name]
	_, file := s.EnvFiles[name]
	_, ref := s.EnvValueFrom[name]
	return direct || file || ref || len(s.EnvFrom) > 0
}
