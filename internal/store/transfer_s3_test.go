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

package store

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
)

func TestTransferMultipartCommitAndAbort(t *testing.T) {
	for _, outcome := range []string{"success", "conflict", "part_failure"} {
		t.Run(outcome, func(t *testing.T) {
			var mu sync.Mutex
			parts, aborts, commits := 0, 0, 0
			var received int64
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				w.Header().Set("Content-Type", "application/xml")
				switch {
				case r.Method == http.MethodPost && r.URL.Query().Has("uploads"):
					fmt.Fprint(w, `<InitiateMultipartUploadResult><Bucket>bucket</Bucket><Key>object</Key><UploadId>upload-1</UploadId></InitiateMultipartUploadResult>`)
				case r.Method == http.MethodPut && r.URL.Query().Get("uploadId") == "upload-1":
					parts++
					n, err := io.Copy(io.Discard, r.Body)
					if err != nil {
						t.Errorf("part read: %v", err)
					}
					received += n
					if outcome == "part_failure" {
						w.WriteHeader(403)
						fmt.Fprint(w, `<Error><Code>AccessDenied</Code></Error>`)
						return
					}
					w.Header().Set("ETag", fmt.Sprintf(`"part-%d"`, parts))
				case r.Method == http.MethodPost && r.URL.Query().Get("uploadId") == "upload-1":
					commits++
					if r.Header.Get("If-None-Match") != "*" {
						t.Error("multipart commit did not enforce overwrite=false")
					}
					if outcome == "conflict" {
						w.WriteHeader(412)
						fmt.Fprint(w, `<Error><Code>PreconditionFailed</Code></Error>`)
						return
					}
					fmt.Fprint(w, `<CompleteMultipartUploadResult><Bucket>bucket</Bucket><Key>object</Key><ETag>"complete"</ETag></CompleteMultipartUploadResult>`)
				case r.Method == http.MethodDelete && r.URL.Query().Get("uploadId") == "upload-1":
					aborts++
					w.WriteHeader(204)
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL)
					w.WriteHeader(400)
				}
			}))
			defer remote.Close()
			cfg := testS3Config()
			cfg.Endpoint = remote.URL
			cfg.PathStyle = true
			client, err := newS3Client(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			object := &s3obj{client: client, bucket: "bucket"}
			file, err := os.CreateTemp(t.TempDir(), "source")
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			const size = 17 << 20
			if err := file.Truncate(size); err != nil {
				t.Fatal(err)
			}
			err = object.upload(t.Context(), "object", file, size, false)
			mu.Lock()
			defer mu.Unlock()
			switch outcome {
			case "success":
				if err != nil || parts != 3 || received != size || commits != 1 || aborts != 0 {
					t.Fatalf("multipart: err=%v parts=%d bytes=%d commits=%d aborts=%d", err, parts, received, commits, aborts)
				}
			case "conflict":
				if !errors.Is(err, ErrTransferConflict) || commits != 1 || aborts != 1 {
					t.Fatalf("conflict err=%v commits=%d aborts=%d", err, commits, aborts)
				}
			case "part_failure":
				if err == nil || commits != 0 || aborts != 1 {
					t.Fatalf("part failure err=%v commits=%d aborts=%d", err, commits, aborts)
				}
			}
		})
	}
}
