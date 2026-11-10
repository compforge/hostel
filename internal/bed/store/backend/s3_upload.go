package backend

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
)

const DefaultPartSizeBytes int64 = 8 << 20

func ValidatePartSize(size int64) error {
	if size < 5<<20 || size > 5<<30 {
		return fmt.Errorf("store: S3 part size must be between 5 MiB and 5 GiB, got %d bytes", size)
	}
	return nil
}

// +spec=`The SDK owns PUT/multipart selection, part sizing and failure cleanup; the caller supplies declared lengths and conditional-write requirements.`
// +why=`Persistence uploads finalized buffers or archives after stopping writers for final eviction; explicit transfers require caller-owned source stability.`
// +rule=`Keep the source unchanged until upload returns. Concurrent source mutation is outside the contract; do not add custom S3 transfer machinery to handle it.`
// +link=component://docs/volumes.md
func (o *S3) upload(ctx context.Context, key string, body io.ReadSeeker, size int64, meta map[string]string, ifAbsent *string) error {
	partSize := o.partSize
	if partSize == 0 {
		partSize = DefaultPartSizeBytes
	}
	uploader := transfermanager.New(o.client, func(options *transfermanager.Options) {
		options.PartSizeBytes = partSize
		options.MultipartUploadThreshold = partSize
		options.Concurrency = 2
		options.FailTimeout = 30 * time.Second
	})
	slog.DebugContext(ctx, "S3 upload started", "object_bytes", size, "part_bytes", partSize)
	_, err := uploader.UploadObject(ctx, &transfermanager.UploadObjectInput{
		Bucket: &o.bucket, Key: &key, Body: body, ContentLength: &size, MpuObjectSize: &size,
		Metadata: meta, IfNoneMatch: ifAbsent,
	})
	if err != nil {
		slog.DebugContext(ctx, "S3 upload failed", "object_bytes", size, "part_bytes", partSize, "failure", Failure(err))
		return err
	}
	slog.DebugContext(ctx, "S3 upload completed", "object_bytes", size, "part_bytes", partSize)
	return nil
}
