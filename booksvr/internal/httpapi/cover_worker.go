package httpapi

import (
	"context"
	"database/sql"
	"sync"
	"talebook/booksvr/internal/coverart"
	"time"
)

func attachCoverWorker(parent context.Context, db *sql.DB, root string) (func(), error) {
	if db == nil {
		return func() {}, nil
	}
	if err := coverart.MigrateQueue(db); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		var wg sync.WaitGroup
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				idle := time.Second
				for {
					if ctx.Err() != nil {
						return
					}
					worked, err := coverart.WorkOne(ctx, db, root)
					if worked && err == nil {
						idle = time.Second
						continue
					}
					select {
					case <-ctx.Done():
						return
					case <-time.After(idle):
					}
					if err == nil {
						idle *= 2
						if idle > 15*time.Second {
							idle = 15 * time.Second
						}
					} else {
						idle = time.Second
					}
				}
			}()
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					_ = coverart.RecheckStopped(ctx, db, 16)
				}
			}
		}()
		wg.Wait()
	}()
	return func() { cancel(); <-done }, nil
}
