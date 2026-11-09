package backend

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type observeS3Transport struct {
	base    http.RoundTripper
	observe func(*http.Request)
	after   func(*http.Request, error)
}

func (o observeS3Transport) RoundTrip(r *http.Request) (*http.Response, error) {
	o.observe(r)
	response, err := o.base.RoundTrip(r)
	if o.after != nil {
		o.after(r, err)
	}
	return response, err
}

// TestS3MultipartCompatibility exercises the real backend against an explicitly
// selected S3 service. It writes only a unique diagnostics prefix and cleans it.
// HTTP endpoints are bridged through verified TLS to exercise aws-chunked bodies.
func TestS3MultipartCompatibility(t *testing.T) {
	endpoint := os.Getenv("HOSTEL_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("HOSTEL_TEST_S3_ENDPOINT is unset")
	}
	cfg := Config{Endpoint: endpoint, PathStyle: true, Bucket: os.Getenv("HOSTEL_TEST_S3_BUCKET"), Region: os.Getenv("HOSTEL_TEST_S3_REGION"), AccessKeyID: os.Getenv("HOSTEL_TEST_S3_ACCESS_KEY"), SecretAccessKey: os.Getenv("HOSTEL_TEST_S3_SECRET_KEY"), PartSizeBytes: 8 << 20}
	if cfg.Bucket == "" {
		t.Fatal("HOSTEL_TEST_S3_BUCKET is required")
	}
	plain, err := NewS3(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	target, err := url.Parse(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	transport := http.DefaultTransport
	if target.Scheme == "http" {
		bridge := httptest.NewTLSServer(httputil.NewSingleHostReverseProxy(target))
		defer bridge.Close()
		cfg.Endpoint = bridge.URL
		transport = bridge.Client().Transport
	}
	obj, err := NewS3(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	var id [8]byte
	if _, err := rand.Read(id[:]); err != nil {
		t.Fatal(err)
	}
	prefix := "diagnostics/hostel-multipart/" + hex.EncodeToString(id[:]) + "/"
	t.Logf("S3 compatibility prefix=%s part_bytes=%d", prefix, cfg.PartSizeBytes)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		uploads, err := plain.client.ListMultipartUploads(ctx, &s3.ListMultipartUploadsInput{Bucket: &cfg.Bucket, Prefix: &prefix})
		if err != nil {
			t.Error(err)
		} else {
			for _, u := range uploads.Uploads {
				t.Errorf("unfinished multipart upload: %s", *u.Key)
				if _, err := plain.client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{Bucket: &cfg.Bucket, Key: u.Key, UploadId: u.UploadId}); err != nil {
					t.Error(err)
				}
			}
		}
		keys, err := plain.List(ctx, prefix)
		if err != nil {
			t.Error(err)
			return
		}
		// Older S3-compatible servers require Content-MD5 on batch deletes.
		// Individual deletes keep test cleanup independent of that capability.
		for _, key := range keys {
			if _, err := plain.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &cfg.Bucket, Key: &key}); err != nil {
				t.Error(err)
			}
		}
		remaining, err := plain.List(ctx, prefix)
		if err != nil || len(remaining) != 0 {
			t.Errorf("cleanup remaining=%d err=%v", len(remaining), err)
		} else {
			t.Log("cleanup verified: zero objects and zero unfinished uploads")
		}
	}()
	data := make([]byte, 32<<20)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		size  int
		parts int32
	}{
		{"equal", 8 << 20, 0}, {"above", (8 << 20) + 1, 2}, {"pack", 32 << 20, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var parts atomic.Int32
			obj.client = s3.New(obj.client.Options(), func(o *s3.Options) {
				o.HTTPClient = &http.Client{Timeout: 90 * time.Second, Transport: observeS3Transport{base: transport, observe: func(r *http.Request) {
					if r.Method != "PUT" {
						return
					}
					if r.URL.Query().Has("partNumber") {
						parts.Add(1)
					}
					if r.Header.Get("Content-Encoding") == "aws-chunked" {
						var first []byte
						for len(first) < 128 {
							var b [1]byte
							n, err := r.Body.Read(b[:])
							if n > 0 {
								first = append(first, b[0])
							}
							if err != nil || bytes.HasSuffix(first, []byte("\r\n")) {
								break
							}
						}
						size, err := strconv.ParseInt(strings.TrimSpace(string(first)), 16, 64)
						if err != nil || size > cfg.PartSizeBytes {
							t.Errorf("chunk header %q exceeds configured part or is invalid: %v", first, err)
						}
						t.Logf("wire: aws-chunked chunk_bytes=%d multipart=%v", size, r.URL.Query().Has("partNumber"))
						r.Body = struct {
							io.Reader
							io.Closer
						}{io.MultiReader(bytes.NewReader(first), r.Body), r.Body}
					}
				}}}
			})
			key := prefix + tc.name
			if err := obj.Put(t.Context(), key, bytes.NewReader(data[:tc.size]), int64(tc.size), map[string]string{"layout": "pack"}); err != nil {
				t.Fatal(err)
			}
			if parts.Load() != tc.parts {
				t.Fatalf("parts=%d want=%d", parts.Load(), tc.parts)
			}
			meta, size, found, err := plain.Head(t.Context(), key)
			if err != nil || !found || size != int64(tc.size) || meta["layout"] != "pack" {
				t.Fatalf("head bytes=%d found=%v metadata=%v err=%v", size, found, meta, err)
			}
			body, err := plain.Get(t.Context(), key)
			if err != nil {
				t.Fatal(err)
			}
			h := sha256.New()
			n, err := io.Copy(h, body)
			body.Close()
			want := sha256.Sum256(data[:tc.size])
			if err != nil || n != int64(tc.size) || !bytes.Equal(h.Sum(nil), want[:]) {
				t.Fatal("readback differs", err)
			}
			t.Logf("verified object_bytes=%d parts=%d metadata=ok sha256=match", size, parts.Load())
		})
	}
	t.Run("cancel", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		obj.client = s3.New(obj.client.Options(), func(o *s3.Options) {
			o.HTTPClient = &http.Client{Timeout: 90 * time.Second, Transport: observeS3Transport{base: transport, observe: func(*http.Request) {}, after: func(r *http.Request, err error) {
				if r.URL.Query().Has("partNumber") && err == nil {
					cancel()
				}
			}}}
		})
		key := prefix + "cancel"
		if err := obj.Put(ctx, key, bytes.NewReader(data), int64(len(data)), nil); err == nil {
			t.Fatal("canceled upload committed")
		}
		_, _, found, err := plain.Head(t.Context(), key)
		if err != nil || found {
			t.Fatalf("canceled object exists=%v err=%v", found, err)
		}
	})
}
