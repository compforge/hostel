// Package filesystem provides process filesystem views and mount mechanisms.
// Callers supply paths and composition order; no path has application meaning.
package filesystem

import (
	"context"
	"os/exec"
)

// Mapping projects a host source into a process-visible target. It does not
// establish a security boundary: pathshim and PRoot only translate paths.
type Mapping struct{ Source, Target string }

type Pathshim struct{ Path string }

func (p Pathshim) Wrap(cmd *exec.Cmd, mappings []Mapping, cwd string) {
	args := appendPathshimBinds([]string{p.Path, "--quiet"}, mappings)
	args = append(args, "--cwd", cwd, "--")
	cmd.Args = append(args, cmd.Args...)
	cmd.Path = p.Path
}

// ProbeCommand uses the pathshim v0.1.6 probe protocol pinned by the image.
// The caller may wrap this command to probe its complete execution boundary.
func (p Pathshim) ProbeCommand(ctx context.Context, mappings []Mapping) *exec.Cmd {
	return exec.CommandContext(ctx, p.Path, appendPathshimBinds([]string{"probe"}, mappings)...)
}
func appendPathshimBinds(args []string, mappings []Mapping) []string {
	for _, m := range mappings {
		args = append(args, "--bind", m.Source+":"+m.Target)
	}
	return args
}

type PRoot struct{ Path string }

func (p PRoot) Wrap(cmd *exec.Cmd, mappings []Mapping, cwd string) {
	args := []string{p.Path, "-v", "-1"}
	for _, m := range mappings {
		// The suffix preserves the requested target instead of resolving its symlink.
		args = append(args, "-b", m.Source+":"+m.Target+"!")
	}
	args = append(args, "-w", cwd)
	cmd.Args = append(args, cmd.Args...)
	cmd.Path = p.Path
}
