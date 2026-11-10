package backend

import (
	"context"
	"io"
	"log/slog"
	"time"

	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
)

// +spec=`The SDK owns PUT/multipart selection, part sizing and failure cleanup; the caller supplies declared lengths and conditional-write requirements.`
// +why=`Persistence uploads finalized buffers or archives after stopping writers for final eviction; explicit transfers require caller-owned source stability.`
// +rule=`Keep the source unchanged until upload returns. Concurrent source mutation is outside the contract; do not add custom S3 transfer machinery to handle it.`
// +link=component://docs/volumes.md
func (o *S3) upload(ctx context.Context, key string, body io.ReadSeeker, size int64, meta map[string]string, ifAbsent *string) error {
	uploader := transfermanager.New(o.client, func(options *transfermanager.Options) {
		options.Concurrency = 2
		options.FailTimeout = 30 * time.Second
	})
	slog.DebugContext(ctx, "S3 upload started", "object_bytes", size)
	_, err := uploader.UploadObject(ctx, &transfermanager.UploadObjectInput{
		Bucket: &o.bucket, Key: &key, Body: body, ContentLength: &size, MpuObjectSize: &size,
		Metadata: meta, IfNoneMatch: ifAbsent,
	})
	if err != nil {
		slog.DebugContext(ctx, "S3 upload failed", "object_bytes", size, "failure", Failure(err))
		return err
	}
	slog.DebugContext(ctx, "S3 upload completed", "object_bytes", size)
	return nil
}
