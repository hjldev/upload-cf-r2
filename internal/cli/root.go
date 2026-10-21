// Package cli 定义 r2up 的命令行接口。
package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"upload-cf/internal/config"
	"upload-cf/internal/r2"
)

type globalFlags struct {
	configPath string
	bucket     string
	accountID  string
	endpoint   string
	publicURL  string
}

// NewRootCmd 构建命令树。version 由 main 通过 -ldflags 注入。
func NewRootCmd(version string) *cobra.Command {
	g := &globalFlags{}
	uf := &uploadFlags{}
	root := &cobra.Command{
		Use:   "r2up <文件或目录>...",
		Short: "Cloudflare R2 文件上传工具",
		Long: `r2up 通过 S3 兼容 API 向 Cloudflare R2 上传文件。

不写子命令时默认执行 upload：r2up <文件> 等同于 r2up upload <文件>。
若文件名恰好与子命令同名（如 ls、rm），请写成 r2up upload ls 或 r2up ./ls。

凭证可写在配置文件（默认 ~/.config/r2up/config.json）中，或通过环境变量提供：
  R2_ACCOUNT_ID  R2_ACCESS_KEY_ID  R2_SECRET_ACCESS_KEY  R2_BUCKET  R2_PUBLIC_URL
优先级：命令行参数 > 环境变量 > 配置文件。`,
		Example: `  r2up photo.jpg
  r2up ./dist -p site -x .DS_Store
  r2up backup.tar.gz -k backups/2026-10-21.tar.gz
  r2up ls site/`,
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
		// cobra 只在报“未知命令”时才默认设为 2，这里由 checkTypo 自己调用 SuggestionsFor。
		SuggestionsMinimumDistance: 2,
		// 设置 Args 后，cobra 不再把未知的首个参数当作“未知命令”报错，而是交给 RunE。
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			if err := checkTypo(cmd, args[0]); err != nil {
				return err
			}
			return runUpload(cmd, g, uf, args)
		},
	}
	addUploadFlags(root.Flags(), uf)
	pf := root.PersistentFlags()
	pf.StringVar(&g.configPath, "config", "", "配置文件路径（默认 "+config.DefaultPath()+"）")
	pf.StringVarP(&g.bucket, "bucket", "b", "", "存储桶名称")
	pf.StringVar(&g.accountID, "account-id", "", "Cloudflare 账户 ID")
	pf.StringVar(&g.endpoint, "endpoint", "", "自定义 S3 API 地址（覆盖 account-id）")
	pf.StringVar(&g.publicURL, "public-url", "", "公开访问域名，用于输出对象链接，如 https://cdn.example.com")

	root.AddCommand(
		newInitCmd(g),
		newUploadCmd(g),
		newLsCmd(g),
		newRmCmd(g),
		newPresignCmd(g),
	)
	return root
}

// checkTypo 在首个参数既不是本地文件、又与某个子命令相近时报错，
// 避免把 `r2up uplod x` 这类拼写错误当成上传一个叫 uplod 的文件。
func checkTypo(cmd *cobra.Command, arg string) error {
	if _, err := os.Stat(arg); !errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if s := cmd.SuggestionsFor(arg); len(s) > 0 {
		return fmt.Errorf("%q 既不是子命令也不是本地文件，是否想用: %s", arg, strings.Join(s, ", "))
	}
	return nil
}

// configFile 返回配置文件路径，以及它是否由 --config 显式指定。
func (g *globalFlags) configFile() (path string, explicit bool) {
	if g.configPath != "" {
		return g.configPath, true
	}
	return config.DefaultPath(), false
}

func (g *globalFlags) load() (config.Config, error) {
	path, explicit := g.configFile()
	cfg, err := config.Load(path, explicit)
	if err != nil {
		return cfg, err
	}
	g.applyOverrides(&cfg)
	return cfg, nil
}

// applyOverrides 用命令行参数覆盖配置。
func (g *globalFlags) applyOverrides(cfg *config.Config) {
	for _, o := range []struct {
		flag  string
		field *string
	}{
		{g.bucket, &cfg.Bucket},
		{g.accountID, &cfg.AccountID},
		{g.endpoint, &cfg.Endpoint},
		{g.publicURL, &cfg.PublicURL},
	} {
		if o.flag != "" {
			*o.field = o.flag
		}
	}
}

func (g *globalFlags) client() (*r2.Client, error) {
	cfg, err := g.load()
	if err != nil {
		return nil, err
	}
	return r2.NewClient(cfg)
}
