package configuration

import (
	"context"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/qiankunli/hostel/internal/bed"
)

const maxValueBytes = 65536

// Config maps operator-approved source names to directories of configuration files.
// Registration never reads values; unused sources do not block unrelated Beds.
type Config struct {
	Sources map[string]string `json:"sources,omitempty"`
}

func (c Config) Validate() error {
	for name, directory := range c.Sources {
		if !validKey(name) || !filepath.IsAbs(directory) || strings.ContainsRune(directory, 0) {
			return fmt.Errorf("invalid configuration source %q", name)
		}
	}
	return nil
}

type Report struct {
	Sources      []SourceStatus `json:"sources"`
	PreparedBeds int            `json:"prepared_beds"`
}

// SourceStatus is host evidence, not a guarantee of process isolation or valid values.
type SourceStatus struct {
	Name              string    `json:"name"`
	DirectoryReadable bool      `json:"directory_readable"`
	Reason            string    `json:"reason"`
	ObservedAt        time.Time `json:"observed_at,omitzero"`
}

// Manager owns main-process values for a resident Bed. Service restarts resolve
// their own declaration independently; no values enter the Bed's persisted Spec.
type Manager struct {
	bed.Noop
	sources      map[string]string
	mu           sync.RWMutex
	main         map[*bed.Bed]map[string]string
	observations map[string]SourceStatus
}

func NewManager(c Config) (*Manager, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &Manager{sources: maps.Clone(c.Sources), main: make(map[*bed.Bed]map[string]string), observations: make(map[string]SourceStatus)}, nil
}

// Start observes source directories under the daemon's real host identity.
// Missing unused sources are reported, not promoted to a daemon-wide failure.
func (m *Manager) Start(ctx context.Context) error {
	for name := range m.sources {
		if err := ctx.Err(); err != nil {
			return err
		}
		root, err := m.openSource(name)
		if err == nil {
			root.Close()
		}
	}
	return nil
}

func (m *Manager) Prepare(ctx context.Context, b *bed.Bed) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	env, err := m.Resolve(b.Spec().MainConfiguration())
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.main[b] = env
	return nil
}

func (m *Manager) Main(b *bed.Bed) (map[string]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	env, ok := m.main[b]
	if !ok {
		return nil, fmt.Errorf("Bed configuration is not prepared")
	}
	return maps.Clone(env), nil
}

func (m *Manager) Release(_ context.Context, b *bed.Bed) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.main, b)
	return nil
}

func (m *Manager) Close(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	clear(m.main)
	return nil
}

func (m *Manager) Status() Report {
	m.mu.RLock()
	defer m.mu.RUnlock()
	sources := make([]SourceStatus, 0, len(m.sources))
	for name := range m.sources {
		status, ok := m.observations[name]
		if !ok {
			status = SourceStatus{Name: name, Reason: "NotObserved"}
		}
		sources = append(sources, status)
	}
	slices.SortFunc(sources, func(a, b SourceStatus) int { return strings.Compare(a.Name, b.Name) })
	return Report{Sources: sources, PreparedBeds: len(m.main)}
}

func (*Manager) LevelStatus() bed.LevelStatus { return bed.LevelStatus{} }

var _ bed.Component[Report] = (*Manager)(nil)

// Resolve imports directories in declaration order, then explicit variables.
// Only source references are retained; contents are never copied into BedFS.
func (m *Manager) Resolve(s bed.Configuration) (map[string]string, error) {
	if err := Validate(s); err != nil {
		return nil, err
	}
	env := make(map[string]string)
	for _, name := range s.EnvFrom {
		values, err := m.readSource(name)
		if err != nil {
			return nil, err
		}
		maps.Copy(env, values)
	}
	for name, ref := range s.EnvValueFrom {
		root, err := m.openSource(ref.Source)
		if err != nil {
			return nil, err
		}
		value, err := readRootValue(root, ref.Key)
		root.Close()
		if err != nil {
			return nil, fmt.Errorf("%w: source %q key %q unavailable or invalid", ErrInvalid, ref.Source, ref.Key)
		}
		env[name] = value
	}
	for name, file := range s.EnvFiles {
		// Explicit file paths are a trusted control-plane facility, matching the
		// existing process API. Prefer registered sources for directory imports.
		root, err := os.OpenRoot(filepath.Dir(file))
		if err != nil {
			return nil, fmt.Errorf("%w: file for %q unavailable", ErrInvalid, name)
		}
		value, err := readRootValue(root, filepath.Base(file))
		root.Close()
		if err != nil {
			return nil, fmt.Errorf("%w: file for %q unavailable or invalid", ErrInvalid, name)
		}
		env[name] = value
	}
	maps.Copy(env, s.Env)
	return env, nil
}

func (m *Manager) openSource(name string) (*os.Root, error) {
	directory, ok := m.sources[name]
	if !ok {
		return nil, fmt.Errorf("%w: unknown source %q", ErrInvalid, name)
	}
	root, err := os.OpenRoot(directory)
	if err == nil {
		var dir *os.File
		dir, err = root.Open(".")
		if err == nil {
			_, err = dir.ReadDir(1)
			dir.Close()
			if err == io.EOF {
				err = nil
			}
		}
	}
	status := SourceStatus{Name: name, DirectoryReadable: err == nil, Reason: "Readable", ObservedAt: time.Now()}
	if err != nil {
		status.Reason = "Unavailable"
	}
	m.mu.Lock()
	m.observations[name] = status
	m.mu.Unlock()
	if err != nil {
		if root != nil {
			root.Close()
		}
		return nil, fmt.Errorf("%w: source %q unavailable", ErrInvalid, name)
	}
	return root, nil
}

func (m *Manager) readSource(name string) (map[string]string, error) {
	root, err := m.openSource(name)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	dir, err := root.Open(".")
	if err != nil {
		return nil, fmt.Errorf("%w: source %q unavailable", ErrInvalid, name)
	}
	defer dir.Close()
	entries, err := dir.ReadDir(4097)
	if (err != nil && err != io.EOF) || len(entries) > 4096 {
		return nil, fmt.Errorf("%w: source %q cannot be enumerated", ErrInvalid, name)
	}
	env := make(map[string]string)
	for _, entry := range entries {
		key := entry.Name()
		// Atomic directory projections use hidden generation entries. The public
		// key symlinks are followed within os.Root, never outside this source.
		if strings.HasPrefix(key, "..") {
			continue
		}
		if !ValidName(key) {
			return nil, fmt.Errorf("%w: source %q has invalid variable %q", ErrInvalid, name, key)
		}
		value, err := readRootValue(root, key)
		if err != nil {
			return nil, fmt.Errorf("%w: source %q key %q unavailable or invalid", ErrInvalid, name, key)
		}
		env[key] = value
	}
	return env, nil
}

func readRootValue(root *os.Root, key string) (string, error) {
	info, err := root.Stat(key)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("not a regular file")
	}
	f, err := root.Open(key)
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxValueBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxValueBytes || strings.ContainsRune(string(data), 0) {
		return "", fmt.Errorf("invalid environment value")
	}
	return strings.TrimSuffix(string(data), "\n"), nil
}
