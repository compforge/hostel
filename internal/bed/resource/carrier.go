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

package resource

import (
	"fmt"
	"time"
)

// CarrierSnapshot is one aggregate resource snapshot for the container that
// carries hostel. Zero limits mean that cgroup dimension has no finite limit.
type CarrierSnapshot struct {
	CPUUsage           time.Duration
	CPULimitCores      float64
	MemoryCurrentBytes uint64
	MemoryLimitBytes   uint64
}

// Carrier reads the aggregate cgroup shared by hostel-system and all bed
// children. Unlike Tracker, it is read-only and does not require cgroup
// delegation, so ordinary Kubernetes containers can use it for admission.
type Carrier interface {
	Snapshot() (CarrierSnapshot, error)
}

// NewCarrier resolves the current container cgroup and degrades to a reader
// that reports its probe error when cgroup resource facts are unavailable.
func NewCarrier() Carrier { return newCarrier() }

type unavailableCarrier struct {
	reason string
}

func (c unavailableCarrier) Snapshot() (CarrierSnapshot, error) {
	return CarrierSnapshot{}, fmt.Errorf("carrier resources unavailable: %s", c.reason)
}
