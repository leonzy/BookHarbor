package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// SQLite extended result codes retain the primary code in the low byte.
func retryStartup(ctx context.Context, out io.Writer, start func() error) error {
	delay := time.Second
	for ctx.Err() == nil {
		err := start()
		var dbErr *sqlite.Error
		if !errors.As(err, &dbErr) {
			return err
		}
		switch dbErr.Code() & 0xff {
		case sqlite3.SQLITE_BUSY, sqlite3.SQLITE_LOCKED:
			fmt.Fprintf(out, "booksvr: 启动时 SQLite 锁冲突，%s 后重试: %v\n", delay, err)
		default:
			return err
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		delay = min(2*delay, 30*time.Second)
	}
	return ctx.Err()
}
