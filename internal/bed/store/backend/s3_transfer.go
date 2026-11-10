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
	"os"

	"github.com/aws/aws-sdk-go-v2/service/s3"
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

// Upload preserves overwrite=false at the S3 commit point for both upload modes.
// The caller must keep file contents and size stable until Upload returns.
func (o *S3) Upload(ctx context.Context, key string, file *os.File, size int64, overwrite bool) error {
	var ifAbsent *string
	if !overwrite {
		value := "*"
		ifAbsent = &value
	}
	return transferWriteError(o.upload(ctx, key, file, size, nil, ifAbsent))
}
