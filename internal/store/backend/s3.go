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

package backend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// newS3Client builds the shared S3-compatible client.
func newS3Client(ctx context.Context, cfg Config) (*s3.Client, error) {
	if cfg.AccessKeyID == "" || cfg.SecretAccessKey == "" {
		return nil, errors.New("store: HOSTEL_S3_ACCESS_KEY_ID and HOSTEL_S3_SECRET_ACCESS_KEY are required")
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(cfg.Region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			cfg.AccessKeyID, cfg.SecretAccessKey, cfg.SessionToken,
		)),
	)
	if err != nil {
		return nil, fmt.Errorf("store: aws config: %w", err)
	}
	return s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = &cfg.Endpoint
		}
		// TOS only supports virtual-hosted buckets, while some MinIO/Ceph
		// deployments require path-style. Keep the interoperable default and
		// let deployments opt into path-style explicitly.
		o.UsePathStyle = cfg.PathStyle
	}), nil
}

// S3 adapts a shared S3-compatible client to snapshot and streaming object operations.
type S3 struct {
	client *s3.Client
	bucket string
}

func (o *S3) Head(ctx context.Context, key string) (map[string]string, int64, bool, error) {
	out, err := o.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &o.bucket, Key: &key})
	if err != nil {
		var nf *s3types.NotFound
		if errors.As(err, &nf) {
			return nil, 0, false, nil
		}
		return nil, 0, false, fmt.Errorf("store: head %s: %w", key, err)
	}
	var size int64
	if out.ContentLength != nil {
		size = *out.ContentLength
	}
	return out.Metadata, size, true, nil
}

func (o *S3) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	out, err := o.client.GetObject(ctx, &s3.GetObjectInput{Bucket: &o.bucket, Key: &key})
	if err != nil {
		return nil, fmt.Errorf("store: get %s: %w", key, err)
	}
	return out.Body, nil
}

func (o *S3) Put(ctx context.Context, key string, r io.ReadSeeker, size int64, meta map[string]string) error {
	_, err := o.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: &o.bucket, Key: &key, Body: r, ContentLength: &size, Metadata: meta,
	})
	if err != nil {
		return fmt.Errorf("store: put %s: %w", key, err)
	}
	return nil
}

func (o *S3) Delete(ctx context.Context, keys []string) error {
	// DeleteObjects caps at 1000 keys per call.
	for len(keys) > 0 {
		n := min(len(keys), 1000)
		batch := make([]s3types.ObjectIdentifier, n)
		for i := range n {
			batch[i] = s3types.ObjectIdentifier{Key: &keys[i]}
		}
		if _, err := o.client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: &o.bucket, Delete: &s3types.Delete{Objects: batch},
		}); err != nil {
			return fmt.Errorf("store: delete batch: %w", err)
		}
		keys = keys[n:]
	}
	return nil
}

func (o *S3) List(ctx context.Context, prefix string) ([]string, error) {
	var keys []string
	p := s3.NewListObjectsV2Paginator(o.client, &s3.ListObjectsV2Input{Bucket: &o.bucket, Prefix: &prefix})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("store: list %s: %w", prefix, err)
		}
		for _, obj := range page.Contents {
			keys = append(keys, *obj.Key)
		}
	}
	return keys, nil
}

// Config describes where objects are stored; it contains no synchronization policy.
type Config struct {
	Bucket          string
	Prefix          string
	Endpoint        string
	PathStyle       bool
	Region          string
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
}

// NewS3 opens the object client shared by automatic persistence and explicit transfers.
func NewS3(ctx context.Context, cfg Config) (*S3, error) {
	client, err := newS3Client(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return &S3{client: client, bucket: cfg.Bucket}, nil
}

// ErrConflict reports a failed conditional object write.
var ErrConflict = errors.New("transfer conflict")

// OperationTimeout bounds one remote object operation, including multipart cleanup.
const OperationTimeout = 5 * time.Minute

// Failure returns a safe S3 error category, or an empty string for non-S3 errors.
// Raw transport errors may contain endpoints or signed URLs and must not be logged.
func Failure(err error) string {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "AccessDenied", "InvalidAccessKeyId", "SignatureDoesNotMatch":
			return "S3 access denied"
		case "NoSuchKey", "NoSuchBucket", "NotFound":
			return "S3 source or bucket not found"
		}
		return "S3 request failed"
	}
	return ""
}
