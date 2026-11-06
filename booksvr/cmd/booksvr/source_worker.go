package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"talebook/booksvr/internal/httpapi"
)

func runSourceSyncWorker() error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return httpapi.RunSourceSyncWorker(ctx, os.Stdin, os.Stdout)
}
