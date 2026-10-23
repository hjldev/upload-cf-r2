package r2

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"golang.org/x/sync/errgroup"
)

const (
	MiB = 1 << 20

	// R2 分片限制：除最后一片外每片大小必须一致，最小 5 MiB，最多 10000 片。
	minPartSize = 5 * MiB
	maxParts    = 10000
)

type UploadOptions struct {
	// 文件大小超过 MultipartThreshold 时使用分片上传。
	MultipartThreshold int64
	PartSize           int64
	// PartConcurrency 是单个文件并发上传的分片数。
	PartConcurrency int
	// ContentType 为空时按扩展名或文件内容自动识别。
	ContentType  string
	CacheControl string
	// SkipExisting 为 true 时，远端已存在且大小相同的对象会被跳过。
	SkipExisting bool
}

// UploadFile 上传单个文件，onBytes 会收到已发送字节数的增量（重试时可能为负）。
// 返回 skipped=true 表示因 SkipExisting 而跳过。
func (c *Client) UploadFile(ctx context.Context, job Job, opts UploadOptions, onBytes func(int64)) (skipped bool, err error) {
	f, err := os.Open(job.Path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return false, err
	}
	size := info.Size()

	if opts.SkipExisting {
		head, err := c.S3.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &c.Bucket, Key: &job.Key})
		switch {
		case err == nil:
			if aws.ToInt64(head.ContentLength) == size {
				return true, nil
			}
		case !isNotFound(err):
			return false, fmt.Errorf("检查远端对象: %w", err)
		}
	}

	contentType := opts.ContentType
	if contentType == "" {
		contentType = detectContentType(f, job.Path)
	}
	var cacheControl *string
	if opts.CacheControl != "" {
		cacheControl = &opts.CacheControl
	}

	if size <= opts.MultipartThreshold {
		_, err = c.S3.PutObject(ctx, &s3.PutObjectInput{
			Bucket:        &c.Bucket,
			Key:           &job.Key,
			Body:          &progressReader{r: io.NewSectionReader(f, 0, size), onBytes: onBytes},
			ContentLength: aws.Int64(size),
			ContentType:   &contentType,
			CacheControl:  cacheControl,
		})
		return false, err
	}

	create, err := c.S3.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket:       &c.Bucket,
		Key:          &job.Key,
		ContentType:  &contentType,
		CacheControl: cacheControl,
	})
	if err != nil {
		return false, fmt.Errorf("创建分片上传: %w", err)
	}
	if err := c.uploadParts(ctx, f, size, job.Key, create.UploadId, opts, onBytes); err != nil {
		// 原 ctx 可能已被取消（如 Ctrl+C），用独立的 ctx 清理未完成的分片，避免产生存储费用。
		abortCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_ = c.AbortUpload(abortCtx, job.Key, aws.ToString(create.UploadId))
		return false, err
	}
	return false, nil
}

func (c *Client) uploadParts(ctx context.Context, f *os.File, size int64, key string, uploadID *string, opts UploadOptions, onBytes func(int64)) error {
	partSize := PartSizeFor(size, opts.PartSize)
	numParts := int((size + partSize - 1) / partSize)
	parts := make([]types.CompletedPart, numParts)

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(max(opts.PartConcurrency, 1))
	for i := range numParts {
		if gctx.Err() != nil {
			break
		}
		g.Go(func() error {
			off := int64(i) * partSize
			n := min(partSize, size-off)
			partNum := aws.Int32(int32(i + 1))
			out, err := c.S3.UploadPart(gctx, &s3.UploadPartInput{
				Bucket:        &c.Bucket,
				Key:           &key,
				UploadId:      uploadID,
				PartNumber:    partNum,
				Body:          &progressReader{r: io.NewSectionReader(f, off, n), onBytes: onBytes},
				ContentLength: aws.Int64(n),
			})
			if err != nil {
				return fmt.Errorf("上传分片 %d/%d: %w", i+1, numParts, err)
			}
			parts[i] = types.CompletedPart{ETag: out.ETag, PartNumber: partNum}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	_, err := c.S3.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket:          &c.Bucket,
		Key:             &key,
		UploadId:        uploadID,
		MultipartUpload: &types.CompletedMultipartUpload{Parts: parts},
	})
	if err != nil {
		return fmt.Errorf("完成分片上传: %w", err)
	}
	return nil
}

// PartSizeFor 在满足 R2 分片数量上限的前提下返回尽量接近 want 的分片大小。
func PartSizeFor(size, want int64) int64 {
	ps := max(want, minPartSize)
	if need := (size + maxParts - 1) / maxParts; ps < need {
		ps = (need + MiB - 1) / MiB * MiB
	}
	return ps
}

func detectContentType(f *os.File, name string) string {
	if ct := mime.TypeByExtension(filepath.Ext(name)); ct != "" {
		return ct
	}
	buf := make([]byte, 512)
	n, _ := f.ReadAt(buf, 0)
	return http.DetectContentType(buf[:n])
}

// progressReader 统计已读取的字节数。SDK 重试或计算签名时会 Seek 回退，
// 这时上报负增量，让总进度保持准确。
type progressReader struct {
	r       io.ReadSeeker
	pos     int64
	onBytes func(int64)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.pos += int64(n)
		p.report(int64(n))
	}
	return n, err
}

func (p *progressReader) Seek(offset int64, whence int) (int64, error) {
	pos, err := p.r.Seek(offset, whence)
	if err == nil {
		p.report(pos - p.pos)
		p.pos = pos
	}
	return pos, err
}

func (p *progressReader) report(n int64) {
	if p.onBytes != nil && n != 0 {
		p.onBytes(n)
	}
}
