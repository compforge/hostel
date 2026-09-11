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
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/qiankunli/hostel/internal/store/backend"
)

var (
	ErrTransferInvalid     = errors.New("invalid transfer")
	ErrTransferUnavailable = errors.New("S3 transfer unavailable")
	ErrTransferConflict    = backend.ErrConflict
)
var resticRefPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type TransferEndpoint struct {
	Ref  string
	Type string
	Path string
	Key  string
}

// TransferOptions describes one file operation, independent of task admission and history.
type TransferOptions struct {
	Sync        Kind
	Source      TransferEndpoint
	Destination TransferEndpoint
	ParentRef   string
	Overwrite   bool
}

// ValidateTransfer normalizes the policy and validates operation endpoints and references.
func ValidateTransfer(req TransferOptions) (TransferOptions, error) {
	invalid := func(message string) (TransferOptions, error) {
		return req, fmt.Errorf("%w: %s", ErrTransferInvalid, message)
	}
	if req.Sync == "" {
		req.Sync = KindCopy
	}
	if req.Sync != KindCopy && req.Sync != KindRestic {
		return invalid("sync must be copy or restic")
	}
	if req.Source.Type == req.Destination.Type {
		return invalid("one endpoint must be bed and the other s3")
	}
	for _, endpoint := range []TransferEndpoint{req.Source, req.Destination} {
		switch endpoint.Type {
		case "bed":
			if endpoint.Path == "" || endpoint.Key != "" || endpoint.Ref != "" {
				return invalid("bed endpoint requires path only")
			}
		case "s3":
			if endpoint.Path != "" || !validTransferKey(endpoint.Key) {
				return invalid("s3 endpoint requires a relative key without empty, dot or parent segments")
			}
		default:
			return invalid("endpoint type must be bed or s3")
		}
	}
	if req.Sync == KindCopy {
		if req.Source.Ref != "" || req.Destination.Ref != "" || req.ParentRef != "" {
			return invalid("copy does not accept snapshot references")
		}
	} else {
		if req.Destination.Ref != "" {
			return invalid("destination ref is assigned by the transfer")
		}
		if req.Source.Type == "s3" {
			if !resticRefPattern.MatchString(req.Source.Ref) || req.ParentRef != "" {
				return invalid("restic download requires a full source ref and no parent_ref")
			}
		} else if req.ParentRef != "" && !resticRefPattern.MatchString(req.ParentRef) {
			return invalid("parent_ref must be a full restic snapshot id")
		}
	}
	return req, nil
}

func validTransferKey(key string) bool {
	if key == "" || strings.ContainsAny(key, "\\\x00") {
		return false
	}
	for _, part := range strings.Split(strings.TrimSuffix(key, "/"), "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

// Failure returns actionable categories without exposing SDK URLs, credentials or
// carrier paths from wrapped transport and filesystem errors.
func Failure(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline exceeded"
	case errors.Is(err, ErrTransferConflict):
		return "destination already exists"
	case errors.Is(err, ErrTransferInvalid):
		return "unsupported file or object path"
	case errors.Is(err, os.ErrNotExist):
		return "source or destination parent not found"
	case errors.Is(err, os.ErrPermission):
		return "file access denied"
	}
	var resticErr *resticCommandError
	if errors.As(err, &resticErr) {
		return resticErr.Error()
	}
	if category := backend.Failure(err); category != "" {
		return category
	}
	return "copy failed"
}
