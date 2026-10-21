package cli

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/sync/errgroup"

	"upload-cf/internal/r2"
)

type uploadFlags struct {
	prefix          string
	key             string
	excludes        []string
	concurrency     int
	partConcurrency int
	partSizeMiB     int64
	thresholdMiB    int64
	contentType     string
	cacheControl    string
	skipExisting    bool
	dryRun          bool
	quiet           bool
}

func newUploadCmd(g *globalFlags) *cobra.Command {
	f := &uploadFlags{}
	cmd := &cobra.Command{
		Use:     "upload <文件或目录>...",
		Aliases: []string{"up", "cp"},
		Short:   "上传文件或目录",
		Long: `上传一个或多个本地文件/目录到 R2。

目录会递归上传，对象 key 为 <prefix>/<目录内相对路径>（不包含目录本身的名字）；
单个文件的 key 为 <prefix>/<文件名>，也可用 --key 指定完整 key。
大于 --multipart-threshold 的文件自动使用分片上传，中断时会自动清理未完成的分片。`,
		Example: `  r2up upload photo.jpg
  r2up upload ./dist --prefix site --exclude .DS_Store --exclude '*.map'
  r2up upload backup.tar.gz --key backups/2026-10-21.tar.gz
  r2up upload ./assets -p static --cache-control "public, max-age=31536000" --skip-existing`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUpload(cmd, g, f, args)
		},
	}
	addUploadFlags(cmd.Flags(), f)
	return cmd
}

// addUploadFlags 注册上传参数。根命令和 upload 子命令共用这组参数，
// 因此 `r2up <文件>` 与 `r2up upload <文件>` 等价。
func addUploadFlags(fl *pflag.FlagSet, f *uploadFlags) {
	fl.StringVarP(&f.prefix, "prefix", "p", "", "对象 key 前缀（相当于远端目录）")
	fl.StringVarP(&f.key, "key", "k", "", "单个文件的完整对象 key")
	fl.StringArrayVarP(&f.excludes, "exclude", "x", nil, "排除匹配的文件名或相对路径（glob，可重复）")
	fl.IntVarP(&f.concurrency, "concurrency", "j", 4, "同时上传的文件数")
	fl.IntVar(&f.partConcurrency, "part-concurrency", 4, "单个文件同时上传的分片数")
	fl.Int64Var(&f.partSizeMiB, "part-size", 16, "分片大小（MiB，至少 5）")
	fl.Int64Var(&f.thresholdMiB, "multipart-threshold", 100, "超过该大小（MiB）使用分片上传")
	fl.StringVar(&f.contentType, "content-type", "", "指定 Content-Type（默认自动识别）")
	fl.StringVar(&f.cacheControl, "cache-control", "", "设置 Cache-Control 头")
	fl.BoolVar(&f.skipExisting, "skip-existing", false, "跳过远端已存在且大小相同的对象")
	fl.BoolVarP(&f.dryRun, "dry-run", "n", false, "只列出将要上传的文件，不实际上传")
	fl.BoolVarP(&f.quiet, "quiet", "q", false, "只输出错误")
}

func runUpload(cmd *cobra.Command, g *globalFlags, f *uploadFlags, args []string) error {
	if f.key != "" && f.prefix != "" {
		return errors.New("--key 与 --prefix 不能同时使用")
	}
	if f.concurrency < 1 || f.partConcurrency < 1 {
		return errors.New("并发数必须大于 0")
	}
	if f.partSizeMiB < 5 {
		return errors.New("--part-size 不能小于 5 MiB")
	}

	jobs, err := r2.CollectJobs(args, r2.CollectOptions{Prefix: f.prefix, Key: f.key, Excludes: f.excludes})
	if err != nil {
		return err
	}
	if len(jobs) == 0 {
		return errors.New("没有找到需要上传的文件")
	}
	var totalBytes int64
	for _, j := range jobs {
		totalBytes += j.Size
	}

	out, errw := cmd.OutOrStdout(), cmd.ErrOrStderr()
	if f.dryRun {
		cfg, err := g.load()
		if err != nil {
			return err
		}
		for _, j := range jobs {
			fmt.Fprintf(out, "%s → r2://%s/%s (%s)\n", j.Path, cfg.Bucket, j.Key, humanBytes(j.Size))
		}
		fmt.Fprintf(errw, "共 %d 个文件，%s（dry-run，未上传）\n", len(jobs), humanBytes(totalBytes))
		return nil
	}

	client, err := g.client()
	if err != nil {
		return err
	}
	opts := r2.UploadOptions{
		MultipartThreshold: f.thresholdMiB * r2.MiB,
		PartSize:           f.partSizeMiB * r2.MiB,
		PartConcurrency:    f.partConcurrency,
		ContentType:        f.contentType,
		CacheControl:       f.cacheControl,
		SkipExisting:       f.skipExisting,
	}

	ctx := cmd.Context()
	p := newProgress(errw, len(jobs), totalBytes, !f.quiet)
	var uploaded, skipped, failed, uploadedBytes atomic.Int64
	var eg errgroup.Group
	eg.SetLimit(f.concurrency)
	for _, j := range jobs {
		if ctx.Err() != nil {
			break
		}
		eg.Go(func() error {
			// 分片上传时 onBytes 会被多个 goroutine 并发调用。
			var sent atomic.Int64
			skip, err := client.UploadFile(ctx, j, opts, func(n int64) {
				sent.Add(n)
				p.addBytes(n)
			})
			p.fileDone(j.Size, sent.Load())
			switch {
			case errors.Is(err, context.Canceled):
				failed.Add(1)
				p.printf(errw, "✗ %s: 已取消\n", j.Path)
			case err != nil:
				failed.Add(1)
				p.printf(errw, "✗ %s: %v\n", j.Path, err)
			case skip:
				skipped.Add(1)
				if !f.quiet {
					p.printf(out, "- %s → r2://%s/%s（已存在，跳过）\n", j.Path, client.Bucket, j.Key)
				}
			default:
				uploaded.Add(1)
				uploadedBytes.Add(j.Size)
				if !f.quiet {
					line := fmt.Sprintf("✓ %s → r2://%s/%s (%s)\n", j.Path, client.Bucket, j.Key, humanBytes(j.Size))
					if u := r2.ObjectURL(client.PublicURL, j.Key); u != "" {
						line += "  " + u + "\n"
					}
					p.printf(out, "%s", line)
				}
			}
			return nil
		})
	}
	_ = eg.Wait()
	p.finish()

	if !f.quiet {
		elapsed := p.elapsed()
		fmt.Fprintf(errw, "完成：上传 %d，跳过 %d，失败 %d，共 %s，用时 %s（%s/s）\n",
			uploaded.Load(), skipped.Load(), failed.Load(), humanBytes(uploadedBytes.Load()),
			roundDuration(elapsed), humanBytes(rate(uploadedBytes.Load(), elapsed)))
	}
	if ctx.Err() != nil {
		return errors.New("上传已取消")
	}
	if n := failed.Load(); n > 0 {
		return fmt.Errorf("%d 个文件上传失败", n)
	}
	return nil
}
