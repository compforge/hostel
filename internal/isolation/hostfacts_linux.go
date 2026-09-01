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

//go:build linux

package isolation

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	lls "github.com/landlock-lsm/go-landlock/landlock/syscall"
	"golang.org/x/sys/unix"
)

// Linux capability bit numbers the isolation mechanisms gate on.
const (
	capCHOWN  uint = 0
	capSETGID uint = 6
	capSETUID uint = 7
)

// osFacts fills the Linux-only host facts. Each probe degrades to a zero value
// on error, which simply makes the dependent mechanism report unavailable — the
// resolver then floors honestly.
func osFacts() HostFacts {
	var f HostFacts
	landlockABI := ObservedInt{}
	if v, err := lls.LandlockGetABIVersion(); err == nil {
		f.LandlockABI = int(v)
		value := int64(v)
		landlockABI.Value = &value
	} else {
		landlockABI.ReadError = err.Error()
	}
	process, effectiveCaps := readProcessFacts()
	f.EffectiveCaps = effectiveCaps
	f.KernelRelease = kernelRelease()
	unprivilegedUsernsClone := readObservedInt("/proc/sys/kernel/unprivileged_userns_clone")
	f.UnprivilegedUserns = unprivilegedUsernsClone.Value != nil && *unprivilegedUsernsClone.Value == 1
	f.CgroupV2 = cgroupV2()
	f.AppArmorProfile = apparmorProfile()
	f.diagnostics = SystemFacts{
		Process: process,
		SecurityModules: SecurityModuleFacts{
			LSMList:         readObservedString("/sys/kernel/security/lsm"),
			ProcessLabel:    readObservedString("/proc/self/attr/current"),
			AppArmorCurrent: readObservedString("/proc/self/attr/apparmor/current"),
		},
		NamespaceLimits: NamespaceLimitFacts{
			User:                    readObservedInt("/proc/sys/user/max_user_namespaces"),
			Mount:                   readObservedInt("/proc/sys/user/max_mnt_namespaces"),
			PID:                     readObservedInt("/proc/sys/user/max_pid_namespaces"),
			IPC:                     readObservedInt("/proc/sys/user/max_ipc_namespaces"),
			UTS:                     readObservedInt("/proc/sys/user/max_uts_namespaces"),
			Network:                 readObservedInt("/proc/sys/user/max_net_namespaces"),
			Cgroup:                  readObservedInt("/proc/sys/user/max_cgroup_namespaces"),
			UnprivilegedUsernsClone: unprivilegedUsernsClone,
		},
		KernelFeatures: KernelFeatureFacts{
			LandlockABI:            landlockABI,
			CgroupV2:               f.CgroupV2,
			ProcSelfStatusReadable: process.StatusReadError == "",
		},
	}
	return f
}

func readObservedInt(path string) ObservedInt {
	data, err := os.ReadFile(path)
	if err != nil {
		return ObservedInt{ReadError: err.Error()}
	}
	value, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return ObservedInt{ReadError: fmt.Sprintf("parse %s: %v", path, err)}
	}
	return ObservedInt{Value: &value}
}

func readObservedString(path string) ObservedString {
	data, err := os.ReadFile(path)
	if err != nil {
		return ObservedString{ReadError: err.Error()}
	}
	value := strings.TrimSpace(string(data))
	return ObservedString{Value: &value}
}

func readProcessFacts() (ProcessFacts, uint64) {
	facts := ProcessFacts{}
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		facts.StatusReadError = err.Error()
		return facts, 0
	}
	values := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if ok {
			values[key] = strings.TrimSpace(value)
		}
	}
	facts.Capabilities = CapabilityFacts{
		Inheritable: values["CapInh"],
		Permitted:   values["CapPrm"],
		Effective:   values["CapEff"],
		Bounding:    values["CapBnd"],
		Ambient:     values["CapAmb"],
	}
	facts.NoNewPrivs = parseStatusInt(values["NoNewPrivs"])
	facts.SeccompMode = parseStatusInt(values["Seccomp"])
	facts.SeccompFilters = parseStatusInt(values["Seccomp_filters"])
	effective, _ := strconv.ParseUint(values["CapEff"], 16, 64)
	return facts, effective
}

func parseStatusInt(raw string) *int64 {
	if raw == "" {
		return nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return nil
	}
	return &value
}

// apparmorProfile reads this process's AppArmor confinement label. "unconfined"
// and absence both normalize to "" — only an actual confining profile is a fact
// worth surfacing.
//
// The AppArmor-specific attr node (Linux 4.17+) is authoritative: if it exists,
// AppArmor is the active LSM and its value is the answer. The legacy shared node
// (/proc/self/attr/current) is LSM-ambiguous — SELinux writes its own label
// there too — so we read it only when AppArmor is actually present, else an
// SELinux-only host would mislabel a SELinux context as an AppArmor profile.
func apparmorProfile() string {
	if data, err := os.ReadFile("/proc/self/attr/apparmor/current"); err == nil {
		return normalizeAppArmorLabel(string(data))
	}
	if _, err := os.Stat("/sys/kernel/security/apparmor"); err == nil {
		if data, err := os.ReadFile("/proc/self/attr/current"); err == nil {
			return normalizeAppArmorLabel(string(data))
		}
	}
	return ""
}

func normalizeAppArmorLabel(raw string) string {
	label := strings.TrimSpace(raw)
	if label == "" || label == "unconfined" {
		return ""
	}
	return label
}

func kernelRelease() string {
	var u unix.Utsname
	if err := unix.Uname(&u); err != nil {
		return ""
	}
	return unix.ByteSliceToString(u.Release[:])
}

func cgroupV2() bool {
	_, err := os.Stat("/sys/fs/cgroup/cgroup.controllers")
	return err == nil
}
