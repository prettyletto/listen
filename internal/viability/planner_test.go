package viability

import (
	"testing"
	"time"
)

func TestClassifyRealtimeRatioThresholds(t *testing.T) {
	cases := []struct {
		ratio float64
		want  Classification
	}{
		{0, Unknown},
		{0.99, Unsustainable},
		{1, Risky},
		{1.24, Risky},
		{1.25, Sustainable},
		{1.49, Sustainable},
		{1.5, Healthy},
	}
	for _, test := range cases {
		if got := Classify(test.ratio); got != test.want {
			t.Errorf("Classify(%v) = %q, want %q", test.ratio, got, test.want)
		}
	}
}

func TestPlannerUsesEMAAndActualSpeechDurationCalibration(t *testing.T) {
	planner := NewPlanner(0.5)
	first := planner.ObserveTranslationDuration(10*time.Second, 20*time.Second)
	second := planner.ObserveTranslationDuration(10*time.Second, 10*time.Second)
	if first != 2 || second != 1.5 {
		t.Fatalf("translation EMA = %v then %v, want 2 then 1.5", first, second)
	}

	text := "one two three four five six seven eight nine ten"
	estimated := planner.EstimateSpeechDuration(text, "pt-BR")
	if estimated != 4*time.Second {
		t.Fatalf("10-word initial estimate = %v, want 4s", estimated)
	}
	planner.ObserveSpeech(text, "pt-BR", 2*time.Second, 6*time.Second)
	calibrated := planner.EstimateSpeechDuration(text, "pt-BR")
	if calibrated != 6*time.Second {
		t.Fatalf("calibrated estimate = %v, want 6s", calibrated)
	}
	state := planner.Snapshot()
	if state.SpeechRatio != 3 {
		t.Fatalf("speech realtime ratio = %v, want 3", state.SpeechRatio)
	}
}

func TestPlannerPredictsBufferUnderrunFromObservedTrend(t *testing.T) {
	planner := NewPlanner(1)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	planner.ObserveTranslationDuration(10*time.Second, 6*time.Second)
	planner.ObserveBuffer(start, 20)
	planner.ObserveBuffer(start.Add(10*time.Second), 14)
	state := planner.Snapshot()
	if state.Classification != Unsustainable || state.BufferTrendSecondsPerSecond != -0.6 {
		t.Fatalf("snapshot = %#v", state)
	}
	seconds := float64(14) / 0.6
	expectedUnderrun := time.Duration(seconds * float64(time.Second))
	if state.PredictedUnderrun != expectedUnderrun {
		t.Fatalf("predicted underrun = %v", state.PredictedUnderrun)
	}
}

func TestSelectAutoFiltersAndWarnsForSlowPlaybackButAllowsExport(t *testing.T) {
	candidates := []Candidate{
		{ID: "fast-remote", Local: false, Installed: true, SupportsPair: true, RealtimeRatio: 50},
		{ID: "missing", Local: true, Installed: false, SupportsPair: true, RealtimeRatio: 40},
		{ID: "wrong-pair", Local: true, Installed: true, SupportsPair: false, RealtimeRatio: 40},
		{ID: "fast-local", Local: true, Installed: true, SupportsPair: true, RealtimeRatio: 0.8},
		{ID: "slow-local", Local: true, Installed: true, SupportsPair: true, RealtimeRatio: 0.4},
	}
	selected, err := SelectAuto(candidates, false)
	if err != nil || selected.BackendID != "fast-local" || selected.Warning == "" {
		t.Fatalf("playback selection = %#v, err=%v", selected, err)
	}
	exported, err := SelectAuto(candidates[3:], true)
	if err != nil || exported.BackendID != "fast-local" || exported.Warning != "" {
		t.Fatalf("export selection = %#v, err=%v", exported, err)
	}
	if _, err := SelectAuto(candidates[:3], false); err == nil {
		t.Fatal("selection accepted no installed local backend supporting the pair")
	}
}
