package kokoro

// Voice is a user-facing voice example grouped under its listening locale.
type Voice struct {
	ID           string
	Name         string
	Locale       string
	LocaleName   string
	LanguageCode string
}

var availableVoices = []Voice{
	{ID: "af_heart", Name: "Heart", Locale: "en-US", LocaleName: "American English", LanguageCode: "a"},
	{ID: "af_bella", Name: "Bella", Locale: "en-US", LocaleName: "American English", LanguageCode: "a"},
	{ID: "af_sarah", Name: "Sarah", Locale: "en-US", LocaleName: "American English", LanguageCode: "a"},
	{ID: "af_nicole", Name: "Nicole", Locale: "en-US", LocaleName: "American English", LanguageCode: "a"},
	{ID: "af_sky", Name: "Sky", Locale: "en-US", LocaleName: "American English", LanguageCode: "a"},
	{ID: "am_adam", Name: "Adam", Locale: "en-US", LocaleName: "American English", LanguageCode: "a"},
	{ID: "am_michael", Name: "Michael", Locale: "en-US", LocaleName: "American English", LanguageCode: "a"},
	{ID: "bf_emma", Name: "Emma", Locale: "en-GB", LocaleName: "British English", LanguageCode: "b"},
	{ID: "bf_isabella", Name: "Isabella", Locale: "en-GB", LocaleName: "British English", LanguageCode: "b"},
	{ID: "bm_george", Name: "George", Locale: "en-GB", LocaleName: "British English", LanguageCode: "b"},
	{ID: "bm_lewis", Name: "Lewis", Locale: "en-GB", LocaleName: "British English", LanguageCode: "b"},
	{ID: "ef_dora", Name: "Dora", Locale: "es-ES", LocaleName: "Spanish", LanguageCode: "e"},
	{ID: "em_alex", Name: "Alex", Locale: "es-ES", LocaleName: "Spanish", LanguageCode: "e"},
	{ID: "em_santa", Name: "Santa", Locale: "es-ES", LocaleName: "Spanish", LanguageCode: "e"},
	{ID: "ff_siwis", Name: "Siwis", Locale: "fr-FR", LocaleName: "French", LanguageCode: "f"},
	{ID: "hf_alpha", Name: "Alpha", Locale: "hi-IN", LocaleName: "Hindi", LanguageCode: "h"},
	{ID: "hf_beta", Name: "Beta", Locale: "hi-IN", LocaleName: "Hindi", LanguageCode: "h"},
	{ID: "hm_omega", Name: "Omega", Locale: "hi-IN", LocaleName: "Hindi", LanguageCode: "h"},
	{ID: "hm_psi", Name: "Psi", Locale: "hi-IN", LocaleName: "Hindi", LanguageCode: "h"},
	{ID: "if_sara", Name: "Sara", Locale: "it-IT", LocaleName: "Italian", LanguageCode: "i"},
	{ID: "im_nicola", Name: "Nicola", Locale: "it-IT", LocaleName: "Italian", LanguageCode: "i"},
	{ID: "pf_dora", Name: "Dora", Locale: "pt-BR", LocaleName: "Brazilian Portuguese", LanguageCode: "p"},
	{ID: "pm_alex", Name: "Alex", Locale: "pt-BR", LocaleName: "Brazilian Portuguese", LanguageCode: "p"},
	{ID: "pm_santa", Name: "Santa", Locale: "pt-BR", LocaleName: "Brazilian Portuguese", LanguageCode: "p"},
}

func Voices() []Voice { return append([]Voice(nil), availableVoices...) }

func Locales() []Voice {
	var locales []Voice
	seen := make(map[string]struct{})
	for _, voice := range availableVoices {
		if _, ok := seen[voice.Locale]; ok {
			continue
		}
		seen[voice.Locale] = struct{}{}
		locales = append(locales, voice)
	}
	return locales
}

func FindVoice(id string) (Voice, bool) {
	for _, voice := range availableVoices {
		if voice.ID == id {
			return voice, true
		}
	}
	return Voice{}, false
}

func DefaultVoice(locale string) (Voice, bool) {
	for _, voice := range availableVoices {
		if voice.Locale == locale {
			return voice, true
		}
	}
	return Voice{}, false
}
