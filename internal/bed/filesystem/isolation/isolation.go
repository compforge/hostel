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

// Package isolation confines a bed's processes. Filesystem LEVEL expresses a
// domain guarantee (shared/confined/private), while the MECHANISM
// (direct/uid/landlock/bwrap) is how it is realized on the
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
	"slices"

	"github.com/qiankunli/hostel/internal/bed/filesystem/bedfs"
	"github.com/qiankunli/hostel/internal/feature"
	hostfacts "github.com/qiankunli/hostel/internal/host/facts"
	hostfs "github.com/qiankunli/hostel/internal/host/filesystem"
)

// Level is a data-isolation guarantee, ordered weakest→strongest.
type Level int

const (
	// Shared — bunk room: no barrier between beds (organizational split only).
	Shared Level = iota
	// Confined — private room, shared toilet: a bed can't ACCESS others' data
	// (EACCES) but siblings stay visible and host paths (/tmp, /usr) are shared.
	Confined
	// Private — fully private: siblings invisible, private mount view, canonical
	// /workspace. Process env ownership is isolation-independent (bed/env.go).
	Private
)

func (l Level) String() string {
	switch l {
	case Confined:
		return "confined"
	case Private:
		return "private"
	default:
		return "shared"
	}
}

// parseRequest maps a config value to a requested level. "auto" (and "") means
// "as high as the environment allows" → Private.
func parseRequest(s string) Level {
	switch s {
	case "confined":
		return Confined
	case "private":
		return Private
	default: // "auto", "shared", unknown
		if s == "shared" {
			return Shared
		}
		return Private
	}
}

// Boundary contributes the mechanism-specific part of a Bed execution
// environment and reports its isolation guarantee. Filesystem projection is
// selected independently so a user-space path helper cannot masquerade as a
// stronger security boundary. The Bed manager always executes through
// Environment, which composes this boundary with the resolved BedUser and
// network attachment.
type Boundary interface {
	// Name is the mechanism: direct | uid | landlock | bwrap.
	Name() string
	// Level is the guarantee this mechanism delivers.
	Level() Level
	// Available reports whether the mechanism actually works on this host
	// (probed at construction). direct is always available.
	Available() bool
	// AllowsMappings reports whether external data can enter this boundary.
	AllowsMappings() bool
	// Wrap prepares cmd to run confined to fs. A mechanism reported Available
	// must NOT silently degrade here — failing to build the sandbox is an error.
	// cwd is a carrier BedFS path. An empty value means the bed workspace.
	// The mechanism projects it into its process view without rewriting the
	// caller's shell source.
	Wrap(cmd *exec.Cmd, fs *bedfs.FS, cwd string) error
}

// Isolator is the resolved file/isolation component used to build a Bed's
// Environment: one security Boundary plus one process-visible BedFS view.
type Isolator interface {
	Boundary
	View(*bedfs.FS) bedfs.ProcessView
	WorkdirMounted() bool
}

// ProcessViewReport describes the selected process path view and mapping support.
// It is separate from the isolation level: a user-space view
// improves path compatibility but does not add a security boundary.
type ProcessViewReport struct {
	Mode         string               `json:"mode"`
	Available    bool                 `json:"available"`
	Reason       string               `json:"reason,omitempty"`
	PathMappings bedfs.MappingSupport `json:"path_mappings"`
}

// Report is the boot-time resolution, exposed for capabilities/healthz: the
// resolution outcome and mechanism-specific probe evidence.
type Report interface {
	Requested() Level
	Effective() Level
	Ceiling() Level
	Mechanism() string
	ProcessView() ProcessViewReport
	Diagnostics() DiagnosticsReport
}

// Preparer is an optional Boundary capability: a mechanism that must prepare a
// bed's data dir before its commands run. uid isolation tightens directory
// traversal here; BedUser owns the common ownership handoff. Mount- and
// LSM-based mechanisms need no mechanism-specific on-disk prep. The bed manager
// calls Prepare after (re)creating the data dir. The resolved result always
// satisfies Preparer (no-op when the chosen mechanism isn't one).
type Preparer interface {
	Prepare(fs *bedfs.FS) error
}

// resolved composes independently selected security and process view backends and
// retains their boot-time resolution facts.
type resolved struct {
	boundary       Boundary
	process        processViewBackend
	req, eff, ceil Level
	processView    ProcessViewReport
	diagnostics    DiagnosticsReport
}

func (r *resolved) Name() string                        { return r.boundary.Name() }
func (r *resolved) Level() Level                        { return r.boundary.Level() }
func (r *resolved) AllowsMappings() bool                { return r.boundary.AllowsMappings() }
func (r *resolved) Available() bool                     { return r.boundary.Available() }
func (r *resolved) View(fs *bedfs.FS) bedfs.ProcessView { return r.process.View(fs) }
func (r *resolved) WorkdirMounted() bool                { return r.process.Mounted() }
func (r *resolved) Requested() Level                    { return r.req }
func (r *resolved) Effective() Level                    { return r.eff }
func (r *resolved) Ceiling() Level                      { return r.ceil }
func (r *resolved) Mechanism() string                   { return r.boundary.Name() }
func (r *resolved) ProcessView() ProcessViewReport      { return r.processView }
func (r *resolved) Diagnostics() DiagnosticsReport {
	probes := make(map[string]hostfacts.ProbeReport, len(r.diagnostics.Probes))
	for name, probe := range r.diagnostics.Probes {
		probes[name] = probe
	}
	features := make(map[string]feature.Status, len(r.diagnostics.Features))
	for name, report := range r.diagnostics.Features {
		report.Requirements.Capabilities = slices.Clone(report.Requirements.Capabilities)
		report.Requirements.Tools = slices.Clone(report.Requirements.Tools)
		report.Requirements.Conditions = slices.Clone(report.Requirements.Conditions)
		features[name] = report
	}
	return DiagnosticsReport{Probes: probes, Features: features}
}

// Prepare forwards to the chosen mechanism when it needs data-dir preparation
// (uid), else no-ops — so the bed manager can assert Preparer on the result
// unconditionally, without knowing which mechanism won.
func (r *resolved) Prepare(fs *bedfs.FS) error {
	if p, ok := r.boundary.(Preparer); ok {
		return p.Prepare(fs)
	}
	return nil
}

func (r *resolved) Wrap(cmd *exec.Cmd, fs *bedfs.FS, cwd string) error {
	return wrapRuntimeCommand(r.boundary, r.process, cmd, fs, cwd)
}

// New is the default-policy constructor for valid file levels. Call Resolve
// when accepting external configuration or requiring individual features.
//
// +spec=`effective isolation is the strongest available level not exceeding the request, and requested/effective/ceiling remain observable.`
// +case:id=isolation_level_boundaries,desc=`Run the same sibling-path probe under shared, confined, and private file requests`,expect=`shared permits, confined denies, private hides, and unavailable levels degrade honestly`
func New(facts hostfacts.Snapshot, requested, workspaceRoot string) Isolator {
	iso, err := Resolve(facts, Config{Level: requested}, workspaceRoot)
	if err != nil {
		panic(err) // Invalid programmer-supplied level; external config uses Resolve.
	}
	return iso
}

// Resolve selects features once. Off excludes probes; Required constrains selection
// rather than merely asserting availability. Runtime failures never reopen selection.
// +spec=`Feature policies restrict selection without inventing host facts; every Required feature must be selected before readiness.`
func Resolve(facts hostfacts.Snapshot, config Config, workspaceRoot string) (Isolator, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	req := parseRequest(config.Level)
	policies := config.policies()
	probes := map[string]hostfacts.ProbeReport{}
	reports := map[string]feature.Status{}
	ptraceProbe := hostfacts.ProbeReport{}
	if config.PRoot.Effective() != feature.Off {
		ptraceProbe = hostfs.ProbePtrace()
	}
	probes["ptrace"] = ptraceProbe
	candidates := []Boundary{}
	for _, name := range []string{"bwrap", "landlock", "uid"} {
		policy := policies[name]
		if policy == feature.Off {
			reports[name] = feature.Describe(policy, requirements(name), false, false, false, "")
			probes[name] = hostfacts.ProbeReport{}
			continue
		}
		var candidate Isolator
		var probe hostfacts.ProbeReport
		switch name {
		case "bwrap":
			candidate, probe = newBwrap(facts, workspaceRoot)
		case "landlock":
			candidate, probe = newLandlock(facts, workspaceRoot)
		case "uid":
			if config.DedicatedIdentity {
				candidate, probe = newUID(facts, workspaceRoot)
			} else {
				candidate, probe = unavailable{name: "uid", lvl: Confined}, hostfacts.ProbeReport{Error: "dedicated Bed identity unavailable"}
			}
		}
		probes[name] = probe
		reports[name] = feature.Describe(policy, requirements(name), true, candidate.Available(), false, probe.Error)
		candidates = append(candidates, candidate)
	}
	chosen, ceiling := selectBoundary(config, candidates)
	for _, name := range []string{"bwrap", "landlock", "uid"} {
		report := reports[name]
		report.Selected = chosen.Name() == name
		reports[name] = report
		if err := report.CheckRequired("filesystem." + name); err != nil {
			return nil, err
		}
	}
	workspace, view := resolveProcessViewWithConfig(chosen, workspaceRoot, ptraceProbe, probes, config)
	for _, name := range []string{"proot", "pathshim"} {
		probe := probes[name]
		reason := probe.Error
		if name == "proot" && view.Mode != "mount" && !ptraceProbe.Succeeded() && reason == "" {
			reason = rawProbeFailure(ptraceProbe)
		}
		reports[name] = feature.Describe(policies[name], requirements(name), probe.Attempted || reason != "", probe.Succeeded(), view.Mode == name, reason)
		if err := reports[name].CheckRequired("filesystem." + name); err != nil {
			return nil, err
		}
	}
	log.Printf("isolation: requested=%s effective=%s observed_ceiling=%s feature=%s workspace_view=%s", req, chosen.Level(), ceiling, chosen.Name(), view.Mode)
	for name, report := range reports {
		if report.Policy != feature.Auto {
			log.Printf("filesystem: feature=%s policy=%s selected=%t reason=%s", name, report.Policy, report.Selected, report.Reason)
		}
	}
	return &resolved{boundary: chosen, process: workspace, req: req, eff: chosen.Level(), ceil: ceiling, processView: view,
		diagnostics: DiagnosticsReport{Probes: probes, Features: reports}}, nil
}

// unavailable is a mechanism that probed as not usable on this host. It keeps
// its Level so the resolver can still compute the ceiling correctly, but is
// never chosen (Available()=false) and refuses to Wrap.
type unavailable struct {
	name string
	lvl  Level
}

func (u unavailable) Name() string                        { return u.name }
func (u unavailable) Level() Level                        { return u.lvl }
func (u unavailable) AllowsMappings() bool                { return false }
func (u unavailable) Available() bool                     { return false }
func (u unavailable) View(fs *bedfs.FS) bedfs.ProcessView { return bedfs.HostView(fs) }
func (u unavailable) WorkdirMounted() bool                { return false }
func (u unavailable) Wrap(*exec.Cmd, *bedfs.FS, string) error {
	return errUnavailable
}

var errUnavailable = &isoError{"isolation: mechanism unavailable"}

type isoError struct{ msg string }

func (e *isoError) Error() string { return e.msg }

// direct runs the command straight on the host, only pinning its cwd to the
// bed workspace. The shared level: no enforced isolation. Always available.
type direct struct{}

func (direct) Name() string                        { return "direct" }
func (direct) Level() Level                        { return Shared }
func (direct) AllowsMappings() bool                { return true }
func (direct) Available() bool                     { return true }
func (direct) View(fs *bedfs.FS) bedfs.ProcessView { return bedfs.HostView(fs) }
func (direct) WorkdirMounted() bool                { return false }
func (direct) Wrap(cmd *exec.Cmd, fs *bedfs.FS, cwd string) error {
	cmd.Dir = commandCwd(fs, cwd)
	return nil
}

func commandCwd(fs *bedfs.FS, cwd string) string {
	if cwd == "" {
		return fs.Workdir()
	}
	return cwd
}

// selectBoundary owns priority and mutual exclusion. Required means adoption,
// even when an automatically preferred boundary also works.
func selectBoundary(config Config, candidates []Boundary) (Boundary, Level) {
	req := parseRequest(config.Level)
	policies := config.policies()
	ceiling := Shared
	var chosen Boundary = direct{}
	var forced Boundary
	for _, m := range candidates {
		if policies[m.Name()] == feature.Off || !m.Available() {
			continue
		}
		if m.Level() > ceiling {
			ceiling = m.Level()
		}
		// A failed composition changes eligibility, not the standalone host fact.
		if config.Excluded[m.Name()] != "" && policies[m.Name()] != feature.Required {
			continue
		}
		if policies[m.Name()] == feature.Required {
			forced = m
		}
		// A required user-space view excludes bwrap's mutually exclusive mount view.
		if m.Name() == "bwrap" && (config.PRoot == feature.Required || config.Pathshim == feature.Required) {
			continue
		}
		if m.Level() <= req && m.Level() > chosen.Level() {
			chosen = m
		}
	}
	if forced != nil {
		chosen = forced
	}
	return chosen, ceiling
}
