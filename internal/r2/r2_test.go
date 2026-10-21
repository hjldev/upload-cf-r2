package r2

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func keys(jobs []Job) []string {
	var ks []string
	for _, j := range jobs {
		ks = append(ks, j.Key)
	}
	slices.Sort(ks)
	return ks
}

func TestCollectJobs(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"dist/index.html":     "<html>",
		"dist/js/app.js":      "x",
		"dist/js/app.js.map":  "x",
		"dist/.DS_Store":      "x",
		"dist/node_modules/a": "x",
		"single.txt":          "hello",
		"other/single.txt":    "dup",
	})
	dist := filepath.Join(root, "dist")
	single := filepath.Join(root, "single.txt")

	tests := []struct {
		name  string
		paths []string
		opts  CollectOptions
		want  []string
	}{
		{"单个文件", []string{single}, CollectOptions{}, []string{"single.txt"}},
		{"文件加前缀", []string{single}, CollectOptions{Prefix: "/docs/"}, []string{"docs/single.txt"}},
		{"指定 key", []string{single}, CollectOptions{Key: "/a/b.txt"}, []string{"a/b.txt"}},
		{
			"目录排除", []string{dist},
			CollectOptions{Prefix: "site", Excludes: []string{".DS_Store", "*.map", "node_modules"}},
			[]string{"site/index.html", "site/js/app.js"},
		},
		{
			"排除相对路径", []string{dist},
			CollectOptions{Excludes: []string{"js/*", ".*", "node_modules"}},
			[]string{"index.html"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jobs, err := CollectJobs(tt.paths, tt.opts)
			if err != nil {
				t.Fatal(err)
			}
			if got := keys(jobs); !slices.Equal(got, tt.want) {
				t.Errorf("keys = %v, want %v", got, tt.want)
			}
		})
	}

	if _, err := CollectJobs([]string{single, filepath.Join(root, "other/single.txt")}, CollectOptions{}); err == nil {
		t.Error("两个文件映射到相同 key 时应报错")
	}
	if _, err := CollectJobs([]string{single, dist}, CollectOptions{Key: "x"}); err == nil {
		t.Error("--key 搭配多个路径时应报错")
	}
	if _, err := CollectJobs([]string{dist}, CollectOptions{Key: "x"}); err == nil {
		t.Error("--key 搭配目录时应报错")
	}
	if _, err := CollectJobs([]string{dist}, CollectOptions{Excludes: []string{"["}}); err == nil {
		t.Error("无效的 glob 模式应报错")
	}
}

func TestPartSizeFor(t *testing.T) {
	if got := PartSizeFor(100*MiB, 1*MiB); got != minPartSize {
		t.Errorf("分片大小应不小于 5 MiB, got %d", got)
	}
	if got := PartSizeFor(100*MiB, 16*MiB); got != 16*MiB {
		t.Errorf("小文件应使用指定分片大小, got %d", got)
	}
	size := int64(4) << 40 // 4 TiB
	ps := PartSizeFor(size, 16*MiB)
	if parts := (size + ps - 1) / ps; parts > maxParts {
		t.Errorf("分片数 %d 超过上限 %d", parts, maxParts)
	}
	if ps%MiB != 0 {
		t.Errorf("分片大小应按 MiB 对齐, got %d", ps)
	}
}

func TestObjectURL(t *testing.T) {
	if got := ObjectURL("", "a.txt"); got != "" {
		t.Errorf("未配置 public_url 应返回空, got %q", got)
	}
	got := ObjectURL("https://cdn.example.com/", "图片/my photo.jpg")
	want := "https://cdn.example.com/%E5%9B%BE%E7%89%87/my%20photo.jpg"
	if got != want {
		t.Errorf("ObjectURL = %q, want %q", got, want)
	}
}

func TestProgressReaderSeek(t *testing.T) {
	var total int64
	r := &progressReader{r: strings.NewReader("hello world"), onBytes: func(n int64) { total += n }}
	if _, err := io.ReadAll(r); err != nil {
		t.Fatal(err)
	}
	if total != 11 {
		t.Fatalf("total = %d, want 11", total)
	}
	// 模拟 SDK 重试时回退到开头
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if total != 0 {
		t.Fatalf("回退后 total = %d, want 0", total)
	}
	_, _ = io.ReadAll(r)
	if total != 11 {
		t.Fatalf("重读后 total = %d, want 11", total)
	}
}
