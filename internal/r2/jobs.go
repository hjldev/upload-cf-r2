package r2

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Job 表示一个待上传的本地文件及其目标 key。
type Job struct {
	Path string
	Key  string
	Size int64
}

// CollectOptions 控制本地路径到对象 key 的映射。
type CollectOptions struct {
	// Prefix 会作为“目录”拼接在 key 前面。
	Prefix string
	// Key 为单个文件指定完整 key，设置后只允许传入一个文件。
	Key string
	// Excludes 是 glob 模式，匹配文件名或相对路径时跳过（目录会整体跳过）。
	Excludes []string
}

// CollectJobs 展开参数中的文件和目录。目录按 aws s3 cp --recursive 的方式处理：
// 目录内的相对路径拼接到 Prefix 之后，不包含目录本身的名字。
func CollectJobs(paths []string, opts CollectOptions) ([]Job, error) {
	for _, p := range opts.Excludes {
		if _, err := path.Match(p, ""); err != nil {
			return nil, fmt.Errorf("无效的 --exclude 模式 %q: %w", p, err)
		}
	}
	if opts.Key != "" {
		return collectSingle(paths, opts.Key)
	}

	var jobs []Job
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			if !info.Mode().IsRegular() {
				return nil, fmt.Errorf("%s 不是普通文件", p)
			}
			jobs = append(jobs, Job{Path: p, Key: joinKey(opts.Prefix, info.Name()), Size: info.Size()})
			continue
		}
		dirJobs, err := walkDir(p, opts)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, dirJobs...)
	}

	seen := make(map[string]string, len(jobs))
	for _, j := range jobs {
		if prev, ok := seen[j.Key]; ok {
			return nil, fmt.Errorf("%s 和 %s 会上传到同一个 key %q", prev, j.Path, j.Key)
		}
		seen[j.Key] = j.Path
	}
	return jobs, nil
}

func collectSingle(paths []string, key string) ([]Job, error) {
	if len(paths) != 1 {
		return nil, errors.New("使用 --key 时只能上传一个文件")
	}
	info, err := os.Stat(paths[0])
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("使用 --key 时 %s 必须是普通文件", paths[0])
	}
	return []Job{{Path: paths[0], Key: strings.TrimPrefix(key, "/"), Size: info.Size()}}, nil
}

func walkDir(root string, opts CollectOptions) ([]Job, error) {
	var jobs []Job
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if excluded(opts.Excludes, rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		// os.Stat 会跟随符号链接；指向目录或特殊文件的条目直接忽略。
		info, err := os.Stat(p)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		jobs = append(jobs, Job{Path: p, Key: joinKey(opts.Prefix, rel), Size: info.Size()})
		return nil
	})
	return jobs, err
}

func excluded(patterns []string, rel string) bool {
	base := path.Base(rel)
	for _, p := range patterns {
		if ok, _ := path.Match(p, base); ok {
			return true
		}
		if ok, _ := path.Match(p, rel); ok {
			return true
		}
	}
	return false
}

func joinKey(prefix, rel string) string {
	return strings.TrimPrefix(path.Join(filepath.ToSlash(prefix), rel), "/")
}
