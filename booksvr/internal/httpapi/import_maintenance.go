package httpapi

import (
	"context"
	"sync"
)

// Shared across the importer and audit attachment; callbacks run after commit.
type importMaintenanceCheckpoint struct {
	mu  sync.RWMutex
	run func(context.Context)
}

func (m *importMaintenanceCheckpoint) Set(run func(context.Context)) {
	m.mu.Lock()
	m.run = run
	m.mu.Unlock()
}
func (m *importMaintenanceCheckpoint) Run(ctx context.Context) {
	m.mu.RLock()
	run := m.run
	m.mu.RUnlock()
	if run != nil && ctx.Err() == nil {
		run(ctx)
	}
}
