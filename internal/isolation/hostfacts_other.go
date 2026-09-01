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

//go:build !linux

package isolation

// osFacts: the Linux-only probes (caps, Landlock, userns, cgroup) are all
// absent off Linux; only collectHostFacts's cross-platform bwrap lookup may set
// anything. Room/suite mechanisms then report unavailable and the resolver
// floors to dorm.
func osFacts() HostFacts {
	unsupportedInt := ObservedInt{ReadError: "unsupported operating system"}
	unsupportedString := ObservedString{ReadError: "unsupported operating system"}
	return HostFacts{
		diagnostics: SystemFacts{
			Process: ProcessFacts{StatusReadError: "unsupported operating system"},
			SecurityModules: SecurityModuleFacts{
				LSMList:         unsupportedString,
				ProcessLabel:    unsupportedString,
				AppArmorCurrent: unsupportedString,
			},
			NamespaceLimits: NamespaceLimitFacts{
				User:                    unsupportedInt,
				Mount:                   unsupportedInt,
				PID:                     unsupportedInt,
				IPC:                     unsupportedInt,
				UTS:                     unsupportedInt,
				Network:                 unsupportedInt,
				Cgroup:                  unsupportedInt,
				UnprivilegedUsernsClone: unsupportedInt,
			},
			KernelFeatures: KernelFeatureFacts{
				LandlockABI: unsupportedInt,
			},
		},
	}
}
