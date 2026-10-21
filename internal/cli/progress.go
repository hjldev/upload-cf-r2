package cli

import (
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// progress 汇总多个并发上传的进度。终端下会在 stderr 刷新一行状态，
// 结果行写到 stdout；输出被重定向时只打印结果行。
type progress struct {
	mu         sync.Mutex
	errw       io.Writer
	live       bool
	totalFiles int
	totalBytes int64
	start      time.Time

	doneFiles atomic.Int64
	sent      atomic.Int64

	stop chan struct{}
	done chan struct{}
}

func newProgress(errw io.Writer, files int, bytes int64, live bool) *progress {
	p := &progress{
		errw:       errw,
		live:       live && isTerminal(errw),
		totalFiles: files,
		totalBytes: bytes,
		start:      time.Now(),
		stop:       make(chan struct{}),
		done:       make(chan struct{}),
	}
	go p.loop()
	return p
}

func (p *progress) loop() {
	defer close(p.done)
	if !p.live {
		<-p.stop
		return
	}
	t := time.NewTicker(200 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			p.mu.Lock()
			p.render()
			p.mu.Unlock()
		case <-p.stop:
			return
		}
	}
}

func (p *progress) addBytes(n int64) { p.sent.Add(n) }

// fileDone 标记一个文件结束（成功、跳过或失败），把它未发送的字节计入完成量，
// 保证全部结束时进度为 100%。
func (p *progress) fileDone(size, sent int64) {
	p.sent.Add(size - sent)
	p.doneFiles.Add(1)
}

func (p *progress) printf(w io.Writer, format string, args ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.clear()
	fmt.Fprintf(w, format, args...)
	p.render()
}

func (p *progress) finish() {
	close(p.stop)
	<-p.done
	p.mu.Lock()
	p.clear()
	p.mu.Unlock()
}

func (p *progress) elapsed() time.Duration { return time.Since(p.start) }

func (p *progress) clear() {
	if p.live {
		fmt.Fprint(p.errw, "\r\033[K")
	}
}

func (p *progress) render() {
	if !p.live {
		return
	}
	sent := p.sent.Load()
	pct := 100.0
	if p.totalBytes > 0 {
		pct = float64(sent) * 100 / float64(p.totalBytes)
	}
	fmt.Fprintf(p.errw, "\r\033[K[%d/%d] %s / %s  %.0f%%  %s/s",
		p.doneFiles.Load(), p.totalFiles,
		humanBytes(sent), humanBytes(p.totalBytes), pct,
		humanBytes(rate(sent, time.Since(p.start))))
}

func rate(n int64, d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	return int64(float64(n) / d.Seconds())
}

func roundDuration(d time.Duration) time.Duration {
	if d < time.Second {
		return d.Round(time.Millisecond)
	}
	return d.Round(100 * time.Millisecond)
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
