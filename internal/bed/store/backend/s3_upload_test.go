package backend

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
)

// +case:id=s3_upload_boundary,desc=`SDK selects PUT below the threshold and multipart at or above it`,expect=`one committed object with identical bytes and metadata`
func TestPutObjectPartBoundary(t *testing.T) {
	for _, tc := range []struct {
		name      string
		size      int
		part      int64
		wantParts int
	}{
		{"empty", 0, 8 << 20, 0},
		{"below", (8 << 20) - 1, 8 << 20, 0},
		{"equal", 8 << 20, 8 << 20, 1},
		{"above", (8 << 20) + 1, 8 << 20, 2},
		{"pack", 32 << 20, 8 << 20, 4},
		{"custom", 12 << 20, 5 << 20, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := make([]byte, tc.size)
			for i := range data {
				data[i] = byte(i*31 + i/(1<<20))
			}
			var mu sync.Mutex
			parts := map[int][]byte{}
			puts, commits := 0, 0
			var stored []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				w.Header().Set("Content-Type", "application/xml")
				q := r.URL.Query()
				switch {
				case r.Method == http.MethodPost && q.Has("uploads"):
					if r.Header.Get("X-Amz-Meta-Layout") != "pack" {
						t.Error("multipart metadata lost")
					}
					fmt.Fprint(w, `<InitiateMultipartUploadResult><UploadId>upload-1</UploadId></InitiateMultipartUploadResult>`)
				case r.Method == http.MethodPut && q.Has("partNumber"):
					n, _ := strconv.Atoi(q.Get("partNumber"))
					part, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
					}
					if int64(len(part)) > tc.part {
						t.Errorf("part bytes=%d limit=%d", len(part), tc.part)
					}
					parts[n] = part
					w.Header().Set("ETag", fmt.Sprintf(`"part-%d"`, n))
				case r.Method == http.MethodPost && q.Has("uploadId"):
					commits++
					if got := r.Header.Get("X-Amz-Mp-Object-Size"); got != strconv.Itoa(tc.size) {
						t.Errorf("declared multipart length=%q want=%d", got, tc.size)
					}
					for i := 1; i <= len(parts); i++ {
						stored = append(stored, parts[i]...)
					}
					fmt.Fprint(w, `<CompleteMultipartUploadResult><ETag>"done"</ETag></CompleteMultipartUploadResult>`)
				case r.Method == http.MethodPut:
					puts++
					if r.Header.Get("X-Amz-Meta-Layout") != "pack" {
						t.Error("PUT metadata lost")
					}
					var err error
					stored, err = io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
					}
					w.Header().Set("ETag", `"done"`)
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL)
					w.WriteHeader(400)
				}
			}))
			defer server.Close()
			obj, err := NewS3(t.Context(), Config{Bucket: "bucket", Region: "us-east-1", AccessKeyID: "test", SecretAccessKey: "test", Endpoint: server.URL, PathStyle: true, PartSizeBytes: tc.part})
			if err != nil {
				t.Fatal(err)
			}
			if err = obj.Put(t.Context(), "object", bytes.NewReader(data), int64(len(data)), map[string]string{"layout": "pack"}); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(parts) != tc.wantParts {
				t.Fatalf("parts=%d want=%d", len(parts), tc.wantParts)
			}
			if tc.wantParts == 0 && (puts != 1 || commits != 0) {
				t.Fatalf("puts=%d commits=%d", puts, commits)
			}
			if tc.wantParts > 0 && (puts != 0 || commits != 1) {
				t.Fatalf("puts=%d commits=%d", puts, commits)
			}
			if sha256.Sum256(stored) != sha256.Sum256(data) {
				t.Fatal("committed object differs from input")
			}
		})
	}
}
