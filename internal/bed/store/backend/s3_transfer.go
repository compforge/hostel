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
	"os"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

// visit uses pages rather than retaining every object key in a directory.
func (o *S3) Visit(ctx context.Context, prefix string, visit func(string) error) error {
	pages := s3.NewListObjectsV2Paginator(o.client, &s3.ListObjectsV2Input{Bucket: &o.bucket, Prefix: &prefix})
	for pages.HasMorePages() {
		callCtx, cancel := context.WithTimeout(ctx, OperationTimeout)
		page, err := pages.NextPage(callCtx)
		cancel()
		if err != nil {
			return err
		}
		for _, object := range page.Contents {
			if err := ctx.Err(); err != nil {
				return err
			}
			if object.Key != nil {
				if err := visit(*object.Key); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func transferWriteError(err error) error {
	var response *smithyhttp.ResponseError
	if errors.As(err, &response) && (response.HTTPStatusCode() == 412 || response.HTTPStatusCode() == 409) {
		return fmt.Errorf("%w: remote object exists or changed", ErrConflict)
	}
	return err
}

// upload preserves overwrite=false at the S3 commit point, including multipart
// uploads. HEAD followed by unconditional PUT would race another writer.
func (o *S3) Upload(ctx context.Context, key string, file *os.File, size int64, overwrite bool) (retErr error) {
	var ifAbsent *string
	if !overwrite {
		value := "*"
		ifAbsent = &value
	}
	const multipartThreshold = 16 << 20
	if size <= multipartThreshold {
		_, err := o.client.PutObject(ctx, &s3.PutObjectInput{Bucket: &o.bucket, Key: &key, Body: file, ContentLength: &size, IfNoneMatch: ifAbsent})
		return transferWriteError(err)
	}
	created, err := o.client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{Bucket: &o.bucket, Key: &key})
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		// A canceled request must still attempt to reclaim its incomplete upload.
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), OperationTimeout)
		defer cancel()
		_, err := o.client.AbortMultipartUpload(cleanupCtx, &s3.AbortMultipartUploadInput{Bucket: &o.bucket, Key: &key, UploadId: created.UploadId})
		retErr = errors.Join(retErr, err)
	}()
	partSize := max(int64(8<<20), (size+9999)/10000)
	var parts []s3types.CompletedPart
	for offset := int64(0); offset < size; offset += partSize {
		length := min(partSize, size-offset)
		number := int32(len(parts) + 1)
		part, err := o.client.UploadPart(ctx, &s3.UploadPartInput{Bucket: &o.bucket, Key: &key, UploadId: created.UploadId, PartNumber: &number, ContentLength: &length, Body: io.NewSectionReader(file, offset, length)})
		if err != nil {
			return err
		}
		parts = append(parts, s3types.CompletedPart{PartNumber: &number, ETag: part.ETag})
	}
	_, err = o.client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{Bucket: &o.bucket, Key: &key, UploadId: created.UploadId, MultipartUpload: &s3types.CompletedMultipartUpload{Parts: parts}, IfNoneMatch: ifAbsent})
	if err != nil {
		return transferWriteError(err)
	}
	committed = true
	return nil
}
