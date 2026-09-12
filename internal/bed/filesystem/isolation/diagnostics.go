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

package isolation

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ObservedInt preserves both a successfully read integer and a read failure.
// A missing host knob is not the same fact as a present knob set to zero.
type ObservedInt struct {
	Value     *int64 `json:"value"`
	ReadError string `json:"read_error"`
}

// ObservedString preserves raw text from a host pseudo-file.
type ObservedString struct {
	Value     *string `json:"value"`
	ReadError string  `json:"read_error"`
}

type RuntimeFacts struct {
	OS            string `json:"os"`
	Arch          string `json:"arch"`
	KernelRelease string `json:"kernel_release"`
}

type CapabilityFacts struct {
	Inheritable string `json:"inheritable"`
	Permitted   string `json:"permitted"`
	Effective   string `json:"effective"`
	Bounding    string `json:"bounding"`
	Ambient     string `json:"ambient"`
}

type ProcessFacts struct {
	EUID            int             `json:"euid"`
	EGID            int             `json:"egid"`
	Capabilities    CapabilityFacts `json:"capabilities"`
	NoNewPrivs      *int64          `json:"no_new_privs"`
	SeccompMode     *int64          `json:"seccomp_mode"`
	SeccompFilters  *int64          `json:"seccomp_filters"`
	StatusReadError string          `json:"status_read_error"`
}

type SecurityModuleFacts struct {
	LSMList         ObservedString `json:"lsm_list"`
	ProcessLabel    ObservedString `json:"process_label"`
	AppArmorCurrent ObservedString `json:"apparmor_current"`
}

type NamespaceLimitFacts struct {
	User                    ObservedInt `json:"max_user_namespaces"`
	Mount                   ObservedInt `json:"max_mnt_namespaces"`
	PID                     ObservedInt `json:"max_pid_namespaces"`
	IPC                     ObservedInt `json:"max_ipc_namespaces"`
	UTS                     ObservedInt `json:"max_uts_namespaces"`
	Network                 ObservedInt `json:"max_net_namespaces"`
	Cgroup                  ObservedInt `json:"max_cgroup_namespaces"`
	UnprivilegedUsernsClone ObservedInt `json:"unprivileged_userns_clone"`
}

type KernelFeatureFacts struct {
	LandlockABI            ObservedInt `json:"landlock_abi"`
	CgroupV2               bool        `json:"cgroup_v2"`
	ProcSelfStatusReadable bool        `json:"proc_self_status_readable"`
}

// PtraceFacts retain the kernel policy input needed to assess ptrace-based
// workspace views such as proot. Capability masks and seccomp remain under
// ProcessFacts because they affect more than ptrace alone.
type PtraceFacts struct {
	YamaScope ObservedInt `json:"yama_scope"`
}

// SystemFacts are raw, boot-time observations. They deliberately contain no
// missing-permission classification or remediation advice.
type SystemFacts struct {
	Runtime         RuntimeFacts        `json:"runtime"`
	Process         ProcessFacts        `json:"process"`
	SecurityModules SecurityModuleFacts `json:"security_modules"`
	NamespaceLimits NamespaceLimitFacts `json:"namespace_limits"`
	KernelFeatures  KernelFeatureFacts  `json:"kernel_features"`
	Ptrace          PtraceFacts         `json:"ptrace"`
}

// ProbeReport records one boot probe without interpreting why it passed or
// failed. ExitCode remains null when no child process reached an exit status.
type ProbeReport struct {
	ConfiguredPath string `json:"configured_path"`
	ResolvedPath   string `json:"resolved_path"`
	Exists         bool   `json:"exists"`
	Executable     bool   `json:"executable"`
	Attempted      bool   `json:"attempted"`
	ExitCode       *int   `json:"exit_code"`
	Stdout         string `json:"stdout"`
	Stderr         string `json:"stderr"`
	Error          string `json:"error"`
	DurationMS     int64  `json:"duration_ms"`
}

// discoverExecutable records packaging facts before any runtime prerequisite
// is considered. A helper can therefore be present and executable even when a
// separate ptrace or namespace probe prevents its smoke test from running.
func discoverExecutable(configured string) ProbeReport {
	report := ProbeReport{ConfiguredPath: configured}
	resolved, err := exec.LookPath(configured)
	if err != nil {
		report.Error = "find binary: " + err.Error()
		resolved = existingCommandPath(configured)
		if resolved == "" {
			return report
		}
	}
	abs, absErr := filepath.Abs(resolved)
	if absErr != nil {
		report.Error = "resolve binary: " + absErr.Error()
		return report
	}
	report.ResolvedPath = abs
	info, statErr := os.Stat(abs)
	if statErr != nil {
		report.Error = "stat binary: " + statErr.Error()
		return report
	}
	report.Exists = true
	report.Executable = !info.IsDir() && info.Mode().Perm()&0o111 != 0
	if !report.Executable && err == nil {
		report.Error = "binary is not executable"
	}
	return report
}

func existingCommandPath(configured string) string {
	if strings.ContainsRune(configured, os.PathSeparator) {
		if _, err := os.Stat(configured); err == nil {
			return configured
		}
		return ""
	}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			dir = "."
		}
		candidate := filepath.Join(dir, configured)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

func withExecutionProbe(discovery, execution ProbeReport) ProbeReport {
	execution.ConfiguredPath = discovery.ConfiguredPath
	execution.ResolvedPath = discovery.ResolvedPath
	execution.Exists = discovery.Exists
	execution.Executable = discovery.Executable
	return execution
}

// DiagnosticsReport is the immutable boot snapshot served by the diagnostics
// endpoint. Reading it must never rerun a mechanism probe.
type DiagnosticsReport struct {
	System SystemFacts            `json:"system"`
	Probes map[string]ProbeReport `json:"probes"`
}

func runExecProbe(cmd *exec.Cmd) ProbeReport {
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	started := time.Now()
	err := cmd.Run()
	report := ProbeReport{
		Attempted:  true,
		Stdout:     stdout.String(),
		Stderr:     stderr.String(),
		DurationMS: time.Since(started).Milliseconds(),
	}
	if err == nil {
		code := 0
		report.ExitCode = &code
		return report
	}
	report.Error = err.Error()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code := exitErr.ExitCode()
		report.ExitCode = &code
	}
	return report
}

func (p ProbeReport) failed() bool {
	return p.Error != "" || (p.ExitCode != nil && *p.ExitCode != 0)
}

func (p ProbeReport) succeeded() bool {
	return p.Attempted && p.Error == "" && p.ExitCode != nil && *p.ExitCode == 0
}
