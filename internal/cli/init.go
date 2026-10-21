package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"upload-cf/internal/config"
	"upload-cf/internal/r2"
)

func newInitCmd(g *globalFlags) *cobra.Command {
	var noVerify bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "交互式填写 R2 凭证并写入配置文件",
		Long: `按提示填写账户 ID、Access Key ID、Secret Access Key 和默认存储桶，
测试连接后写入配置文件（默认 ` + config.DefaultPath() + `，可用 --config 指定）。

凭证在 Cloudflare 控制台 R2 → 管理 R2 API 令牌 中创建。账户 ID 一栏可以直接粘贴
创建令牌后显示的 S3 终结点地址，会自动提取账户 ID 和辖区。
配置文件已存在时，[ ] 中显示当前值，直接回车即保留。`,
		Example: `  r2up init
  r2up init --config ./r2-work.json
  r2up init -b my-bucket     # 预先填好存储桶，回车确认即可`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			err := runInit(cmd, g, noVerify)
			if errors.Is(err, context.Canceled) {
				return errors.New("已取消，配置未保存")
			}
			return err
		},
	}
	cmd.Flags().BoolVar(&noVerify, "no-verify", false, "保存前不测试连接")
	return cmd
}

func runInit(cmd *cobra.Command, g *globalFlags, noVerify bool) error {
	path, _ := g.configFile()
	if path == "" {
		return errors.New("无法确定用户目录，请用 --config 指定配置文件路径")
	}
	// 文件不存在时从空配置开始；环境变量和命令行参数中的值会作为默认值。
	cfg, err := config.Load(path, false)
	if err != nil {
		return err
	}
	g.applyOverrides(&cfg)

	ctx := cmd.Context()
	errw := cmd.ErrOrStderr()
	p := newPrompter(cmd.InOrStdin(), errw)
	if _, err := os.Stat(path); err == nil {
		fmt.Fprintf(errw, "更新配置文件 %s，直接回车保留 [ ] 中的当前值。\n\n", path)
	} else {
		fmt.Fprintf(errw, "创建配置文件 %s\n请先在 Cloudflare 控制台 R2 → 管理 R2 API 令牌 中创建令牌（权限：对象读和写）。\n\n", path)
	}

	acct, err := p.ask(ctx, "账户 ID（也可粘贴 S3 终结点地址）", cfg.AccountID, false, func(s string) error {
		if s == "" && cfg.Endpoint != "" {
			return nil // 使用自定义 endpoint 时不需要账户 ID
		}
		_, _, _, err := config.ParseAccount(s)
		return err
	})
	if err != nil {
		return err
	}
	if acct != "" {
		id, jurisdiction, fromEndpoint, _ := config.ParseAccount(acct)
		cfg.AccountID = id
		if fromEndpoint {
			cfg.Jurisdiction = jurisdiction
		}
	}
	if cfg.AccessKeyID, err = p.ask(ctx, "Access Key ID", cfg.AccessKeyID, false, required("Access Key ID")); err != nil {
		return err
	}
	if cfg.SecretAccessKey, err = p.ask(ctx, "Secret Access Key（输入时不显示）", cfg.SecretAccessKey, true, required("Secret Access Key")); err != nil {
		return err
	}
	if cfg.Bucket, err = p.ask(ctx, "默认存储桶", cfg.Bucket, false, required("存储桶")); err != nil {
		return err
	}
	pub, err := p.ask(ctx, "公开访问域名（可选，输入 - 清空）", cfg.PublicURL, false, func(s string) error {
		_, err := normalizePublicURL(s)
		return err
	})
	if err != nil {
		return err
	}
	cfg.PublicURL, _ = normalizePublicURL(pub)

	if !noVerify {
		fmt.Fprint(errw, "\n正在测试连接… ")
		if err := verifyConfig(ctx, cfg); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			fmt.Fprintf(errw, "失败\n  %v\n", err)
			if hint := verifyHint(err); hint != "" {
				fmt.Fprintf(errw, "  提示: %s\n", hint)
			}
			ok, err := p.confirm(ctx, "仍然保存配置? [y/N]")
			if err != nil {
				return err
			}
			if !ok {
				return errors.New("配置未保存")
			}
		} else {
			fmt.Fprintf(errw, "成功，可以访问存储桶 %s\n", cfg.Bucket)
		}
	}

	if err := config.Save(path, cfg); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "配置已保存到 %s\n", path)
	return nil
}

func verifyConfig(ctx context.Context, cfg config.Config) error {
	client, err := r2.NewClient(cfg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	// 列对象只需要读权限，比 HeadBucket 等操作对令牌权限的要求更低。
	_, err = client.S3.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: &cfg.Bucket, MaxKeys: aws.Int32(1)})
	return err
}

func verifyHint(err error) string {
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return "请检查网络连接和账户 ID"
	}
	switch apiErr.ErrorCode() {
	case "InvalidAccessKeyId", "SignatureDoesNotMatch", "Unauthorized":
		return "Access Key ID 或 Secret Access Key 不正确，注意不要填成令牌值（Token value）"
	case "AccessDenied":
		return "令牌没有该存储桶的权限，请确认权限为“对象读和写”且包含这个存储桶"
	case "NoSuchBucket":
		return "存储桶不存在，请检查名称"
	}
	return ""
}

func normalizePublicURL(s string) (string, error) {
	if s == "" || s == "-" {
		return "", nil
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return "", errors.New("请输入域名或 http(s) 地址，如 https://cdn.example.com")
	}
	return strings.TrimRight(s, "/"), nil
}

func required(name string) func(string) error {
	return func(s string) error {
		if s == "" {
			return fmt.Errorf("%s 不能为空", name)
		}
		return nil
	}
}

func maskSecret(s string) string {
	if len(s) <= 4 {
		return "****"
	}
	return "****" + s[len(s)-4:]
}

// prompter 逐行读取用户输入。输入来自终端时，密钥输入不回显；
// 来自管道时按行读取，便于脚本化：printf '...\n' | r2up init。
type prompter struct {
	in  *bufio.Reader
	tty *os.File
	out io.Writer
}

func newPrompter(in io.Reader, out io.Writer) *prompter {
	p := &prompter{in: bufio.NewReader(in), out: out}
	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		p.tty = f
	}
	return p
}

// ask 显示提示并读取一行，空输入时使用默认值；validate 不通过时重新询问。
func (p *prompter) ask(ctx context.Context, label, def string, secret bool, validate func(string) error) (string, error) {
	shown := def
	if secret && def != "" {
		shown = maskSecret(def)
	}
	for {
		if shown != "" {
			fmt.Fprintf(p.out, "%s [%s]: ", label, shown)
		} else {
			fmt.Fprintf(p.out, "%s: ", label)
		}
		line, err := p.readLine(ctx, secret)
		if err != nil {
			return "", err
		}
		if line == "" {
			line = def
		}
		if err := validate(line); err != nil {
			fmt.Fprintf(p.out, "  %v\n", err)
			continue
		}
		return line, nil
	}
}

func (p *prompter) confirm(ctx context.Context, label string) (bool, error) {
	fmt.Fprintf(p.out, "%s ", label)
	line, err := p.readLine(ctx, false)
	if err != nil {
		return false, err
	}
	switch strings.ToLower(line) {
	case "y", "yes":
		return true, nil
	}
	return false, nil
}

// readLine 在后台 goroutine 中读取，这样按 Ctrl+C（ctx 被取消）时能立即返回，
// 而不是一直卡在读取上；隐藏输入被中断时会恢复终端回显。
func (p *prompter) readLine(ctx context.Context, secret bool) (string, error) {
	hidden := secret && p.tty != nil
	var state *term.State
	if hidden {
		s, err := term.GetState(int(p.tty.Fd()))
		if err != nil {
			return "", err
		}
		state = s
	}

	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		var r result
		if hidden {
			var b []byte
			b, r.err = term.ReadPassword(int(p.tty.Fd()))
			r.line = string(b)
		} else {
			r.line, r.err = p.in.ReadString('\n')
			if r.err == io.EOF && r.line != "" {
				r.err = nil // 最后一行没有换行符
			}
		}
		ch <- r
	}()

	select {
	case r := <-ch:
		if hidden {
			fmt.Fprintln(p.out) // ReadPassword 不回显用户按下的回车
		}
		if r.err == io.EOF {
			return "", errors.New("输入已结束，配置未保存")
		}
		return strings.TrimSpace(r.line), r.err
	case <-ctx.Done():
		if state != nil {
			_ = term.Restore(int(p.tty.Fd()), state)
		}
		fmt.Fprintln(p.out)
		return "", ctx.Err()
	}
}
