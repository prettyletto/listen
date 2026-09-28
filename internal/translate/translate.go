package translate

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/language"

	"github.com/prettyletto/listen/internal/segment"
)

type Request struct {
	TargetLanguage   string
	Segments         []segment.Segment
	Glossary         map[string]string
	ContextHint      string
	StyleInstruction string
	PromptVersion    string
}

type Result struct {
	SegmentID string
	Text      string
}

type Capabilities struct {
	Name          string
	Local         bool
	Remote        bool
	LanguagePairs map[string]bool
	SupportsBatch bool
}

// SupportsLanguagePair reports whether a translator declares the normalized
// source/target pair. Empty capability maps are treated as unrestricted for
// test and user-provided local adapters that predate pair declarations.
func (capabilities Capabilities) SupportsLanguagePair(source, target string) bool {
	if len(capabilities.LanguagePairs) == 0 {
		return true
	}
	pair, err := LanguagePair(source, target)
	if err != nil {
		return false
	}
	return capabilities.LanguagePairs[pair]
}

// LanguagePair canonicalizes a BCP-47 source tag to its language subtag and
// retains the full target tag so regional target variants stay distinct.
func LanguagePair(source, target string) (string, error) {
	source = strings.TrimSpace(source)
	targetTag, err := NormalizeLanguageTag(target)
	if err != nil {
		return "", err
	}
	if source == "" {
		return "", errors.New("source language is not declared")
	}
	sourceTag, err := language.Parse(source)
	if err != nil {
		return "", err
	}
	base, _ := sourceTag.Base()
	return base.String() + ":" + targetTag, nil
}

type Translator interface {
	ID() string
	Capabilities(context.Context) (Capabilities, error)
	Translate(context.Context, Request) ([]Result, error)
}

type cloudAuthorizationContextKey struct{}

// contextWithCloudAuthorization carries the permission proof created by the
// authorization wrapper into the provider adapter call. Keeping creation
// private prevents callers from forging consent for a raw provider adapter.
func contextWithCloudAuthorization(ctx context.Context) context.Context {
	if ctx == nil {
		return nil
	}
	return context.WithValue(ctx, cloudAuthorizationContextKey{}, true)
}

// HasCloudAuthorization reports whether a provider call was reached through a
// cloud authorization wrapper.
func HasCloudAuthorization(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	allowed, _ := ctx.Value(cloudAuthorizationContextKey{}).(bool)
	return allowed
}

type authorizedCloudTranslator struct {
	inner   Translator
	policy  string
	consent bool
}

// AuthorizeCloud creates a pipeline-eligible wrapper after an explicit
// `allowed` policy or a positive `ask` confirmation. The underlying adapter
// remains unusable through the normal translation pipeline by itself.
func AuthorizeCloud(inner Translator, policy string, consent bool) (Translator, error) {
	if inner == nil {
		return nil, errors.New("cloud translation requires a provider")
	}
	if policy != "allowed" && policy != "ask" {
		return nil, errors.New("cloud translation policy does not authorize remote processing")
	}
	if policy == "ask" && !consent {
		return nil, errors.New("cloud translation requires confirmation")
	}
	return &authorizedCloudTranslator{inner: inner, policy: policy, consent: consent}, nil
}

// IsCloudAuthorized reports whether translator is the authorization wrapper
// created by AuthorizeCloud. The concrete-type check prevents a remote adapter
// from satisfying the pipeline gate by implementing an exported marker method.
func IsCloudAuthorized(translator Translator) bool {
	authorized, ok := translator.(*authorizedCloudTranslator)
	return ok && authorized.CloudTranslationAuthorized()
}

func (translator *authorizedCloudTranslator) ID() string {
	return translator.inner.ID()
}

func (translator *authorizedCloudTranslator) Capabilities(ctx context.Context) (Capabilities, error) {
	return translator.inner.Capabilities(ctx)
}

func (translator *authorizedCloudTranslator) Translate(ctx context.Context, request Request) ([]Result, error) {
	if ctx == nil {
		return nil, errors.New("cloud translation requires a context")
	}
	if !translator.CloudTranslationAuthorized() {
		return nil, errors.New("cloud translation is not authorized")
	}
	return translator.inner.Translate(contextWithCloudAuthorization(ctx), request)
}

func (translator *authorizedCloudTranslator) Close() error {
	if closer, ok := translator.inner.(interface{ Close() error }); ok {
		return closer.Close()
	}
	return nil
}

func (translator *authorizedCloudTranslator) CloudTranslationAuthorized() bool {
	return translator != nil && translator.inner != nil && (translator.policy == "allowed" || (translator.policy == "ask" && translator.consent))
}

type Sample struct {
	WallTime             time.Duration
	ProducedAudioSeconds float64
	Segments             int
}

const MaxTranslationBytes = 1 << 20

// NormalizeTranslationText validates a complete translation before it can be
// committed to a segment or written to the cache.
func NormalizeTranslationText(value string) (string, error) {
	if len(value) > MaxTranslationBytes {
		return "", errors.New("translation text exceeds the maximum size")
	}
	if !utf8.ValidString(value) {
		return "", errors.New("translation text is not valid UTF-8")
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("translation text is empty")
	}
	return value, nil
}

func (s Sample) RealtimeRatio() float64 {
	if s.WallTime <= 0 {
		return 0
	}
	return s.ProducedAudioSeconds / s.WallTime.Seconds()
}
