package backend

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

const DefaultPartSizeBytes int64 = 8 << 20

func ValidatePartSize(size int64) error {
	if size < 5<<20 || size > 5<<30 {
		return fmt.Errorf("store: S3 part size must be between 5 MiB and 5 GiB, got %d bytes", size)
	}
	return nil
}

// +spec=`Objects no larger than one part use PUT; larger objects use fixed-size multipart uploads, independently of pack layout.`
// +why=`Bounded request bodies avoid S3-compatible servers' aws-chunked limits without reducing stored object sizes.`
// +link=component://docs/volumes.md
func (o *S3) upload(ctx context.Context, key string, body io.ReadSeeker, size int64, meta map[string]string, ifAbsent *string) error {
	partSize := o.partSize
	if partSize == 0 {
		partSize = DefaultPartSizeBytes
	}
	if size <= partSize {
		_, err := o.client.PutObject(ctx, &s3.PutObjectInput{Bucket: &o.bucket, Key: &key, Body: body, ContentLength: &size, Metadata: meta, IfNoneMatch: ifAbsent})
		return err
	}
	// Keep the configured part size even for very large objects. The SDK would
	// otherwise silently increase it to stay within S3's 10,000-part limit.
	if size > partSize*10000 {
		return fmt.Errorf("store: object size %d exceeds 10000 parts of %d bytes", size, partSize)
	}
	uploader := transfermanager.New(o.client, func(options *transfermanager.Options) {
		options.PartSizeBytes = partSize
		options.MultipartUploadThreshold = partSize
		options.Concurrency = 2
		options.FailTimeout = 30 * time.Second
	})
	slog.DebugContext(ctx, "S3 multipart upload started", "object_bytes", size, "part_bytes", partSize)
	// Hide the seekable length so the SDK cannot resize parts at the 10,000-part
	// boundary. The known size is checked above and bounds this reader.
	_, err := uploader.UploadObject(ctx, &transfermanager.UploadObjectInput{
		Bucket: &o.bucket, Key: &key, Body: io.LimitReader(body, size), Metadata: meta, IfNoneMatch: ifAbsent,
	})
	if err != nil {
		slog.DebugContext(ctx, "S3 multipart upload failed", "object_bytes", size, "part_bytes", partSize, "failure", Failure(err))
		return err
	}
	slog.DebugContext(ctx, "S3 multipart upload completed", "object_bytes", size, "part_bytes", partSize)
	return nil
}
