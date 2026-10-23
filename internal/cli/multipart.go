package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"upload-cf/internal/r2"
)

func newMultipartCmd(g *globalFlags) *cobra.Command {
	var olderThan time.Duration
	cmd := &cobra.Command{
		Use:     "multipart [前缀]",
		Aliases: []string{"mp"},
		Short:   "查看和清理未完成的分片上传",
		Long: `分片上传中断后（如进程被强制结束、断电、断网），已上传的分片会留在存储桶里，
占用存储空间并计费，但不会出现在 r2up ls 的对象列表中。

  r2up multipart [前缀]         列出未完成的分片上传（同 multipart ls）
  r2up multipart clean [前缀]   取消这些上传并删除已上传的分片

正在进行中的上传也会出现在列表里，清理它们会导致那次上传失败；
可用 --older-than 只处理开始时间较早的上传。`,
		Example: `  r2up multipart
  r2up multipart clean
  r2up multipart clean backups/ --older-than 24h -y`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMultipartLs(cmd, g, args, olderThan)
		},
	}
	cmd.PersistentFlags().DurationVar(&olderThan, "older-than", 0, "只处理开始时间早于该时长的上传，如 1h、24h、168h")

	ls := &cobra.Command{
		Use:   "ls [前缀]",
		Short: "列出未完成的分片上传及其占用的空间",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMultipartLs(cmd, g, args, olderThan)
		},
	}

	var yes bool
	clean := &cobra.Command{
		Use:   "clean [前缀]",
		Short: "取消未完成的分片上传并删除已上传的分片",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMultipartClean(cmd, g, args, olderThan, yes)
		},
	}
	clean.Flags().BoolVarP(&yes, "yes", "y", false, "不询问，直接清理")

	cmd.AddCommand(ls, clean)
	return cmd
}

func runMultipartLs(cmd *cobra.Command, g *globalFlags, args []string, olderThan time.Duration) error {
	client, err := g.client()
	if err != nil {
		return err
	}
	uploads, skipped, err := pendingUploads(cmd.Context(), client, firstArg(args), olderThan)
	if err != nil {
		return err
	}
	printUploads(cmd.OutOrStdout(), uploads)
	errw := cmd.ErrOrStderr()
	fmt.Fprintf(errw, "共 %d 个未完成的分片上传，占用 %s%s\n", len(uploads), humanBytes(totalSize(uploads)), skippedNote(skipped, olderThan))
	if len(uploads) > 0 {
		fmt.Fprintln(errw, "可用 r2up multipart clean 清理")
	}
	return nil
}

func runMultipartClean(cmd *cobra.Command, g *globalFlags, args []string, olderThan time.Duration, yes bool) error {
	client, err := g.client()
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	out, errw := cmd.OutOrStdout(), cmd.ErrOrStderr()
	uploads, skipped, err := pendingUploads(ctx, client, firstArg(args), olderThan)
	if err != nil {
		return err
	}
	if len(uploads) == 0 {
		fmt.Fprintf(errw, "没有需要清理的未完成分片上传%s\n", skippedNote(skipped, olderThan))
		return nil
	}

	printUploads(out, uploads)
	if !yes {
		fmt.Fprintf(errw, "将取消以上 %d 个上传并删除其分片，释放 %s。其中如有正在进行的上传，它会因此失败。\n",
			len(uploads), humanBytes(totalSize(uploads)))
		ok, err := newPrompter(cmd.InOrStdin(), errw).confirm(ctx, "确认清理? [y/N]")
		switch {
		case errors.Is(err, errInputEnded):
			return errors.New("未确认，没有清理任何内容（非交互环境可加 -y）")
		case errors.Is(err, context.Canceled):
			return errors.New("已取消，没有清理任何内容")
		case err != nil:
			return err
		case !ok:
			return errors.New("已取消，没有清理任何内容")
		}
	}

	var cleaned, failed int
	var freed int64
	for _, u := range uploads {
		if ctx.Err() != nil {
			break
		}
		if err := client.AbortUpload(ctx, u.Key, u.UploadID); err != nil {
			failed++
			fmt.Fprintf(errw, "✗ %s: %v\n", u.Key, err)
			continue
		}
		cleaned++
		freed += u.Size
		fmt.Fprintf(out, "已清理 %s（%d 片，%s）\n", u.Key, u.Parts, humanBytes(u.Size))
	}
	fmt.Fprintf(errw, "完成：清理 %d 个，释放 %s\n", cleaned, humanBytes(freed))
	if ctx.Err() != nil {
		return errors.New("清理已中断")
	}
	if failed > 0 {
		return fmt.Errorf("%d 个上传清理失败", failed)
	}
	return nil
}

// pendingUploads 返回开始时间早于 olderThan 的未完成上传，以及因此被略过的数量。
func pendingUploads(ctx context.Context, client *r2.Client, prefix string, olderThan time.Duration) ([]r2.IncompleteUpload, int, error) {
	all, err := client.ListIncompleteUploads(ctx, prefix)
	if err != nil {
		return nil, 0, err
	}
	now := time.Now()
	var kept []r2.IncompleteUpload
	for _, u := range all {
		if now.Sub(u.Initiated) < olderThan {
			continue
		}
		kept = append(kept, u)
	}
	return kept, len(all) - len(kept), nil
}

func printUploads(w io.Writer, uploads []r2.IncompleteUpload) {
	now := time.Now()
	for _, u := range uploads {
		fmt.Fprintf(w, "%s %8s 前 %5d 片 %10s  %s\n",
			u.Initiated.Local().Format("2006-01-02 15:04:05"), humanAge(now.Sub(u.Initiated)),
			u.Parts, humanBytes(u.Size), u.Key)
	}
}

func totalSize(uploads []r2.IncompleteUpload) int64 {
	var n int64
	for _, u := range uploads {
		n += u.Size
	}
	return n
}

func skippedNote(skipped int, olderThan time.Duration) string {
	if skipped == 0 {
		return ""
	}
	return fmt.Sprintf("（另有 %d 个开始不到 %s 的上传未计入）", skipped, olderThan)
}

func humanAge(d time.Duration) string {
	d = max(d, 0)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
	}
}

func firstArg(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}
