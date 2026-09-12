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

package manager

import (
	"context"
	"time"

	"github.com/qiankunli/hostel/internal/bed/filesystem/bedfs"
	"github.com/qiankunli/hostel/internal/bed/store"
)

// StartTransfer pins a resident Bed for the complete file operation. A replay
// only reads its original status and does not reinitialize or dirty the Bed.
func (m *Manager) StartTransfer(ctx context.Context, bedID string, request store.TransferRequest) (store.Transfer, error) {
	return m.store.StartTransfer(ctx, bedID, request, func() (*bedfs.FS, func(), error) {
		b, ok := m.Get(bedID)
		if !ok {
			return nil, nil, ErrBedUnavailable
		}
		timeout := request.Timeout
		if timeout == 0 {
			timeout = store.DefaultTransferTimeout
		}
		finish, err := m.BeginOperation(b, OpFile, timeout)
		if err != nil {
			return nil, nil, err
		}
		return b.BedFS(), finish, nil
	})
}

func (m *Manager) TransferStatus(bedID, id string) (store.Transfer, error) {
	return m.store.TransferStatus(bedID, id)
}
func (m *Manager) CancelTransfer(bedID, id, instanceID string) (store.Transfer, error) {
	return m.store.CancelTransfer(bedID, id, instanceID)
}
func (m *Manager) TransferInstanceID() string { return m.store.TransferInstanceID() }
func (m *Manager) TransfersConfigured() bool  { return m.store.TransfersConfigured() }

func (m *Manager) stopBedTransfers(bedID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return m.store.StopTransfers(ctx, bedID)
}
