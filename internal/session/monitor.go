package session

import (
	"context"
	"sync"
	"time"

	"github.com/prettyletto/listen/internal/segment"
)

type PositionReader interface {
	Position(context.Context) (uint64, time.Duration, error)
}

type Monitor struct {
	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
	mu       sync.Mutex
	err      error
}

// StartMonitor checkpoints mpv's current stable segment and offset. Position
// polling uses short independent contexts so cancellation can still flush one
// final checkpoint before the player is closed.
func StartMonitor(ctx context.Context, player PositionReader, store *Store, state State, plan segment.Plan, interval time.Duration) *Monitor {
	if ctx == nil || player == nil || store == nil || len(plan.Segments) == 0 {
		return nil
	}
	if interval <= 0 {
		interval = time.Second
	}
	monitor := &Monitor{stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(monitor.done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		checkpoint := func() {
			queryCtx, queryCancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
			seq, offset, err := player.Position(queryCtx)
			queryCancel()
			if err != nil {
				return
			}
			current, ok := RecordPosition(state, plan, seq, offset)
			if !ok {
				return
			}
			saveCtx, saveCancel := context.WithTimeout(context.Background(), 2*time.Second)
			err = store.Save(saveCtx, current)
			saveCancel()
			monitor.mu.Lock()
			monitor.err = err
			monitor.mu.Unlock()
			if err == nil {
				state = current
			}
		}
		for {
			select {
			case <-ctx.Done():
				checkpoint()
				return
			case <-monitor.stop:
				checkpoint()
				return
			case <-ticker.C:
				checkpoint()
			}
		}
	}()
	return monitor
}

func (m *Monitor) Stop() error {
	if m == nil {
		return nil
	}
	m.stopOnce.Do(func() { close(m.stop) })
	<-m.done
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.err
}
