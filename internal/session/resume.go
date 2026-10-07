package session

import (
	"strings"
	"sync"
	"time"

	"github.com/prettyletto/listen/internal/segment"
)

type ResumePoint struct {
	Index  int
	Seq    uint64
	Offset time.Duration
	Exact  bool
}

// PositionRecorder maps a player's current sequence and offset to a durable
// checkpoint. Live sources use a recorder because their complete plan is not
// available when playback starts.
type PositionRecorder func(state State, seq uint64, offset time.Duration) (State, bool)

// PositionResolver safely publishes streamed segments as the source discovers
// them. The playback monitor may read a sequence while the producer adds later
// pages concurrently.
type PositionResolver struct {
	mu       sync.RWMutex
	bySeq    map[uint64]segment.Segment
	pageSlot map[uint64]int
}

func NewPositionResolver() *PositionResolver {
	return &PositionResolver{bySeq: make(map[uint64]segment.Segment), pageSlot: make(map[uint64]int)}
}

// Add publishes a segment and its zero-based position among passages on the
// same source page.
func (r *PositionResolver) Add(item segment.Segment, pageSegment int) {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.bySeq == nil {
		r.bySeq = make(map[uint64]segment.Segment)
		r.pageSlot = make(map[uint64]int)
	}
	r.bySeq[item.Seq] = item
	r.pageSlot[item.Seq] = max(0, pageSegment)
	r.mu.Unlock()
}

func (r *PositionResolver) Record(state State, seq uint64, offset time.Duration) (State, bool) {
	if r == nil || offset < 0 || offset > maxResumeOffset {
		return state, false
	}
	r.mu.RLock()
	item, ok := r.bySeq[seq]
	pageSegment := r.pageSlot[seq]
	r.mu.RUnlock()
	if !ok {
		return state, false
	}
	return recordSegment(state, item, pageSegment, offset), true
}

// Page returns the source page for a streamed sequence after the producer has
// published it. It shares the resolver's lock so playback display and durable
// checkpoints use the same page-to-sequence mapping.
func (r *PositionResolver) Page(seq uint64) (int, bool) {
	if r == nil {
		return 0, false
	}
	r.mu.RLock()
	item, ok := r.bySeq[seq]
	r.mu.RUnlock()
	if !ok || item.Source.Page <= 0 {
		return 0, false
	}
	return item.Source.Page, true
}

// Resolve maps a saved playback position to a current plan. Stable segment
// IDs win; if the source changed, the nearest segment in the same major unit
// or section is used with its offset reset to zero.
func Resolve(plan segment.Plan, state State) (ResumePoint, bool) {
	if state.Complete || len(plan.Segments) == 0 {
		return ResumePoint{}, false
	}
	sameDocument := state.DocumentID != "" && plan.DocumentID != "" && state.DocumentID == plan.DocumentID
	for index, item := range plan.Segments {
		if state.SegmentID != "" && item.ID == state.SegmentID {
			offset := time.Duration(0)
			if sameDocument {
				offset = nonNegative(state.Offset)
			}
			return ResumePoint{Index: index, Seq: item.Seq, Offset: offset, Exact: sameDocument}, true
		}
	}
	if point, ok := resolvePDFPage(plan, state); ok {
		if sameDocument {
			point.Offset = nonNegative(state.Offset)
		}
		return point, true
	}

	bestIndex := -1
	bestPath := -1
	bestDistance := ^uint64(0)
	for index, item := range plan.Segments {
		pathScore := commonPathPrefix(item.SectionPath, state.SectionPath)
		if state.MajorUnitID != "" && item.MajorUnitID == state.MajorUnitID {
			pathScore += 100
		}
		if pathScore == 0 {
			continue
		}
		distance := sequenceDistance(item.Seq, state.SegmentSeq)
		if pathScore > bestPath || (pathScore == bestPath && distance < bestDistance) {
			bestIndex, bestPath, bestDistance = index, pathScore, distance
		}
	}
	if bestIndex < 0 {
		bestIndex = 0
		for index, item := range plan.Segments {
			if sequenceDistance(item.Seq, state.SegmentSeq) < sequenceDistance(plan.Segments[bestIndex].Seq, state.SegmentSeq) {
				bestIndex = index
			}
		}
	}
	item := plan.Segments[bestIndex]
	return ResumePoint{Index: bestIndex, Seq: item.Seq}, true
}

func RecordPosition(state State, plan segment.Plan, seq uint64, offset time.Duration) (State, bool) {
	if offset < 0 || offset > maxResumeOffset {
		return state, false
	}
	pageSegment := 0
	targetPage := pageForSequence(plan, seq)
	for _, item := range plan.Segments {
		if item.Seq != seq {
			if targetPage > 0 && item.Source.Page == targetPage {
				pageSegment++
			}
			continue
		}
		return recordSegment(state, item, pageSegment, offset), true
	}
	return state, false
}

func recordSegment(state State, item segment.Segment, pageSegment int, offset time.Duration) State {
	state.SegmentID = item.ID
	state.SegmentSeq = item.Seq
	state.Page = item.Source.Page
	state.PageSegment = max(0, pageSegment)
	state.MajorUnitID = item.MajorUnitID
	state.SectionPath = append([]string(nil), item.SectionPath...)
	state.Offset = offset
	state.Complete = false
	return state
}

func pageForSequence(plan segment.Plan, seq uint64) int {
	for _, item := range plan.Segments {
		if item.Seq == seq {
			return item.Source.Page
		}
	}
	return 0
}

func resolvePDFPage(plan segment.Plan, state State) (ResumePoint, bool) {
	if state.Page <= 0 {
		return ResumePoint{}, false
	}
	pageIndices := make(map[int][]int)
	for index, item := range plan.Segments {
		if item.Source.Page > 0 {
			pageIndices[item.Source.Page] = append(pageIndices[item.Source.Page], index)
		}
	}
	if len(pageIndices) == 0 {
		return ResumePoint{}, false
	}
	if indices := pageIndices[state.Page]; len(indices) > 0 {
		return ResumePoint{Index: nearestPageSlot(indices, state.PageSegment), Seq: plan.Segments[nearestPageSlot(indices, state.PageSegment)].Seq}, true
	}
	bestPage, bestDistance := 0, int(^uint(0)>>1)
	for page := range pageIndices {
		distance := page - state.Page
		if distance < 0 {
			distance = -distance
		}
		if distance < bestDistance || (distance == bestDistance && page < bestPage) {
			bestPage, bestDistance = page, distance
		}
	}
	indices := pageIndices[bestPage]
	index := nearestPageSlot(indices, state.PageSegment)
	return ResumePoint{Index: index, Seq: plan.Segments[index].Seq}, true
}

func nearestPageSlot(indices []int, slot int) int {
	if slot < 0 {
		slot = 0
	}
	if slot >= len(indices) {
		slot = len(indices) - 1
	}
	return indices[slot]
}

func commonPathPrefix(left, right []string) int {
	limit := len(left)
	if len(right) < limit {
		limit = len(right)
	}
	score := 0
	for index := 0; index < limit; index++ {
		if !strings.EqualFold(strings.TrimSpace(left[index]), strings.TrimSpace(right[index])) {
			break
		}
		score++
	}
	return score
}

func sequenceDistance(left, right uint64) uint64 {
	if left >= right {
		return left - right
	}
	return right - left
}

func nonNegative(offset time.Duration) time.Duration {
	if offset < 0 {
		return 0
	}
	return offset
}
