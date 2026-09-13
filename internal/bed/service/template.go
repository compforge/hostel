// Package service supervises declared Bed workloads through the Bed executor.
package service

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/qiankunli/hostel/internal/bed"
)

// Template is trusted deployment configuration, loaded once before admission.
// Durations are seconds. Command is argv, never an implicitly evaluated shell.
type Template struct {
	Name           string            `json:"name"`
	Command        []string          `json:"command"`
	Directory      string            `json:"directory,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	EnvFiles       map[string]string `json:"env_files,omitempty"`
	AllowEnv       []string          `json:"allow_env,omitempty"`
	Required       bool              `json:"required,omitempty"`
	Restart        string            `json:"restart,omitempty"`
	MaxRestarts    int               `json:"max_restarts,omitempty"`
	StartupSeconds int               `json:"startup_seconds,omitempty"`
	StopSeconds    int               `json:"stop_seconds,omitempty"`
	HTTP           *HTTPTemplate     `json:"http,omitempty"`
}
type HTTPTemplate struct {
	ReadyPath string `json:"ready_path"`
	// TokenEnv must be consumed by the service to authenticate each request.
	// A fresh token per execution prevents stale addresses authorizing a new Bed.
	TokenEnv string `json:"token_env"`
}
type Catalog struct {
	templates map[string]Template
	digests   map[string]string
}

var identifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$`)
var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func LoadCatalog(dir string) (*Catalog, error) {
	var templates []Template
	if dir != "" {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			f, err := os.Open(filepath.Join(dir, entry.Name()))
			if err != nil {
				return nil, err
			}
			dec := json.NewDecoder(io.LimitReader(f, 1<<20))
			dec.DisallowUnknownFields()
			var t Template
			err = dec.Decode(&t)
			if err == nil {
				var extra any
				if dec.Decode(&extra) != io.EOF {
					err = fmt.Errorf("expected one JSON object")
				}
			}
			f.Close()
			if err != nil {
				return nil, fmt.Errorf("service template %s: %w", entry.Name(), err)
			}
			templates = append(templates, t)
		}
	}
	return NewCatalog(templates)
}

func NewCatalog(templates []Template) (*Catalog, error) {
	c := &Catalog{templates: make(map[string]Template), digests: make(map[string]string)}
	for _, original := range templates {
		// Freeze caller-owned maps and slices before concurrent use.
		raw, _ := json.Marshal(original)
		var t Template
		_ = json.Unmarshal(raw, &t)
		if !identifier.MatchString(t.Name) || len(t.Command) == 0 || t.Command[0] == "" {
			return nil, fmt.Errorf("invalid service template %q", t.Name)
		}
		if _, ok := c.templates[t.Name]; ok {
			return nil, fmt.Errorf("duplicate service template %q", t.Name)
		}
		if t.Restart == "" {
			t.Restart = "never"
		}
		if !slices.Contains([]string{"never", "on-failure", "always"}, t.Restart) || t.MaxRestarts < 0 || t.MaxRestarts > 100 {
			return nil, fmt.Errorf("invalid restart policy for %s", t.Name)
		}
		if t.MaxRestarts == 0 {
			t.MaxRestarts = 5
		}
		if t.StartupSeconds == 0 {
			t.StartupSeconds = 30
		}
		if t.StopSeconds == 0 {
			t.StopSeconds = 5
		}
		if t.StartupSeconds < 1 || t.StartupSeconds > 120 || t.StopSeconds < 1 || t.StopSeconds > 30 {
			return nil, fmt.Errorf("invalid service deadlines for %s", t.Name)
		}
		if t.Directory == "" {
			t.Directory = "/workspace"
		}
		for k := range t.Env {
			if !validEnv(k) {
				return nil, fmt.Errorf("invalid template env key %s", k)
			}
		}
		for k, p := range t.EnvFiles {
			if !validEnv(k) || !filepath.IsAbs(p) {
				return nil, fmt.Errorf("invalid credential source for %s", k)
			}
			if _, ok := t.Env[k]; ok {
				return nil, fmt.Errorf("duplicate env key %s", k)
			}
		}
		for _, k := range t.AllowEnv {
			if !validEnv(k) {
				return nil, fmt.Errorf("invalid override key %s", k)
			}
			if _, ok := t.EnvFiles[k]; ok {
				return nil, fmt.Errorf("credential override forbidden: %s", k)
			}
		}
		if t.HTTP != nil {
			if !strings.HasPrefix(t.HTTP.ReadyPath, "/") || strings.HasPrefix(t.HTTP.ReadyPath, "//") || !validEnv(t.HTTP.TokenEnv) {
				return nil, fmt.Errorf("HTTP template %s requires readiness path and per-execution token env", t.Name)
			}
			if slices.Contains(t.AllowEnv, t.HTTP.TokenEnv) {
				return nil, fmt.Errorf("token cannot be caller-overridden")
			}
		}
		raw, _ = json.Marshal(t)
		c.digests[t.Name] = fmt.Sprintf("%x", sha256.Sum256(raw))
		c.templates[t.Name] = t
	}
	return c, nil
}
func validEnv(k string) bool {
	return envName.MatchString(k) && !strings.HasPrefix(k, "HOSTEL_") && !strings.HasPrefix(k, "BED_")
}

func (c *Catalog) Resolve(specs []bed.ServiceSpec) ([]bed.ServiceSpec, error) {
	out := bed.CloneServices(specs)
	seen := make(map[string]bool)
	for i := range out {
		s := &out[i]
		if !identifier.MatchString(s.Name) || seen[s.Name] {
			return nil, fmt.Errorf("invalid or duplicate service name %q", s.Name)
		}
		seen[s.Name] = true
		if len(s.Env) == 0 {
			s.Env = nil
		}
		var t Template
		var ok bool
		if c != nil {
			t, ok = c.templates[s.Template]
		}
		if !ok {
			return nil, fmt.Errorf("unknown service template %q", s.Template)
		}
		if s.TemplateDigest != "" && s.TemplateDigest != c.digests[s.Template] {
			return nil, fmt.Errorf("service %s template changed since local creation", s.Name)
		}
		for k, v := range s.Env {
			if !slices.Contains(t.AllowEnv, k) || strings.ContainsRune(v, 0) {
				return nil, fmt.Errorf("service %s override not allowed: %s", s.Name, k)
			}
		}
		s.TemplateDigest = c.digests[s.Template]
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
