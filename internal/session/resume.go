package session

import (
	"strings"
	"time"

	"github.com/prettyletto/listen/internal/segment"
)

type ResumePoint struct {
	Index  int
	Seq    uint64
	Offset time.Duration
	Exact  bool
}

// Resolve maps a saved playback position to a current plan. Stable segment
// IDs win; if the source changed, the nearest segment in the same major unit
// or section is used with its offset reset to zero.
func Resolve(plan segment.Plan, state State) (ResumePoint, bool) {
	if state.Complete || len(plan.Segments) == 0 {
		return ResumePoint{}, false
	}
	for index, item := range plan.Segments {
		if state.SegmentID != "" && item.ID == state.SegmentID {
			return ResumePoint{Index: index, Seq: item.Seq, Offset: nonNegative(state.Offset), Exact: true}, true
		}
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
	for _, item := range plan.Segments {
		if item.Seq != seq {
			continue
		}
		state.SegmentID = item.ID
		state.SegmentSeq = item.Seq
		state.MajorUnitID = item.MajorUnitID
		state.SectionPath = append([]string(nil), item.SectionPath...)
		state.Offset = offset
		state.Complete = false
		return state, true
	}
	return state, false
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
