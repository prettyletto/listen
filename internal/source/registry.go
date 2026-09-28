package source

import (
	"context"
	"errors"
	"fmt"
)

var ErrNoParser = errors.New("no source parser recognized the input")

// SelectParser chooses the parser that most confidently recognizes an input.
// Ties keep the caller's order, which lets applications provide a stable
// preference when multiple parsers can handle the same source.
func SelectParser(ctx context.Context, input Input, parsers ...Parser) (Parser, error) {
	if ctx == nil {
		return nil, errors.New("source selection requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var selected Parser
	bestScore := 0
	for _, parser := range parsers {
		if parser == nil {
			continue
		}
		score, err := parser.Probe(ctx, input)
		if err != nil {
			return nil, fmt.Errorf("probe %s parser: %w", parser.Name(), err)
		}
		if score > bestScore {
			selected = parser
			bestScore = score
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if selected == nil {
		return nil, ErrNoParser
	}
	return selected, nil
}
