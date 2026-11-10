package filesystem

import (
	"context"
	"fmt"
	"github.com/qiankunli/hostel/internal/bed/filesystem/bedfs"
	"github.com/qiankunli/hostel/internal/bed/privilege"
	"log"
	"slices"
	"time"

	"github.com/qiankunli/hostel/internal/bed/tool"
	hostfs "github.com/qiankunli/hostel/internal/host/filesystem"
)

type SharedSoftwareStatus struct {
	Path    string      `json:"path"`
	GID     int         `json:"gid"`
	Tool    tool.Status `json:"tool"`
	Entries int         `json:"entries"`
}

const softwareRoot = bedfs.SoftwareRoot

type sharedDirectory struct {
	Path   string
	GID    int
	Policy tool.Policy
}

// SetSoftwareGroups binds actual selected credentials, not a caller's request.
func (m *Manager) SetSoftwareGroups(groups []int) {
	m.sharedSoftware = []sharedDirectory{{Path: softwareRoot, GID: privilege.SoftwareGID, Policy: tool.Auto}}
	m.softwareGroupAvailable = slices.Contains(groups, privilege.SoftwareGID)
}

func SoftwareEnvironment() map[string]string {
	return map[string]string{"PYTHONUSERBASE": softwareRoot, "NPM_CONFIG_PREFIX": softwareRoot, "UV_TOOL_DIR": softwareRoot + "/share/uv/tools", "UV_TOOL_BIN_DIR": softwareRoot + "/bin"}
}

// Start prepares Carrier-wide software once, before any Bed may execute. Close
// does not undo permissions or delete externally supplied Carrier software.
// +spec=`Only declared shared software trees are prepared; auto reports failures, required rejects startup. No Bed owns or recursively reinitializes the shared tree.`
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	if m.sharedStarted {
		m.mu.Unlock()
		return nil
	}
	m.sharedReports = nil
	m.mu.Unlock()
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	for _, d := range m.sharedSoftware {
		started := time.Now()
		requirements := tool.Requirements{Conditions: []string{"Linux POSIX default ACL", "directory metadata authority", "shared supplementary GID"}}
		report := SharedSoftwareStatus{Path: d.Path, GID: d.GID, Tool: tool.Describe(d.Policy, requirements, false, false, false, "")}
		if d.Policy.Effective() != tool.Off {
			count := 0
			var err error
			if !m.softwareGroupAvailable {
				err = fmt.Errorf("selected Bed identity cannot use the internal software group")
			} else {
				count, err = hostfs.PrepareSharedSoftware(ctx, d.Path, d.GID)
			}
			reason := ""
			if err != nil {
				reason = err.Error()
			}
			report.Entries = count
			report.Tool = tool.Describe(d.Policy, requirements, true, err == nil, err == nil, reason)
			log.Printf("filesystem: shared_software path=%q gid=%d policy=%s prepared=%t entries=%d duration_ms=%d reason=%q", d.Path, d.GID, d.Policy.Effective(), err == nil, count, time.Since(started).Milliseconds(), reason)
		}
		m.mu.Lock()
		m.sharedReports = append(m.sharedReports, report)
		m.mu.Unlock()
		if err := report.Tool.CheckRequired("filesystem.shared_software " + d.Path); err != nil {
			return err
		}
		if err := parent.Err(); err != nil {
			return fmt.Errorf("shared software preparation: %w", err)
		}
	}
	m.mu.Lock()
	m.sharedStarted = true
	m.mu.Unlock()
	return nil
}
