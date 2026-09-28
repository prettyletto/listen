package source

import (
	"context"
	"errors"
	"testing"

	"github.com/prettyletto/listen/internal/domain"
)

type probeParser struct {
	name  string
	score int
	err   error
}

func (p probeParser) Name() string { return p.name }
func (p probeParser) Probe(context.Context, Input) (int, error) {
	return p.score, p.err
}
func (p probeParser) Parse(context.Context, Input) (*domain.Document, error) {
	return nil, nil
}

func TestSelectParserUsesHighestScoreAndStableTieOrder(t *testing.T) {
	first := probeParser{name: "first", score: 50}
	tie := probeParser{name: "tie", score: 50}
	best := probeParser{name: "best", score: 90}
	selected, err := SelectParser(context.Background(), Input{Name: "book"}, first, tie, best)
	if err != nil {
		t.Fatal(err)
	}
	if selected.Name() != "best" {
		t.Fatalf("selected %q, want highest-scoring parser", selected.Name())
	}

	selected, err = SelectParser(context.Background(), Input{Name: "book"}, first, tie)
	if err != nil {
		t.Fatal(err)
	}
	if selected.Name() != "first" {
		t.Fatalf("selected %q, want first parser for a tie", selected.Name())
	}
}

func TestSelectParserReturnsProbeErrorAndNoMatch(t *testing.T) {
	cause := errors.New("probe failed")
	if _, err := SelectParser(context.Background(), Input{}, probeParser{name: "broken", err: cause}); !errors.Is(err, cause) {
		t.Fatalf("got %v, want wrapped probe failure", err)
	}
	if _, err := SelectParser(context.Background(), Input{}, probeParser{name: "none"}); !errors.Is(err, ErrNoParser) {
		t.Fatalf("got %v, want ErrNoParser", err)
	}
}
