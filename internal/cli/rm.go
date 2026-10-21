package cli

import (
	"fmt"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/spf13/cobra"
)

func newRmCmd(g *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:     "rm <key>...",
		Aliases: []string{"delete"},
		Short:   "删除对象",
		Example: `  r2up rm site/old.html
  r2up rm a.txt b.txt`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := g.client()
			if err != nil {
				return err
			}
			for _, key := range args {
				_, err := client.S3.DeleteObject(cmd.Context(), &s3.DeleteObjectInput{Bucket: &client.Bucket, Key: &key})
				if err != nil {
					return fmt.Errorf("删除 %s: %w", key, err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "已删除 r2://%s/%s\n", client.Bucket, key)
			}
			return nil
		},
	}
}
