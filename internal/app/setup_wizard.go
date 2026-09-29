package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/prettyletto/listen/internal/settings"
	"github.com/prettyletto/listen/internal/speech/kokoro"
	"github.com/prettyletto/listen/internal/translate/argos"
)

type setupPrompter struct {
	ctx    context.Context
	reader *bufio.Reader
	writer io.Writer
}

func runSetupWizard(ctx context.Context, stdin io.Reader, stdout io.Writer, config settings.Config, translationPair string) (settings.Config, string, error) {
	prompter := setupPrompter{ctx: ctx, reader: bufio.NewReader(stdin), writer: stdout}
	locales := kokoro.Locales()
	defaults := setupDefaultLocales(config, locales)
	if _, err := fmt.Fprintln(stdout, "Let's set up your listening preferences."); err != nil {
		return settings.Config{}, "", err
	}
	if _, err := fmt.Fprintln(stdout, "Which languages do you listen in? Choose more than one with commas:"); err != nil {
		return settings.Config{}, "", err
	}
	for index, locale := range locales {
		if _, err := fmt.Fprintf(stdout, "  %d. %s (%s)\n", index+1, locale.LocaleName, locale.Locale); err != nil {
			return settings.Config{}, "", err
		}
	}
	languageAnswer, err := prompter.ask("Listening languages (numbers or locale codes)", strings.Join(defaults, ", "))
	if err != nil {
		return settings.Config{}, "", err
	}
	selectedLocales, err := parseSetupLocales(languageAnswer, defaults, locales)
	if err != nil {
		return settings.Config{}, "", err
	}

	if translationPair == "" {
		answer, err := prompter.ask("Install local English-to-Brazilian Portuguese translation? [y/N]", "no")
		if err != nil {
			return settings.Config{}, "", err
		}
		translationPair, err = parseSetupYesNo(answer)
		if err != nil {
			return settings.Config{}, "", err
		}
		if translationPair == "yes" {
			translationPair = argos.LanguagePair
		} else {
			translationPair = ""
		}
	}
	if translationPair != "" {
		_, targetLocale, _ := strings.Cut(translationPair, ":")
		if !containsSetupLocale(selectedLocales, targetLocale) {
			if voice, ok := kokoro.DefaultVoice(targetLocale); ok {
				if _, err := fmt.Fprintf(stdout, "\n%s will be added so translated speech has a voice.\n", voice.LocaleName); err != nil {
					return settings.Config{}, "", err
				}
			}
		}
		selectedLocales = appendSetupLocale(selectedLocales, targetLocale)
	}

	previousVoices := config.Voices
	selectedVoices := make(map[string]string, len(selectedLocales))
	for _, locale := range selectedLocales {
		voices := voicesForLocale(locale)
		if len(voices) == 0 {
			return settings.Config{}, "", fmt.Errorf("no local voices are available for %s", locale)
		}
		defaultVoice := previousVoices[locale]
		if !voiceMatchesLocale(defaultVoice, locale) {
			defaultVoice = voices[0].ID
		}
		if _, err := fmt.Fprintf(stdout, "\nChoose a default voice for %s (%s):\n", voices[0].LocaleName, locale); err != nil {
			return settings.Config{}, "", err
		}
		for index, voice := range voices {
			if _, err := fmt.Fprintf(stdout, "  %d. %s (%s)\n", index+1, voice.Name, voice.ID); err != nil {
				return settings.Config{}, "", err
			}
		}
		answer, err := prompter.ask("Voice (number or ID)", defaultVoice)
		if err != nil {
			return settings.Config{}, "", err
		}
		voice, err := parseSetupVoice(answer, locale, defaultVoice, voices)
		if err != nil {
			return settings.Config{}, "", err
		}
		selectedVoices[locale] = voice.ID
	}

	defaultVoice, ok := kokoro.FindVoice(config.Speech.DefaultVoice)
	if !ok || !containsSetupLocale(selectedLocales, defaultVoice.Locale) {
		defaultVoice, _ = kokoro.FindVoice(selectedVoices[selectedLocales[0]])
	} else {
		defaultVoice, _ = kokoro.FindVoice(selectedVoices[defaultVoice.Locale])
	}
	config.Voices = selectedVoices
	if err := settings.SetDefaultVoice(&config, defaultVoice.ID); err != nil {
		return settings.Config{}, "", err
	}
	if _, err := fmt.Fprintf(stdout, "\nCloud fallback stays %q. Change it with `listen config set cloud-policy <ask|never|allowed>`.\n", config.CloudPolicy); err != nil {
		return settings.Config{}, "", err
	}
	return config, translationPair, nil
}

func (prompter setupPrompter) ask(label, defaultValue string) (string, error) {
	if prompter.reader == nil {
		return defaultValue, nil
	}
	if _, err := fmt.Fprintf(prompter.writer, "%s [%s]: ", label, defaultValue); err != nil {
		return "", err
	}
	type result struct {
		line string
		err  error
	}
	resultCh := make(chan result, 1)
	go func() {
		line, err := prompter.reader.ReadString('\n')
		resultCh <- result{line: line, err: err}
	}()
	select {
	case <-prompter.ctx.Done():
		return "", prompter.ctx.Err()
	case answer := <-resultCh:
		if answer.err != nil && !errors.Is(answer.err, io.EOF) {
			return "", answer.err
		}
		value := strings.TrimSpace(answer.line)
		if value == "" {
			return defaultValue, nil
		}
		return value, nil
	}
}

func setupDefaultLocales(config settings.Config, available []kokoro.Voice) []string {
	selected := make([]string, 0, len(config.Voices)+1)
	for _, locale := range available {
		if _, ok := config.Voices[locale.Locale]; ok {
			selected = append(selected, locale.Locale)
		}
	}
	if defaultVoice, ok := kokoro.FindVoice(config.Speech.DefaultVoice); ok && !containsSetupLocale(selected, defaultVoice.Locale) {
		selected = append([]string{defaultVoice.Locale}, selected...)
	}
	if len(selected) == 0 && len(available) > 0 {
		selected = append(selected, available[0].Locale)
	}
	return selected
}

func parseSetupLocales(answer string, defaults []string, available []kokoro.Voice) ([]string, error) {
	if strings.TrimSpace(answer) == "" {
		return append([]string(nil), defaults...), nil
	}
	choices := make([]string, 0)
	for _, raw := range strings.Split(answer, ",") {
		choice := strings.TrimSpace(raw)
		if choice == "" {
			continue
		}
		locale := ""
		if index, err := strconv.Atoi(choice); err == nil {
			if index < 1 || index > len(available) {
				return nil, fmt.Errorf("language choice %q is outside the listed options", choice)
			}
			locale = available[index-1].Locale
		} else {
			for _, candidate := range available {
				if strings.EqualFold(candidate.Locale, choice) {
					locale = candidate.Locale
					break
				}
			}
			if locale == "" {
				return nil, fmt.Errorf("%q is not a listed listening language", choice)
			}
		}
		if !containsSetupLocale(choices, locale) {
			choices = append(choices, locale)
		}
	}
	if len(choices) == 0 {
		return nil, errors.New("choose at least one listening language")
	}
	return choices, nil
}

func parseSetupYesNo(answer string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return "yes", nil
	case "", "n", "no":
		return "no", nil
	default:
		return "", errors.New("answer yes or no")
	}
}

func parseSetupVoice(answer, locale, defaultVoice string, voices []kokoro.Voice) (kokoro.Voice, error) {
	choice := strings.TrimSpace(answer)
	if choice == "" {
		choice = defaultVoice
	}
	if index, err := strconv.Atoi(choice); err == nil {
		if index < 1 || index > len(voices) {
			return kokoro.Voice{}, fmt.Errorf("voice choice %q is outside the listed options for %s", choice, locale)
		}
		return voices[index-1], nil
	}
	voice, ok := kokoro.FindVoice(choice)
	if !ok || voice.Locale != locale {
		return kokoro.Voice{}, fmt.Errorf("voice %q is not available for %s", choice, locale)
	}
	return voice, nil
}

func voicesForLocale(locale string) []kokoro.Voice {
	voices := make([]kokoro.Voice, 0)
	for _, voice := range kokoro.Voices() {
		if voice.Locale == locale {
			voices = append(voices, voice)
		}
	}
	return voices
}

func voiceMatchesLocale(voiceID, locale string) bool {
	voice, ok := kokoro.FindVoice(voiceID)
	return ok && voice.Locale == locale
}

func appendSetupLocale(locales []string, locale string) []string {
	if containsSetupLocale(locales, locale) {
		return locales
	}
	return append(locales, locale)
}

func containsSetupLocale(locales []string, wanted string) bool {
	for _, locale := range locales {
		if locale == wanted {
			return true
		}
	}
	return false
}

func ensureTranslationVoice(config *settings.Config, pair string) error {
	_, targetLocale, ok := strings.Cut(pair, ":")
	if !ok || targetLocale == "" {
		return fmt.Errorf("invalid translation pair %q", pair)
	}
	voiceID := config.Voices[targetLocale]
	if !voiceMatchesLocale(voiceID, targetLocale) {
		voice, ok := kokoro.DefaultVoice(targetLocale)
		if !ok {
			return fmt.Errorf("no local speech voice is available for %s", targetLocale)
		}
		voiceID = voice.ID
	}
	return settings.SetLocaleVoice(config, targetLocale, voiceID)
}
