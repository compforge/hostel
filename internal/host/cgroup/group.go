package cgroup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Group owns one directory allocation. The open directory pins its identity;
// repeated cleanup of an old handle cannot remove a replacement at the same path.
// A parent serializes child acquisition and recursive teardown.
type Group struct {
	retiring      bool
	mu            sync.Mutex
	path          string
	dir           *os.File
	owned, closed bool
	children      map[string]*Group
}

func validateName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") {
		return fmt.Errorf("invalid cgroup name %q", name)
	}
	return nil
}
func openGroup(path string, owned bool) (*Group, error) {
	dir, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	return &Group{path: path, dir: dir, owned: owned, children: make(map[string]*Group)}, nil
}

// OpenChild adopts or creates a named accounting parent. CreateChild requires
// a fresh allocation; choose it for replaceable process groups.
func (g *Group) OpenChild(name string) (*Group, error)   { return g.child(name, false) }
func (g *Group) CreateChild(name string) (*Group, error) { return g.child(name, true) }
func (g *Group) child(name string, exclusive bool) (*Group, error) {
	if err := validateName(name); err != nil {
		return nil, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed || g.retiring {
		return nil, os.ErrClosed
	}
	// Retired process groups may have unique names forever. Keep only
	// live handles so a long-lived accounting parent has bounded bookkeeping.
	for key, child := range g.children {
		child.mu.Lock()
		closed := child.closed
		child.mu.Unlock()
		if closed {
			delete(g.children, key)
		}
	}
	if err := g.current(); err != nil {
		return nil, err
	}
	if old := g.children[name]; old != nil {
		old.mu.Lock()
		closed := old.closed
		old.mu.Unlock()
		if !closed {
			if exclusive {
				return nil, os.ErrExist
			}
			return old, nil
		}
	}
	path := filepath.Join(g.path, name)
	err := os.Mkdir(path, 0755)
	if err != nil && (exclusive || !errors.Is(err, os.ErrExist)) {
		return nil, fmt.Errorf("create cgroup %s: %w", path, err)
	}
	child, openErr := openGroup(path, true)
	if openErr != nil {
		// A failed acquisition still removes the empty directory it just created.
		if err == nil {
			openErr = errors.Join(openErr, os.Remove(path))
		}
		return nil, openErr
	}
	g.children[name] = child
	return child, nil
}
func (g *Group) EnableControllers(names ...string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed || g.retiring {
		return os.ErrClosed
	}
	if err := g.current(); err != nil {
		return err
	}
	return enableControllers(g.path, names...)
}

// Open returns a caller-owned descriptor for process placement.
func (g *Group) Open() (*os.File, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed || g.retiring {
		return nil, os.ErrClosed
	}
	if err := g.current(); err != nil {
		return nil, err
	}
	return os.Open(g.path)
}
func (g *Group) current() error {
	expected, err := g.dir.Stat()
	if err != nil {
		return err
	}
	actual, err := os.Stat(g.path)
	if err != nil {
		return err
	}
	if !os.SameFile(expected, actual) {
		return fmt.Errorf("cgroup allocation replaced: %s", g.path)
	}
	return nil
}

// Close kills remaining processes and removes owned descendants. Failed cleanup
// retains the handle for retry. The borrowed hierarchy root itself is untouched.
func (g *Group) Close() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return nil
	}
	g.retiring = true
	if err := g.current(); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	} else {
		// Include groups inherited across daemon restarts, which have no live handle.
		entries, err := os.ReadDir(g.path)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			if !g.owned && g.children[e.Name()] == nil {
				continue
			}
			child := g.children[e.Name()]
			if child == nil {
				child, err = openGroup(filepath.Join(g.path, e.Name()), true)
				if err != nil {
					return err
				}
				g.children[e.Name()] = child
			}
			if err := child.Close(); err != nil {
				return err
			}
		}
		if g.owned {
			if err := removeGroup(g.path); err != nil {
				return err
			}
		}
	}
	g.closed = true
	return g.dir.Close()
}
func removeGroup(path string) error {
	// Do not create a regular file when the kernel does not expose cgroup.kill.
	file, err := os.OpenFile(filepath.Join(path, "cgroup.kill"), os.O_WRONLY, 0)
	if err == nil {
		_, err = file.WriteString("1")
		err = errors.Join(err, file.Close())
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("kill cgroup %s: %w", path, err)
	}
	for range 20 {
		err := os.Remove(path)
		if err == nil || errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if !errors.Is(err, syscall.EBUSY) {
			return fmt.Errorf("remove cgroup %s: %w", path, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("remove cgroup %s: still populated", path)
}
