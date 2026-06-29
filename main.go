package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/alecthomas/kong"
)

func main() {
	runCtx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	ctx := kong.Parse(
		&CLI{},
		kong.Name("etcd-backup"),
		kong.Description("Simple etcd backup and defragmentation tool."),
		kong.UsageOnError(),
		kong.BindFor(runCtx),
	)

	ctx.FatalIfErrorf(ctx.Run())
}
