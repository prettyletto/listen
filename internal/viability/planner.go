// Package viability measures whether local translation and speech can keep
// producing playable audio while the reader is listening.
package viability

import (
	"errors"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

type Classification string

const (
	Unknown       Classification = "unknown"
	Healthy       Classification = "healthy"
	Sustainable   Classification = "sustainable"
	Risky         Classification = "risky"
	Unsustainable Classification = "unsustainable"
)

const (
	HealthyThreshold     = 1.50
	SustainableThreshold = 1.25
	RiskyThreshold       = 1.00
	DefaultWordsPerMin   = 150.0
)

type Snapshot struct {
	TranslationRatio            float64
	SpeechRatio                 float64
	RealtimeRatio               float64
	Classification              Classification
	BufferSeconds               float64
	BufferTrendSecondsPerSecond float64
	PredictedUnderrun           time.Duration
}

// Planner keeps exponentially weighted stage measurements and spoken-duration
// calibration for one document session. It is safe for concurrent callbacks.
type Planner struct {
	mu                sync.Mutex
	alpha             float64
	translationRatio  float64
	speechRatio       float64
	durationScale     map[string]float64
	bufferObserved    bool
	lastBufferAt      time.Time
	lastBufferSeconds float64
	bufferTrend       float64
}

func NewPlanner(alpha float64) *Planner {
	if alpha <= 0 || alpha > 1 || math.IsNaN(alpha) {
		alpha = 0.25
	}
	return &Planner{alpha: alpha, durationScale: make(map[string]float64)}
}

// EstimateSpeechDuration estimates playable duration at a neutral 150 words
// per minute, then applies calibration from actual local TTS results.
func (p *Planner) EstimateSpeechDuration(text, locale string) time.Duration {
	words := countWords(text)
	if words == 0 {
		return 0
	}
	p.mu.Lock()
	scale := p.durationScale[normalizeLocale(locale)]
	p.mu.Unlock()
	if scale <= 0 {
		scale = 1
	}
	seconds := float64(words) * 60 / DefaultWordsPerMin * scale
	return time.Duration(seconds * float64(time.Second))
}

// ObserveTranslation records target-language audio estimated from completed
// translated segments and the wall time spent translating them.
func (p *Planner) ObserveTranslation(wall time.Duration, segments []MeasuredText) float64 {
	var audio time.Duration
	for _, item := range segments {
		audio += p.EstimateSpeechDuration(item.Text, item.Locale)
	}
	return p.ObserveTranslationDuration(wall, audio)
}

func (p *Planner) ObserveTranslationDuration(wall, estimatedAudio time.Duration) float64 {
	ratio := realtimeRatio(wall, estimatedAudio)
	if ratio <= 0 {
		return ratio
	}
	p.mu.Lock()
	p.translationRatio = ema(p.translationRatio, ratio, p.alpha)
	result := p.translationRatio
	p.mu.Unlock()
	return result
}

// MeasuredText couples translated text to its target locale for duration
// estimation. It intentionally contains no backend or model details.
type MeasuredText struct {
	Text   string
	Locale string
}

// ObserveSpeech records generated audio per wall-clock second and calibrates
// the target-locale duration estimate with the actual WAV duration.
func (p *Planner) ObserveSpeech(text, locale string, wall, actualAudio time.Duration) float64 {
	ratio := realtimeRatio(wall, actualAudio)
	if ratio <= 0 {
		return ratio
	}
	estimated := p.EstimateSpeechDuration(text, locale)
	key := normalizeLocale(locale)
	p.mu.Lock()
	p.speechRatio = ema(p.speechRatio, ratio, p.alpha)
	if estimated > 0 {
		observedScale := float64(actualAudio) / float64(estimated)
		if observedScale >= 0.25 && observedScale <= 4 {
			p.durationScale[key] = ema(p.durationScale[key], observedScale, p.alpha)
		}
	}
	result := p.speechRatio
	p.mu.Unlock()
	return result
}

// ObserveBuffer records playable seconds remaining after an ordered audio item
// enters the player queue. The trend uses seconds of audio per wall second.
func (p *Planner) ObserveBuffer(at time.Time, seconds float64) {
	if at.IsZero() || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 {
		return
	}
	p.mu.Lock()
	if p.bufferObserved {
		elapsed := at.Sub(p.lastBufferAt).Seconds()
		if elapsed > 0 {
			trend := (seconds - p.lastBufferSeconds) / elapsed
			p.bufferTrend = ema(p.bufferTrend, trend, p.alpha)
		}
	}
	p.bufferObserved = true
	p.lastBufferAt = at
	p.lastBufferSeconds = seconds
	p.mu.Unlock()
}

func (p *Planner) Snapshot() Snapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	return snapshot(p.translationRatio, p.speechRatio, p.bufferObserved, p.lastBufferSeconds, p.bufferTrend)
}

func snapshot(translationRatio, speechRatio float64, hasBuffer bool, bufferSeconds, trend float64) Snapshot {
	ratio := 0.0
	switch {
	case translationRatio > 0 && speechRatio > 0:
		ratio = math.Min(translationRatio, speechRatio)
	case translationRatio > 0:
		ratio = translationRatio
	case speechRatio > 0:
		ratio = speechRatio
	}
	state := Snapshot{
		TranslationRatio:            translationRatio,
		SpeechRatio:                 speechRatio,
		RealtimeRatio:               ratio,
		Classification:              Classify(ratio),
		BufferTrendSecondsPerSecond: trend,
	}
	if hasBuffer {
		state.BufferSeconds = bufferSeconds
		if trend < -0.01 {
			state.PredictedUnderrun = time.Duration(bufferSeconds / -trend * float64(time.Second))
		} else if ratio > 0 && ratio < 1 {
			state.PredictedUnderrun = time.Duration(bufferSeconds / (1 - ratio) * float64(time.Second))
		}
	}
	return state
}

func Classify(ratio float64) Classification {
	switch {
	case ratio <= 0 || math.IsNaN(ratio) || math.IsInf(ratio, 0):
		return Unknown
	case ratio >= HealthyThreshold:
		return Healthy
	case ratio >= SustainableThreshold:
		return Sustainable
	case ratio >= RiskyThreshold:
		return Risky
	default:
		return Unsustainable
	}
}

type Candidate struct {
	ID            string
	Local         bool
	Installed     bool
	SupportsPair  bool
	RealtimeRatio float64
}

type Selection struct {
	BackendID string
	Warning   string
}

// SelectAuto chooses the best installed local backend for the requested pair.
// Export ignores throughput. Playback keeps the best local option but warns
// when it cannot keep pace, leaving cloud consent to the later cloud slice.
func SelectAuto(candidates []Candidate, export bool) (Selection, error) {
	eligible := make([]Candidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.ID != "" && candidate.Local && candidate.Installed && candidate.SupportsPair {
			eligible = append(eligible, candidate)
		}
	}
	if len(eligible) == 0 {
		return Selection{}, errors.New("no installed local translator supports this language pair")
	}
	sort.SliceStable(eligible, func(i, j int) bool {
		return eligible[i].RealtimeRatio > eligible[j].RealtimeRatio
	})
	selected := eligible[0]
	selection := Selection{BackendID: selected.ID}
	if !export && selected.RealtimeRatio > 0 && selected.RealtimeRatio < RiskyThreshold {
		selection.Warning = "Local translation is slower than playback and may run out of ready audio. Continuing locally; use --export to finish without a playback deadline."
	} else if !export && selected.RealtimeRatio > 0 && selected.RealtimeRatio < SustainableThreshold {
		selection.Warning = "Local translation has limited playback headroom and may fall behind. Continuing locally."
	}
	return selection, nil
}

func realtimeRatio(wall, audio time.Duration) float64 {
	if wall <= 0 || audio <= 0 {
		return 0
	}
	return float64(audio) / float64(wall)
}

func ema(previous, value, alpha float64) float64 {
	if previous <= 0 {
		return value
	}
	return alpha*value + (1-alpha)*previous
}

func normalizeLocale(locale string) string {
	return strings.ToLower(strings.TrimSpace(locale))
}

func countWords(value string) int {
	words := 0
	inWord := false
	for _, r := range value {
		wordRune := unicode.IsLetter(r) || unicode.IsDigit(r) || r == '\'' || r == '’'
		if wordRune && !inWord {
			words++
		}
		inWord = wordRune
	}
	return words
}
