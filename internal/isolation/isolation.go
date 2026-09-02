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

// Package isolation confines a bed's processes. Data isolation is graded into
// "hostel room types" (docs/data.md): the LEVEL is the north-facing
// guarantee, the MECHANISM (direct/landlock/bwrap) is how it's realized on the
// current host. A request expresses a wish; the effective level is capped by
// what the environment can actually deliver:
//
//	effective = highest achievable level ≤ requested
//
// so "auto" yields the environment's ceiling, and an explicit lower request is
// an honest, deliberate downgrade.
package isolation

import (
	"log"
	"os/exec"

	"github.com/qiankunli/hostel/internal/bedfs"
)

// Level is a data-isolation guarantee, ordered weakest→strongest.
type Level int

const (
	// Dorm — bunk room: no barrier between beds (organizational split only).
	Dorm Level = iota
	// Room — private room, shared toilet: a bed can't ACCESS others' data
	// (EACCES) but siblings stay visible and host paths (/tmp, /usr) are shared.
	Room
	// Suite — fully private: siblings invisible, private mount view, canonical
	// /workspace. Process env ownership is isolation-independent (bed/env.go).
	Suite
)

func (l Level) String() string {
	switch l {
	case Room:
		return "room"
	case Suite:
		return "suite"
	default:
		return "dorm"
	}
}

// parseRequest maps a config value to a requested level. "auto" (and "") means
// "as high as the environment allows" → Suite.
func parseRequest(s string) Level {
	switch s {
	case "room":
		return Room
	case "suite":
		return Suite
	default: // "auto", "dorm", unknown
		if s == "dorm" {
			return Dorm
		}
		return Suite
	}
}

// Boundary confines an exec.Cmd and reports the isolation guarantee it
// provides. Filesystem projection is selected independently so a user-space
// path helper cannot masquerade as a stronger security boundary.
type Boundary interface {
	// Name is the mechanism: direct | landlock | bwrap.
	Name() string
	// Level is the guarantee this mechanism delivers.
	Level() Level
	// Available reports whether the mechanism actually works on this host
	// (probed at construction). direct is always available.
	Available() bool
	// Wrap prepares cmd to run confined to fs. A mechanism reported Available
	// must NOT silently degrade here — failing to build the sandbox is an error.
	// cwd is a carrier BedFS path. An empty value means the bed workspace.
	// The mechanism projects it into its process view without rewriting the
	// caller's shell source.
	Wrap(cmd *exec.Cmd, fs *bedfs.FS, cwd string) error
}

// Isolator is the resolved runtime consumed by beds: one security Boundary
// plus one process-visible BedFS view.
type Isolator interface {
	Boundary
	View(*bedfs.FS) bedfs.View
	WorkspaceMounted() bool
}

// WorkspaceViewReport describes what a command sees at the canonical
// /workspace path. It is separate from the isolation level: a user-space view
// improves path compatibility but does not add a security boundary.
type WorkspaceViewReport struct {
	Mode      string `json:"mode"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

// Report is the boot-time resolution, exposed for capabilities/healthz: the
// resolution outcome plus the host facts it was resolved from.
type Report interface {
	Requested() Level
	Effective() Level
	Ceiling() Level
	Mechanism() string
	Facts() HostFacts
	WorkspaceView() WorkspaceViewReport
	Diagnostics() DiagnosticsReport
}

type options struct {
	projections []bedfs.PathProjection
}

// Option configures process-view projection without changing the requested
// isolation level.
type Option func(*options)

// WithPathProjections configures business-neutral BedFS-to-Executor path
// projections. Callers validate the set before constructing the runtime.
func WithPathProjections(projections []bedfs.PathProjection) Option {
	return func(o *options) { o.projections = append([]bedfs.PathProjection(nil), projections...) }
}

// Preparer is an optional Boundary capability: a mechanism that must prepare a
// bed's data dir before its commands run. uid isolation implements it (chown
// the dir to the bed's dedicated uid); mount- and LSM-based mechanisms need no
// on-disk prep and don't. The bed manager calls Prepare after (re)creating the
// data dir. The resolved result always satisfies Preparer (no-op when the
// chosen mechanism isn't one), so callers can assert unconditionally.
type Preparer interface {
	Prepare(fs *bedfs.FS) error
}

// resolved composes independently selected security and workspace backends and
// retains their boot-time resolution facts.
type resolved struct {
	boundary       Boundary
	workspace      workspaceBackend
	req, eff, ceil Level
	facts          HostFacts
	workspaceView  WorkspaceViewReport
	diagnostics    DiagnosticsReport
	projections    []bedfs.PathProjection
}

func (r *resolved) Name() string                       { return r.boundary.Name() }
func (r *resolved) Level() Level                       { return r.boundary.Level() }
func (r *resolved) Available() bool                    { return r.boundary.Available() }
func (r *resolved) View(fs *bedfs.FS) bedfs.View       { return r.workspace.View(fs) }
func (r *resolved) WorkspaceMounted() bool             { return r.workspace.Mounted() }
func (r *resolved) Requested() Level                   { return r.req }
func (r *resolved) Effective() Level                   { return r.eff }
func (r *resolved) Ceiling() Level                     { return r.ceil }
func (r *resolved) Mechanism() string                  { return r.boundary.Name() }
func (r *resolved) Facts() HostFacts                   { return r.facts }
func (r *resolved) WorkspaceView() WorkspaceViewReport { return r.workspaceView }
func (r *resolved) Diagnostics() DiagnosticsReport {
	probes := make(map[string]ProbeReport, len(r.diagnostics.Probes))
	for name, probe := range r.diagnostics.Probes {
		probes[name] = probe
	}
	return DiagnosticsReport{System: r.diagnostics.System, Probes: probes}
}

// Prepare forwards to the chosen mechanism when it needs data-dir preparation
// (uid), else no-ops — so the bed manager can assert Preparer on the result
// unconditionally, without knowing which mechanism won.
func (r *resolved) Prepare(fs *bedfs.FS) error {
	for _, projection := range r.projections {
		hostPath, err := fs.Resolve(projection.BedPath)
		if err != nil {
			return err
		}
		if err := fs.EnsureDir(hostPath); err != nil {
			return err
		}
	}
	if p, ok := r.boundary.(Preparer); ok {
		return p.Prepare(fs)
	}
	return nil
}

func (r *resolved) Wrap(cmd *exec.Cmd, fs *bedfs.FS, cwd string) error {
	return wrapRuntimeCommand(r.boundary, r.workspace, cmd, fs, cwd)
}

// New resolves the requested isolation level and workspace view against what
// the environment can deliver. The returned runtime also implements Report and
// is always usable — worst case it degrades to dorm/direct with carrier paths,
// which is logged honestly.
//
// +spec=`effective isolation is the strongest available level not exceeding the request, and requested/effective/ceiling remain observable.`
// +case:id=isolation_level_boundaries,desc=`Run the same sibling-path probe under dorm, room, and suite requests`,expect=`dorm shares, room denies, suite hides, and unavailable levels degrade honestly`
func New(requested, workspaceRoot string, opts ...Option) Isolator {
	cfg := options{}
	for _, option := range opts {
		option(&cfg)
	}
	req := parseRequest(requested)

	// One host probe shared by every mechanism (see HostFacts): they read it for
	// the cheap pre-check and keep their own boot smoke for the verdict.
	facts := collectHostFacts()

	// Candidate mechanisms, strongest first; within a level, preferred first
	// (the selection below keeps the first available at each level). direct
	// (dorm) is the always-available floor. Two mechanisms serve room: landlock
	// (kernel LSM, no privilege — preferred) and uid (Unix DAC, needs setuid
	// caps — the fallback where Landlock is absent, e.g. old/custom kernels).
	ptraceProbe := runPtraceProbe()
	bwrapCandidate, bwrapProbe := newBwrap(facts, workspaceRoot, cfg.projections)
	landlockCandidate, landlockProbe := newLandlock(facts, workspaceRoot)
	uidCandidate, uidProbe := newUID(facts, workspaceRoot)
	probes := map[string]ProbeReport{
		"bwrap":    bwrapProbe,
		"landlock": landlockProbe,
		"ptrace":   ptraceProbe,
		"uid":      uidProbe,
	}
	candidates := []Boundary{
		bwrapCandidate,    // suite
		landlockCandidate, // room — preferred
		uidCandidate,      // room — fallback
		direct{},          // dorm
	}

	ceiling := Dorm
	var chosen Boundary = direct{}
	eff := Dorm
	for _, m := range candidates {
		if !m.Available() {
			continue
		}
		if m.Level() > ceiling {
			ceiling = m.Level()
		}
		// Highest available level that does not exceed the request; among
		// equal-level mechanisms the FIRST-listed wins (strict >), so the
		// preferred realization of a level takes precedence over its fallback.
		if m.Level() <= req && m.Level() > eff {
			chosen = m
			eff = m.Level()
		}
	}

	if eff < req {
		log.Printf("isolation: requested %s but environment ceiling is %s — using %s (mechanism=%s)",
			req, ceiling, eff, chosen.Name())
	} else {
		log.Printf("isolation: level=%s mechanism=%s (requested=%s, ceiling=%s)",
			eff, chosen.Name(), req, ceiling)
	}
	workspace, workspaceView := resolveWorkspaceView(chosen, workspaceRoot, cfg.projections, ptraceProbe, probes)
	return &resolved{
		boundary:      chosen,
		workspace:     workspace,
		req:           req,
		eff:           eff,
		ceil:          ceiling,
		facts:         facts,
		workspaceView: workspaceView,
		diagnostics: DiagnosticsReport{
			System: facts.diagnostics,
			Probes: probes,
		},
		projections: append([]bedfs.PathProjection(nil), cfg.projections...),
	}
}

// unavailable is a mechanism that probed as not usable on this host. It keeps
// its Level so the resolver can still compute the ceiling correctly, but is
// never chosen (Available()=false) and refuses to Wrap.
type unavailable struct {
	name string
	lvl  Level
}

func (u unavailable) Name() string                 { return u.name }
func (u unavailable) Level() Level                 { return u.lvl }
func (u unavailable) Available() bool              { return false }
func (u unavailable) View(fs *bedfs.FS) bedfs.View { return bedfs.HostView(fs) }
func (u unavailable) WorkspaceMounted() bool       { return false }
func (u unavailable) Wrap(*exec.Cmd, *bedfs.FS, string) error {
	return errUnavailable
}

var errUnavailable = &isoError{"isolation: mechanism unavailable"}

type isoError struct{ msg string }

func (e *isoError) Error() string { return e.msg }

// direct runs the command straight on the host, only pinning its cwd to the
// bed workspace. The dorm level: no enforced isolation. Always available.
type direct struct{}

func (direct) Name() string                 { return "direct" }
func (direct) Level() Level                 { return Dorm }
func (direct) Available() bool              { return true }
func (direct) View(fs *bedfs.FS) bedfs.View { return bedfs.HostView(fs) }
func (direct) WorkspaceMounted() bool       { return false }
func (direct) Wrap(cmd *exec.Cmd, fs *bedfs.FS, cwd string) error {
	cmd.Dir = commandCwd(fs, cwd)
	return nil
}

func commandCwd(fs *bedfs.FS, cwd string) string {
	if cwd == "" {
		return fs.Workspace()
	}
	return cwd
}
