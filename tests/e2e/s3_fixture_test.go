//go:build e2e

package e2e_test

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/qiankunli/go-stdx/randx"
)

type s3Fixture struct {
	client *s3.Client
	bucket string
}

// The runner owns only a fresh bucket. Never empty a caller-supplied bucket.
// Reuse the project's AWS SDK; the fixture must not implement an S3 emulator.
func newS3Fixture(t *testing.T) *s3Fixture {
	t.Helper()
	for _, name := range []string{"HOSTEL_S3_ENDPOINT", "HOSTEL_S3_ACCESS_KEY_ID", "HOSTEL_S3_SECRET_ACCESS_KEY"} {
		if os.Getenv(name) == "" {
			t.Fatalf("required S3 fixture setting %s is missing", name)
		}
	}
	f := &s3Fixture{bucket: "hostel-e2e-" + randx.Hex(8)}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	t.Cleanup(transport.CloseIdleConnections)
	f.client = s3.New(s3.Options{
		Region: "us-east-1", BaseEndpoint: aws.String(os.Getenv("HOSTEL_S3_ENDPOINT")), UsePathStyle: true,
		Credentials: credentials.NewStaticCredentialsProvider(os.Getenv("HOSTEL_S3_ACCESS_KEY_ID"), os.Getenv("HOSTEL_S3_SECRET_ACCESS_KEY"), ""),
		HTTPClient:  &http.Client{Transport: transport, Timeout: 30 * time.Second},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := f.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: &f.bucket}); err != nil {
		t.Fatalf("prepare S3 fixture bucket: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		keys := s3.NewListObjectsV2Paginator(f.client, &s3.ListObjectsV2Input{Bucket: &f.bucket})
		for keys.HasMorePages() {
			page, err := keys.NextPage(ctx)
			if err != nil {
				t.Errorf("list fixture cleanup: %v", err)
				return
			}
			for _, object := range page.Contents {
				if _, err := f.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &f.bucket, Key: object.Key}); err != nil {
					t.Errorf("delete fixture object: %v", err)
				}
			}
		}
		if _, err := f.client.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: &f.bucket}); err != nil {
			t.Errorf("delete fixture bucket: %v", err)
		}
	})
	t.Setenv("HOSTEL_S3_BUCKET", f.bucket)
	t.Setenv("HOSTEL_S3_PREFIX", "roundtrip")
	t.Setenv("HOSTEL_S3_REGION", "us-east-1")
	t.Setenv("HOSTEL_S3_PATH_STYLE", "true")
	t.Setenv("HOSTEL_S3_SESSION_TOKEN", "")
	return f
}

func (f *s3Fixture) count(t *testing.T) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pages := s3.NewListObjectsV2Paginator(f.client, &s3.ListObjectsV2Input{Bucket: &f.bucket})
	count := 0
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			t.Fatalf("inspect S3 fixture: %v", err)
		}
		count += len(page.Contents)
	}
	return count
}
