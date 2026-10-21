package cli

import (
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/spf13/cobra"
)

func newLsCmd(g *globalFlags) *cobra.Command {
	var recursive bool
	cmd := &cobra.Command{
		Use:   "ls [前缀]",
		Short: "列出存储桶中的对象",
		Example: `  r2up ls
  r2up ls site/
  r2up ls -r static/`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := g.client()
			if err != nil {
				return err
			}
			input := &s3.ListObjectsV2Input{Bucket: &client.Bucket}
			if len(args) == 1 {
				input.Prefix = &args[0]
			}
			if !recursive {
				input.Delimiter = aws.String("/")
			}

			w := cmd.OutOrStdout()
			var count int
			var total int64
			pager := s3.NewListObjectsV2Paginator(client.S3, input)
			for pager.HasMorePages() {
				page, err := pager.NextPage(cmd.Context())
				if err != nil {
					return fmt.Errorf("列出对象: %w", err)
				}
				for _, cp := range page.CommonPrefixes {
					fmt.Fprintf(w, "%19s %10s  %s\n", "", "PRE", aws.ToString(cp.Prefix))
				}
				for _, o := range page.Contents {
					size := aws.ToInt64(o.Size)
					count++
					total += size
					fmt.Fprintf(w, "%s %10s  %s\n",
						aws.ToTime(o.LastModified).Local().Format("2006-01-02 15:04:05"),
						humanBytes(size), aws.ToString(o.Key))
				}
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "共 %d 个对象，%s\n", count, humanBytes(total))
			return nil
		},
	}
	cmd.Flags().BoolVarP(&recursive, "recursive", "r", false, "递归列出所有子路径下的对象")
	return cmd
}
