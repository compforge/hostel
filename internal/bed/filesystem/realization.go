package filesystem

import (
	"slices"

	"github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/filesystem/bedfs"
	"github.com/qiankunli/hostel/internal/bed/filesystem/isolation"
	"github.com/qiankunli/hostel/protocol"
)

// SetDedicatedIdentity consumes Privilege's actual selection before startup.
// Identity protection remains useful even when optional path mechanisms are off.
func (m *Manager) SetDedicatedIdentity(dedicated bool) { m.dedicatedIdentity = dedicated }

func (m *Manager) preparedRealization(b *bed.Bed) *protocol.FilesystemRealization {
	result := &protocol.FilesystemRealization{
		State: "prepared", PrivateData: "none", CoreAccess: "unix_permissions", FileAPI: "bed_identity",
	}
	if m.dedicatedIdentity {
		result.PrivateData = "uid"
	}
	switch m.isolator.Name() {
	case "bwrap", "landlock":
		result.PrivateData = m.isolator.Name()
		result.CoreAccess = "path_rules"
	case "uid":
		result.PrivateData = "uid"
	}
	m.mu.Lock()
	reports := slices.Clone(m.sharedReports)
	m.mu.Unlock()
	groups := b.Status().Privilege.SupplementaryGIDs
	for _, report := range reports {
		software := protocol.SoftwareAccessStatus{Path: report.Path, Prepared: report.Tool.Selected, Authorized: slices.Contains(groups, report.GID)}
		if !software.Prepared {
			software.Reason = "shared software permissions were not prepared"
		} else if !software.Authorized {
			software.Reason = "Bed identity does not include the software group"
		}
		result.Software = append(result.Software, software)
	}
	return result
}

func (m *Manager) realizedView(pillow string, fs *bedfs.FS) protocol.FilesystemViewStatus {
	view := m.isolator.View(fs)
	support := view.MappingSupport()
	mode, reason := "carrier", ""
	if m.isolator.MountsRoot() {
		mode = "mount"
	}
	if report, ok := m.isolator.(isolation.Report); ok {
		selected := report.ProcessView()
		mode, reason = selected.Mode, selected.Reason
	}
	return protocol.FilesystemViewStatus{Pillow: pillow, Mode: mode, Rootfs: view.Rootfs() == "/", ReadWriteMounts: support.ReadWrite, ReadOnlyMounts: support.ReadOnly, Reason: reason}
}

func (m *Manager) invalidateRealization(b *bed.Bed, state, reason string) {
	m.status.Update(b, func(s *bed.FilesystemStatus) {
		s.Prepared, s.Room = false, ""
		s.Realization = &protocol.FilesystemRealization{State: state, Reason: reason}
		for i := range s.Mounts {
			s.Mounts[i].ProcessMapped = false
			s.Mounts[i].Reason = reason
		}
	})
}
