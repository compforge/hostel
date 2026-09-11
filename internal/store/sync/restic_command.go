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

package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path"
	"strings"
	"time"

	"github.com/qiankunli/hostel/internal/store/backend"
)

const resticVersion = "0.19.1"
const resticOutputLimit = 1 << 20

// Restic owns the format tool, using the same remote configuration as
// Store's object client. No shell or caller-supplied command is involved.
type Restic struct {
	cfg      ResticConfig
	initGate chan struct{}
}

func NewRestic(cfg ResticConfig) *Restic {
	if cfg.Binary == "" {
		cfg.Binary = "restic"
	}
	return &Restic{cfg: cfg, initGate: make(chan struct{}, 1)}
}

func (r *Restic) Available(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, r.cfg.Binary, "version").Output()
	if err != nil {
		return fmt.Errorf("%w: restic %s is required", ErrTransferUnavailable, resticVersion)
	}
	fields := strings.Fields(string(output))
	if len(fields) < 2 || fields[0] != "restic" || fields[1] != resticVersion {
		return fmt.Errorf("%w: restic %s is required", ErrTransferUnavailable, resticVersion)
	}
	return nil
}

func (r *Restic) repository(key string) string {
	endpoint := strings.TrimRight(r.cfg.Endpoint, "/")
	if endpoint == "" {
		endpoint = "https://s3.amazonaws.com"
	}
	segments := strings.Split(path.Join(r.cfg.Bucket, r.cfg.Prefix, key), "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return "s3:" + endpoint + "/" + strings.Join(segments, "/")
}

// Environment controls cannot override the manager's repository or credentials.
func (r *Restic) environment() []string {
	var env []string
	for _, value := range os.Environ() {
		if strings.HasPrefix(value, "RESTIC_") || strings.HasPrefix(value, "AWS_") || strings.HasPrefix(value, "HOSTEL_") {
			continue
		}
		env = append(env, value)
	}
	env = append(env, "AWS_ACCESS_KEY_ID="+r.cfg.AccessKeyID, "AWS_SECRET_ACCESS_KEY="+r.cfg.SecretAccessKey, "AWS_SESSION_TOKEN="+r.cfg.SessionToken, "AWS_DEFAULT_REGION="+r.cfg.Region)
	if r.cfg.Password != "" {
		env = append(env, "RESTIC_PASSWORD="+r.cfg.Password)
	}
	return env
}

type resticCommandError struct{ code int }

func (e *resticCommandError) Error() string {
	return fmt.Sprintf("restic command failed (exit %d)", e.code)
}

func (r *Restic) run(ctx context.Context, repo, dir string, args ...string) ([]byte, error) {
	global := []string{"--repo", repo, "--no-cache", "--json", "--quiet", "--compression", "auto"}
	if r.cfg.Password == "" {
		global = append(global, "--insecure-no-password")
	}
	lookup := "dns"
	if r.cfg.PathStyle {
		lookup = "path"
	}
	if strings.HasPrefix(repo, "s3:") {
		global = append(global, "-o", "s3.bucket-lookup="+lookup, "-o", "s3.connections=4")
	}
	cmd := exec.CommandContext(ctx, r.cfg.Binary, append(global, args...)...)
	cmd.Dir, cmd.Env = dir, r.environment()
	// Restic handles SIGINT by releasing repository locks. Bound that grace
	// period, and join the process before the Transfer releases its Bed pin.
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 2 * time.Second
	var output resticOutput
	cmd.Stdout = &output
	cmd.Stderr = io.Discard // Transport messages can contain endpoints and credentials.
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return nil, &resticCommandError{code: exit.ExitCode()}
		}
		return nil, fmt.Errorf("%w: cannot execute restic", ErrTransferUnavailable)
	}
	if output.truncated {
		return nil, errors.New("restic output limit exceeded")
	}
	return output.Bytes(), nil
}

type resticOutput struct {
	bytes.Buffer
	truncated bool
}

func (b *resticOutput) Write(data []byte) (int, error) {
	n := len(data)
	remaining := resticOutputLimit - b.Len()
	if n > remaining {
		b.truncated = true
		data = data[:remaining]
	}
	_, _ = b.Buffer.Write(data)
	return n, nil
}

func (r *Restic) ensureRepository(ctx context.Context, repo string) error {
	select {
	case r.initGate <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-r.initGate }()
	_, err := r.run(ctx, repo, "", "cat", "config")
	if err == nil {
		return nil
	}
	var failure *resticCommandError
	if !errors.As(err, &failure) || failure.code != 10 {
		return err
	}
	_, err = r.run(ctx, repo, "", "init", "--repository-version", "2")
	return err
}

type resticSummary struct {
	MessageType string `json:"message_type"`
	SnapshotID  string `json:"snapshot_id"`
	Files       int64  `json:"total_files_processed"`
	Bytes       int64  `json:"total_bytes_processed"`
}

func (r *Restic) upload(ctx context.Context, repo, dir, parent string) (resticSummary, error) {
	if err := r.ensureRepository(ctx, repo); err != nil {
		return resticSummary{}, transferErrorAt(err, "init", ".")
	}
	args := []string{"backup", "--host", "hostel"}
	if parent != "" {
		args = append(args, "--parent", parent)
	}
	// Keep inode/ctime checks for the freshly staged tree. --force would discard
	// the explicit parent reference; restic still deduplicates re-read blocks.
	args = append(args, "--", ".")
	output, err := r.run(ctx, repo, dir, args...)
	if err != nil {
		return resticSummary{}, transferErrorAt(err, "upload", ".")
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	for {
		var summary resticSummary
		if err := decoder.Decode(&summary); err != nil {
			if err == io.EOF {
				break
			}
			return resticSummary{}, err
		}
		if summary.MessageType == "summary" && resticRefPattern.MatchString(summary.SnapshotID) {
			return summary, nil
		}
	}
	return resticSummary{}, errors.New("restic returned no snapshot reference")
}

func (r *Restic) download(ctx context.Context, repo, ref, dir string) error {
	_, err := r.run(ctx, repo, "", "restore", ref, "--target", dir)
	return transferErrorAt(err, "download", ".")
}

// ResticConfig configures the format tool and its remote repository location.
type ResticConfig struct {
	backend.Config
	Binary   string
	Password string
}
