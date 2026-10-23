package r2

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"golang.org/x/sync/errgroup"
)

// IncompleteUpload 是一个既没完成也没取消的分片上传。它已上传的分片不会出现在对象列表里，
// 但会一直占用存储空间，直到上传被完成或取消。
type IncompleteUpload struct {
	Key       string
	UploadID  string
	Initiated time.Time
	Parts     int
	Size      int64
}

// ListIncompleteUploads 列出 prefix 下未完成的分片上传，并统计每个上传已传分片的数量和大小。
func (c *Client) ListIncompleteUploads(ctx context.Context, prefix string) ([]IncompleteUpload, error) {
	uploads, err := c.listUploads(ctx, prefix)
	if err != nil {
		return nil, err
	}
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(8)
	for i := range uploads {
		g.Go(func() error {
			u := &uploads[i]
			pager := s3.NewListPartsPaginator(c.S3, &s3.ListPartsInput{
				Bucket: &c.Bucket, Key: &u.Key, UploadId: &u.UploadID,
			})
			for pager.HasMorePages() {
				page, err := pager.NextPage(gctx)
				if isNoSuchUpload(err) {
					return nil // 列出之后刚好被完成或取消
				}
				if err != nil {
					return fmt.Errorf("列出 %s 的分片: %w", u.Key, err)
				}
				for _, p := range page.Parts {
					u.Parts++
					u.Size += aws.ToInt64(p.Size)
				}
			}
			return nil
		})
	}
	return uploads, g.Wait()
}

// CountIncompleteUploads 只统计数量，不逐个列出分片。
func (c *Client) CountIncompleteUploads(ctx context.Context, prefix string) (int, error) {
	uploads, err := c.listUploads(ctx, prefix)
	return len(uploads), err
}

// AbortUpload 取消分片上传并删除其已上传的分片。上传已不存在时视为成功。
func (c *Client) AbortUpload(ctx context.Context, key, uploadID string) error {
	_, err := c.S3.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
		Bucket: &c.Bucket, Key: &key, UploadId: &uploadID,
	})
	if isNoSuchUpload(err) {
		return nil
	}
	return err
}

func (c *Client) listUploads(ctx context.Context, prefix string) ([]IncompleteUpload, error) {
	input := &s3.ListMultipartUploadsInput{Bucket: &c.Bucket}
	if prefix != "" {
		input.Prefix = &prefix
	}
	var uploads []IncompleteUpload
	pager := s3.NewListMultipartUploadsPaginator(c.S3, input)
	for pager.HasMorePages() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("列出未完成的分片上传: %w", err)
		}
		for _, u := range page.Uploads {
			uploads = append(uploads, IncompleteUpload{
				Key:       aws.ToString(u.Key),
				UploadID:  aws.ToString(u.UploadId),
				Initiated: aws.ToTime(u.Initiated),
			})
		}
	}
	return uploads, nil
}

func isNoSuchUpload(err error) bool {
	var apiErr smithy.APIError
	return errors.As(err, &apiErr) && apiErr.ErrorCode() == "NoSuchUpload"
}
