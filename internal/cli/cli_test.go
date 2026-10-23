package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"

	"upload-cf/internal/config"
	"upload-cf/internal/r2"
)

// 用内存版 S3 服务端跑完整的命令行流程。
func TestEndToEnd(t *testing.T) {
	srv := newFakeS3(t, "test")

	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("R2_ACCESS_KEY_ID", "test-id")
	t.Setenv("R2_SECRET_ACCESS_KEY", "test-secret")
	t.Setenv("R2_BUCKET", "test")
	t.Setenv("R2_ENDPOINT", srv.URL)

	dir := t.TempDir()
	big := make([]byte, 12*r2.MiB+123) // 5 MiB 分片 → 3 片
	if _, err := rand.Read(big); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"a.txt":        []byte("hello r2"),
		"sub/b.html":   []byte("<h1>hi</h1>"),
		".DS_Store":    []byte("junk"),
		"data/big.bin": big,
	}
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	client, err := r2.NewClient(config.Config{Endpoint: srv.URL, AccessKeyID: "x", SecretAccessKey: "y", Bucket: "test"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	stdout, stderr, err := run(t, "upload", dir, "-p", "site", "-x", ".DS_Store",
		"--multipart-threshold", "5", "--part-size", "5", "--public-url", "https://cdn.example.com")
	if err != nil {
		t.Fatalf("upload 失败: %v\n%s", err, stderr)
	}
	if !strings.Contains(stdout, "https://cdn.example.com/site/sub/b.html") {
		t.Errorf("输出中缺少公开链接:\n%s", stdout)
	}
	if !strings.Contains(stderr, "上传 3，跳过 0，失败 0") {
		t.Errorf("汇总不符:\n%s", stderr)
	}

	for name, want := range files {
		key := "site/" + name
		obj, err := client.S3.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("test"), Key: &key})
		if name == ".DS_Store" {
			if err == nil {
				t.Errorf("%s 应被排除", key)
			}
			continue
		}
		if err != nil {
			t.Fatalf("获取 %s: %v", key, err)
		}
		got, _ := io.ReadAll(obj.Body)
		obj.Body.Close()
		if !bytes.Equal(got, want) {
			t.Errorf("%s 内容不一致（%d vs %d 字节）", key, len(got), len(want))
		}
		if name == "sub/b.html" && !strings.HasPrefix(aws.ToString(obj.ContentType), "text/html") {
			t.Errorf("%s Content-Type = %q", key, aws.ToString(obj.ContentType))
		}
	}

	_, stderr, err = run(t, "upload", dir, "-p", "site", "-x", ".DS_Store", "--skip-existing")
	if err != nil || !strings.Contains(stderr, "上传 0，跳过 3") {
		t.Errorf("--skip-existing 未生效: %v\n%s", err, stderr)
	}

	stdout, _, err = run(t, "ls", "-r", "site/")
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"site/a.txt", "site/sub/b.html", "site/data/big.bin"} {
		if !strings.Contains(stdout, k) {
			t.Errorf("ls 输出缺少 %s:\n%s", k, stdout)
		}
	}
	stdout, _, _ = run(t, "ls", "site/")
	if !strings.Contains(stdout, "PRE") || strings.Contains(stdout, "site/sub/b.html") {
		t.Errorf("非递归 ls 应只显示当前层级:\n%s", stdout)
	}

	// 不写子命令时默认执行 upload，参数可放在文件前后。
	if _, stderr, err := run(t, "-p", "short", filepath.Join(dir, "a.txt"), "-q"); err != nil {
		t.Fatalf("r2up <文件> 上传失败: %v\n%s", err, stderr)
	}
	if _, err := client.S3.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String("test"), Key: aws.String("short/a.txt")}); err != nil {
		t.Errorf("r2up <文件> 未上传到 short/a.txt: %v", err)
	}

	stdout, _, err = run(t, "presign", "site/a.txt", "-e", "10m")
	if err != nil || !strings.HasPrefix(stdout, srv.URL) || !strings.Contains(stdout, "X-Amz-Signature=") {
		t.Errorf("presign 输出异常: %v\n%s", err, stdout)
	}

	if _, _, err := run(t, "rm", "site/a.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.S3.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String("test"), Key: aws.String("site/a.txt")}); err == nil {
		t.Error("rm 后对象仍然存在")
	}
}

func TestUploadMissingCredentials(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, k := range []string{"R2_ACCESS_KEY_ID", "R2_SECRET_ACCESS_KEY", "R2_BUCKET", "R2_ACCOUNT_ID", "R2_ENDPOINT"} {
		t.Setenv(k, "")
	}
	f := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := run(t, "upload", f)
	if err == nil || !strings.Contains(err.Error(), "配置不完整") {
		t.Errorf("缺少凭证时应提示配置不完整, got %v", err)
	}
}

func TestRootWithoutSubcommand(t *testing.T) {
	stdout, _, err := run(t)
	if err != nil || !strings.Contains(stdout, "Usage:") {
		t.Errorf("无参数时应输出帮助: %v\n%s", err, stdout)
	}

	t.Chdir(t.TempDir())
	_, _, err = run(t, "uplod", "x.txt")
	if err == nil || !strings.Contains(err.Error(), "upload") {
		t.Errorf("拼错子命令时应给出建议, got %v", err)
	}

	// 与子命令名相近、但确实存在的本地文件应照常上传，而不是报拼写错误。
	if err := os.WriteFile("lss", []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("R2_BUCKET", "test")
	stdout, _, err = run(t, "lss", "-n")
	if err != nil || !strings.Contains(stdout, "r2://test/lss") {
		t.Errorf("本地文件 lss 应被当作上传目标: %v\n%s", err, stdout)
	}
}

func TestInit(t *testing.T) {
	clearR2Env(t)
	path := filepath.Join(t.TempDir(), "r2up.json")
	const acct = "0123456789abcdef0123456789abcdef"

	// 首次创建：账户 ID 处粘贴 EU 终结点地址，公开域名省略协议
	in := "https://" + acct + ".eu.r2.cloudflarestorage.com\nkey-id\nsecret-0001\nmy-bucket\ncdn.example.com/\n"
	if _, stderr, err := runIn(t, in, "init", "--config", path, "--no-verify"); err != nil {
		t.Fatalf("init 失败: %v\n%s", err, stderr)
	}
	want := config.Config{
		AccountID: acct, Jurisdiction: "eu", AccessKeyID: "key-id", SecretAccessKey: "secret-0001",
		Bucket: "my-bucket", PublicURL: "https://cdn.example.com",
	}
	if got := loadConfig(t, path); got != want {
		t.Errorf("写入的配置 = %+v, want %+v", got, want)
	}

	// 再次运行时全部回车：保留原值，密钥只显示末 4 位
	_, stderr, err := runIn(t, "\n\n\n\n\n", "init", "--config", path, "--no-verify")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stderr, "secret-0001") || !strings.Contains(stderr, "****0001") {
		t.Errorf("密钥默认值应打码显示:\n%s", stderr)
	}
	if got := loadConfig(t, path); got != want {
		t.Errorf("回车应保留原值, got %+v", got)
	}

	// 无效输入会重新询问；输入 - 清空公开域名
	_, stderr, err = runIn(t, "not-an-id\n\n\n\n\n-\n", "init", "--config", path, "--no-verify")
	if err != nil || !strings.Contains(stderr, "32 位十六进制") {
		t.Errorf("无效账户 ID 应提示并重新询问: %v\n%s", err, stderr)
	}
	want.PublicURL = ""
	if got := loadConfig(t, path); got != want {
		t.Errorf("输入 - 应清空公开域名, got %+v", got)
	}

	// 输入中途结束时不生成文件
	fresh := filepath.Join(t.TempDir(), "fresh.json")
	if _, _, err := runIn(t, acct+"\n", "init", "--config", fresh, "--no-verify"); err == nil {
		t.Error("输入不完整时应报错")
	}
	if _, err := os.Stat(fresh); !os.IsNotExist(err) {
		t.Error("输入不完整时不应生成配置文件")
	}
}

func TestInitVerify(t *testing.T) {
	clearR2Env(t)
	srv := newFakeS3(t, "test")
	dir := t.TempDir()

	// 使用自定义 endpoint 时账户 ID 可以留空
	ok := filepath.Join(dir, "ok.json")
	_, stderr, err := runIn(t, "\nid\nsecret\ntest\n\n", "init", "--config", ok, "--endpoint", srv.URL)
	if err != nil || !strings.Contains(stderr, "成功") {
		t.Fatalf("连接测试应成功: %v\n%s", err, stderr)
	}
	if got := loadConfig(t, ok); got.Endpoint != srv.URL || got.Bucket != "test" {
		t.Errorf("写入的配置不符: %+v", got)
	}

	// 存储桶不存在：给出提示，回答 n 则不保存
	bad := filepath.Join(dir, "bad.json")
	_, stderr, err = runIn(t, "\nid\nsecret\nnope\n\nn\n", "init", "--config", bad, "--endpoint", srv.URL)
	if err == nil || !strings.Contains(stderr, "存储桶不存在") {
		t.Errorf("连接失败应提示原因并报错: %v\n%s", err, stderr)
	}
	if _, err := os.Stat(bad); !os.IsNotExist(err) {
		t.Error("拒绝保存时不应生成配置文件")
	}

	// 回答 y 则仍然保存
	if _, stderr, err := runIn(t, "\nid\nsecret\nnope\n\ny\n", "init", "--config", bad, "--endpoint", srv.URL); err != nil {
		t.Errorf("选择仍然保存时不应报错: %v\n%s", err, stderr)
	}
	if got := loadConfig(t, bad); got.Bucket != "nope" {
		t.Errorf("应保存配置, got %+v", got)
	}
}

func TestMultipart(t *testing.T) {
	clearR2Env(t)
	srv := newFakeS3(t, "test")
	t.Setenv("R2_ACCESS_KEY_ID", "test-id")
	t.Setenv("R2_SECRET_ACCESS_KEY", "test-secret")
	t.Setenv("R2_BUCKET", "test")
	t.Setenv("R2_ENDPOINT", srv.URL)

	client, err := r2.NewClient(config.Config{Endpoint: srv.URL, AccessKeyID: "x", SecretAccessKey: "y", Bucket: "test"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// 模拟中断的上传：创建分片上传、传了部分分片，但没有完成也没有取消。
	startUpload := func(key string, partSizes ...int) {
		t.Helper()
		create, err := client.S3.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{Bucket: aws.String("test"), Key: &key})
		if err != nil {
			t.Fatal(err)
		}
		for i, n := range partSizes {
			_, err := client.S3.UploadPart(ctx, &s3.UploadPartInput{
				Bucket: aws.String("test"), Key: &key, UploadId: create.UploadId,
				PartNumber: aws.Int32(int32(i + 1)), Body: bytes.NewReader(make([]byte, n)),
			})
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	startUpload("tmp/big.bin", 5*r2.MiB, 1024)
	startUpload("keep/x.bin", 2048)

	stdout, stderr, err := run(t, "multipart")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "2 片") || !strings.Contains(stdout, "tmp/big.bin") || !strings.Contains(stdout, "keep/x.bin") {
		t.Errorf("multipart 输出不符:\n%s", stdout)
	}
	if want := "共 2 个未完成的分片上传，占用 " + humanBytes(5*r2.MiB+1024+2048); !strings.Contains(stderr, want) {
		t.Errorf("汇总应包含 %q:\n%s", want, stderr)
	}

	_, stderr, _ = run(t, "ls")
	if !strings.Contains(stderr, "另有 2 个未完成的分片上传") {
		t.Errorf("ls 应提示未完成的分片上传:\n%s", stderr)
	}

	_, stderr, _ = run(t, "mp", "ls", "--older-than", "1h")
	if !strings.Contains(stderr, "共 0 个") || !strings.Contains(stderr, "另有 2 个开始不到 1h0m0s") {
		t.Errorf("--older-than 应过滤掉刚开始的上传:\n%s", stderr)
	}

	// 未确认时不清理：回答 n，或者非交互输入直接结束
	if _, _, err := runIn(t, "n\n", "multipart", "clean", "tmp/"); err == nil {
		t.Error("回答 n 时应报错")
	}
	if _, _, err := run(t, "multipart", "clean", "tmp/"); err == nil || !strings.Contains(err.Error(), "-y") {
		t.Errorf("无输入时应提示加 -y, got %v", err)
	}
	if n, _ := client.CountIncompleteUploads(ctx, ""); n != 2 {
		t.Fatalf("未确认时不应清理，剩余 %d 个", n)
	}

	stdout, _, err = run(t, "multipart", "clean", "tmp/", "-y")
	if err != nil || !strings.Contains(stdout, "已清理 tmp/big.bin") {
		t.Errorf("clean -y 失败: %v\n%s", err, stdout)
	}
	left, err := client.ListIncompleteUploads(ctx, "")
	if err != nil || len(left) != 1 || left[0].Key != "keep/x.bin" {
		t.Errorf("应只清理前缀 tmp/ 下的上传，剩余 %+v, %v", left, err)
	}

	if _, _, err := runIn(t, "y\n", "multipart", "clean"); err != nil {
		t.Errorf("确认后清理失败: %v", err)
	}
	if n, _ := client.CountIncompleteUploads(ctx, ""); n != 0 {
		t.Errorf("全部清理后仍剩 %d 个", n)
	}
	_, stderr, _ = run(t, "multipart", "clean")
	if !strings.Contains(stderr, "没有需要清理") {
		t.Errorf("没有未完成上传时应如实提示:\n%s", stderr)
	}
}

func TestHumanAge(t *testing.T) {
	for d, want := range map[time.Duration]string{
		-time.Second:                  "0s",
		45 * time.Second:              "45s",
		5 * time.Minute:               "5m",
		3*time.Hour + 12*time.Minute:  "3h12m",
		50*time.Hour + 30*time.Minute: "2d2h",
	} {
		if got := humanAge(d); got != want {
			t.Errorf("humanAge(%v) = %q, want %q", d, got, want)
		}
	}
}

func newFakeS3(t *testing.T, buckets ...string) *httptest.Server {
	t.Helper()
	backend := s3mem.New()
	for _, b := range buckets {
		if err := backend.CreateBucket(b); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(gofakes3.New(backend).Server())
	t.Cleanup(srv.Close)
	return srv
}

func clearR2Env(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, k := range []string{"R2_ACCOUNT_ID", "R2_ACCESS_KEY_ID", "R2_SECRET_ACCESS_KEY", "R2_BUCKET", "R2_PUBLIC_URL", "R2_JURISDICTION", "R2_ENDPOINT"} {
		t.Setenv(k, "")
	}
}

func loadConfig(t *testing.T, path string) config.Config {
	t.Helper()
	cfg, err := config.Load(path, true)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func run(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	return runIn(t, "", args...)
}

func runIn(t *testing.T, stdin string, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd := NewRootCmd("test")
	cmd.SetIn(strings.NewReader(stdin))
	// args 为 nil 时 cobra 会回退读取 os.Args（即 go test 的参数）。
	cmd.SetArgs(append([]string{}, args...))
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	err := cmd.ExecuteContext(context.Background())
	return stdout.String(), stderr.String(), err
}
