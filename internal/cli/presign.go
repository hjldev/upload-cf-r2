package cli

import (
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/spf13/cobra"
)

// R2 预签名链接最长有效期为 7 天。
const maxPresignExpiry = 7 * 24 * time.Hour

func newPresignCmd(g *globalFlags) *cobra.Command {
	var (
		expires time.Duration
		put     bool
	)
	cmd := &cobra.Command{
		Use:   "presign <key>",
		Short: "生成对象的预签名链接（下载或上传）",
		Example: `  r2up presign reports/2026.pdf --expires 24h
  r2up presign incoming/data.csv --put    # 生成可用 curl -T 上传的链接`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if expires <= 0 || expires > maxPresignExpiry {
				return errors.New("--expires 必须在 1s 到 168h（7 天）之间")
			}
			client, err := g.client()
			if err != nil {
				return err
			}
			presigner := s3.NewPresignClient(client.S3, s3.WithPresignExpires(expires))
			var req *v4.PresignedHTTPRequest
			if put {
				req, err = presigner.PresignPutObject(cmd.Context(), &s3.PutObjectInput{Bucket: &client.Bucket, Key: &args[0]})
			} else {
				req, err = presigner.PresignGetObject(cmd.Context(), &s3.GetObjectInput{Bucket: &client.Bucket, Key: &args[0]})
			}
			if err != nil {
				return fmt.Errorf("生成预签名链接: %w", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), req.URL)
			return nil
		},
	}
	cmd.Flags().DurationVarP(&expires, "expires", "e", time.Hour, "有效期，如 30m、24h（最长 168h）")
	cmd.Flags().BoolVar(&put, "put", false, "生成上传（PUT）链接而不是下载链接")
	return cmd
}
