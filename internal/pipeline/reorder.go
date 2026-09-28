package pipeline

import "fmt"

// ReorderBuffer accepts values that may complete out of order and emits only a
// contiguous sequence beginning at next. The caller owns synchronization.
type ReorderBuffer[T any] struct {
	next    uint64
	pending map[uint64]T
}

func NewReorderBuffer[T any](next uint64) *ReorderBuffer[T] {
	return &ReorderBuffer[T]{next: next, pending: make(map[uint64]T)}
}

func (r *ReorderBuffer[T]) Push(seq uint64, value T) ([]T, error) {
	if seq < r.next {
		return nil, fmt.Errorf("sequence %d already committed; next=%d", seq, r.next)
	}
	if _, exists := r.pending[seq]; exists {
		return nil, fmt.Errorf("duplicate sequence %d", seq)
	}
	r.pending[seq] = value
	var ready []T
	for {
		value, ok := r.pending[r.next]
		if !ok {
			break
		}
		ready = append(ready, value)
		delete(r.pending, r.next)
		r.next++
	}
	return ready, nil
}

func (r *ReorderBuffer[T]) Next() uint64 { return r.next }
