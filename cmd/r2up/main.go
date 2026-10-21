package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"upload-cf/internal/cli"
)

// version 在构建时通过 -ldflags "-X main.version=..." 注入。
var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := cli.NewRootCmd(version).ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		stop()
		os.Exit(1)
	}
}
