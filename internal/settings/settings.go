// Package settings owns listen's user-editable configuration.
package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/prettyletto/listen/internal/atomicfile"
	"github.com/prettyletto/listen/internal/speech/kokoro"
)

const ConfigVersion = 2
const previousConfigVersion = 1
const FileName = "config.json"

type Config struct {
	Version       int               `json:"config_version"`
	SetupComplete bool              `json:"setup_complete"`
	Speech        SpeechConfig      `json:"speech"`
	PlaybackRate  float64           `json:"playback_rate"`
	OCRLanguages  string            `json:"ocr_languages"`
	Voices        map[string]string `json:"voices"`
	CloudPolicy   string            `json:"cloud_policy"`
	CloudProvider string            `json:"cloud_provider"`
	CloudModel    string            `json:"cloud_model"`
}

type SpeechConfig struct {
	DefaultVoice string  `json:"default_voice"`
	DefaultSpeed float64 `json:"default_speed"`
}

func Defaults() Config {
	return Config{
		Version:       ConfigVersion,
		Speech:        SpeechConfig{DefaultVoice: "af_heart", DefaultSpeed: 1},
		PlaybackRate:  1,
		OCRLanguages:  "eng",
		Voices:        map[string]string{},
		CloudPolicy:   "ask",
		CloudProvider: "openai",
		CloudModel:    "gpt-5.6-luna",
	}
}

func Path(directory string) string { return filepath.Join(directory, FileName) }

func Load(directory string) (Config, bool, error) {
	configPath := Path(directory)
	data, err := os.ReadFile(configPath)
	if errors.Is(err, os.ErrNotExist) {
		return Defaults(), false, nil
	}
	if err != nil {
		return Config{}, false, fmt.Errorf("read config: %w", err)
	}
	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		return Config{}, false, fmt.Errorf("decode config: %w", err)
	}
	if config.Version != ConfigVersion && config.Version != previousConfigVersion {
		return Config{}, false, fmt.Errorf("unsupported config version %d", config.Version)
	}
	// Version 1 stored generation preferences but had no separate playback
	// rate. The zero value intentionally migrates to the familiar 1x rate.
	config.Version = ConfigVersion
	config = mergeDefaults(config)
	if err := Validate(config); err != nil {
		return Config{}, false, err
	}
	return config, true, nil
}

func Save(ctx context.Context, directory string, config Config) error {
	config = mergeDefaults(config)
	if err := Validate(config); err != nil {
		return err
	}
	if err := atomicfile.WriteJSON(ctx, Path(directory), config, 0o600); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	return nil
}

func Validate(config Config) error {
	if config.Version != ConfigVersion {
		return fmt.Errorf("unsupported config version %d", config.Version)
	}
	if _, ok := kokoro.FindVoice(config.Speech.DefaultVoice); !ok {
		return fmt.Errorf("unknown default voice %q", config.Speech.DefaultVoice)
	}
	if config.Speech.DefaultSpeed < 0.5 || config.Speech.DefaultSpeed > 2 || config.Speech.DefaultSpeed != config.Speech.DefaultSpeed {
		return errors.New("default speech speed must be between 0.5 and 2.0")
	}
	if config.PlaybackRate < 0.5 || config.PlaybackRate > 3 || config.PlaybackRate != config.PlaybackRate {
		return errors.New("default playback rate must be between 0.5 and 3.0")
	}
	if err := ValidateOCRLanguages(config.OCRLanguages); err != nil {
		return err
	}
	switch config.CloudPolicy {
	case "ask", "never", "allowed":
	default:
		return fmt.Errorf("cloud policy must be ask, never, or allowed")
	}
	if config.CloudProvider != "openai" {
		return errors.New("cloud provider must be openai")
	}
	if config.CloudModel == "" || len(config.CloudModel) > 128 || strings.ContainsAny(config.CloudModel, " \t\r\n") {
		return errors.New("cloud model must be a non-empty model ID without whitespace")
	}
	for locale, voiceID := range config.Voices {
		voice, ok := kokoro.FindVoice(voiceID)
		if !ok || voice.Locale != locale {
			return fmt.Errorf("voice %q is not available for locale %q", voiceID, locale)
		}
	}
	return nil
}

// ValidateOCRLanguages accepts Tesseract language IDs joined with '+', as
// accepted by Tesseract's -l option. Availability is checked when OCR runs.
func ValidateOCRLanguages(languages string) error {
	parts := strings.Split(languages, "+")
	if len(parts) == 0 || len(parts) > 8 {
		return errors.New("OCR languages must contain between one and eight Tesseract language IDs")
	}
	seen := make(map[string]bool, len(parts))
	for _, part := range parts {
		if part == "" || len(part) > 64 || seen[part] {
			return errors.New("OCR languages must be unique Tesseract IDs joined with '+', such as eng+por")
		}
		for _, char := range part {
			if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '_' {
				return errors.New("OCR languages must be Tesseract language IDs joined with '+', such as eng+por")
			}
		}
		seen[part] = true
	}
	return nil
}

func SetDefaultVoice(config *Config, voiceID string) error {
	voice, ok := kokoro.FindVoice(voiceID)
	if !ok {
		return fmt.Errorf("voice %q is not in the available voice list", voiceID)
	}
	config.Speech.DefaultVoice = voice.ID
	if config.Voices == nil {
		config.Voices = make(map[string]string)
	}
	config.Voices[voice.Locale] = voice.ID
	return nil
}

func SetLocaleVoice(config *Config, locale, voiceID string) error {
	voice, ok := kokoro.FindVoice(voiceID)
	if !ok || voice.Locale != locale {
		return fmt.Errorf("voice %q is not available for locale %q", voiceID, locale)
	}
	if config.Voices == nil {
		config.Voices = make(map[string]string)
	}
	config.Voices[locale] = voiceID
	return nil
}

func mergeDefaults(config Config) Config {
	defaults := Defaults()
	if config.Version == 0 {
		config.Version = defaults.Version
	}
	if config.Speech.DefaultVoice == "" {
		config.Speech.DefaultVoice = defaults.Speech.DefaultVoice
	}
	if config.Speech.DefaultSpeed == 0 {
		config.Speech.DefaultSpeed = defaults.Speech.DefaultSpeed
	}
	if config.PlaybackRate == 0 {
		config.PlaybackRate = defaults.PlaybackRate
	}
	if config.OCRLanguages == "" {
		config.OCRLanguages = defaults.OCRLanguages
	}
	if config.Voices == nil {
		config.Voices = make(map[string]string, len(defaults.Voices))
	}
	if config.CloudPolicy == "" {
		config.CloudPolicy = defaults.CloudPolicy
	}
	if config.CloudProvider == "" {
		config.CloudProvider = defaults.CloudProvider
	}
	if config.CloudModel == "" {
		config.CloudModel = defaults.CloudModel
	}
	return config
}
