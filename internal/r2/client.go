// Package r2 封装 Cloudflare R2（S3 兼容 API）的上传、列举、删除和预签名操作。
package r2

import (
	"errors"
	"net/url"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"upload-cf/internal/config"
)

type Client struct {
	S3        *s3.Client
	Bucket    string
	PublicURL string
}

func NewClient(cfg config.Config) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	endpoint, err := cfg.EndpointURL()
	if err != nil {
		return nil, err
	}
	s3c := s3.New(s3.Options{
		Region:       "auto",
		BaseEndpoint: aws.String(endpoint),
		Credentials:  credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		UsePathStyle: true,
		// 新版 SDK 默认给每个请求附带 CRC 校验头，R2 并不完全支持，按 Cloudflare 文档改为仅在必须时计算。
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
		RetryMaxAttempts:           5,
	})
	return &Client{S3: s3c, Bucket: cfg.Bucket, PublicURL: cfg.PublicURL}, nil
}

// ObjectURL 返回对象的公开访问地址，未配置 public_url 时返回空字符串。
func ObjectURL(publicURL, key string) string {
	if publicURL == "" {
		return ""
	}
	segs := strings.Split(key, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.TrimRight(publicURL, "/") + "/" + strings.Join(segs, "/")
}

func isNotFound(err error) bool {
	var nf *types.NotFound
	if errors.As(err, &nf) {
		return true
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NotFound", "NoSuchKey":
			return true
		}
	}
	return false
}
