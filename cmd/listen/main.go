package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/prettyletto/listen/internal/app"
	"github.com/prettyletto/listen/internal/apperr"
	"github.com/prettyletto/listen/internal/ui"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := app.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		_ = ui.New(os.Stderr).Error(err)
		if apperr.HasCode(err, "interrupted") {
			os.Exit(130)
		}
		os.Exit(1)
	}
}
