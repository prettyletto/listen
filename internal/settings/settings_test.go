package settings

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestSettingsSaveLoadAndPreserveLocaleVoices(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "config")
	config := Defaults()
	if err := SetDefaultVoice(&config, "pf_dora"); err != nil {
		t.Fatal(err)
	}
	if err := SetLocaleVoice(&config, "en-GB", "bf_emma"); err != nil {
		t.Fatal(err)
	}
	config.SetupComplete = true
	config.CloudPolicy = "never"
	config.Speech.DefaultSpeed = 1.2
	config.OCRLanguages = "eng+por"
	if err := Save(context.Background(), directory, config); err != nil {
		t.Fatal(err)
	}
	got, found, err := Load(directory)
	if err != nil || !found {
		t.Fatalf("load config found=%v err=%v", found, err)
	}
	if got.Speech.DefaultVoice != "pf_dora" || got.Speech.DefaultSpeed != 1.2 || got.Voices["en-GB"] != "bf_emma" || !got.SetupComplete || got.CloudPolicy != "never" || got.OCRLanguages != "eng+por" {
		t.Fatalf("loaded config = %#v", got)
	}
	info, err := os.Stat(Path(directory))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config permissions = %o, want 600", info.Mode().Perm())
	}
}

func TestSettingsDefaultsAndRejectsInvalidState(t *testing.T) {
	config, found, err := Load(t.TempDir())
	if err != nil || found {
		t.Fatalf("missing config found=%v err=%v", found, err)
	}
	if config.Speech.DefaultVoice != "af_heart" || config.CloudPolicy != "ask" || config.CloudProvider != "openai" || config.CloudModel != "gpt-5.6-luna" || config.OCRLanguages != "eng" {
		t.Fatalf("default config = %#v", config)
	}
	if err := SetLocaleVoice(&config, "en-US", "pf_dora"); err == nil {
		t.Fatal("accepted a voice under the wrong locale")
	}
	config.CloudPolicy = "silent"
	if err := Save(context.Background(), t.TempDir(), config); err == nil {
		t.Fatal("saved an unknown cloud policy")
	}
	config = Defaults()
	config.CloudProvider = "unknown"
	if err := Save(context.Background(), t.TempDir(), config); err == nil {
		t.Fatal("saved an unknown cloud provider")
	}
	config = Defaults()
	config.CloudModel = "model with spaces"
	if err := Save(context.Background(), t.TempDir(), config); err == nil {
		t.Fatal("saved an invalid cloud model ID")
	}
}

func TestOCRLanguagePreferenceValidation(t *testing.T) {
	for _, languages := range []string{"eng", "eng+por", "chi_sim+chi_tra"} {
		if err := ValidateOCRLanguages(languages); err != nil {
			t.Errorf("ValidateOCRLanguages(%q): %v", languages, err)
		}
	}
	for _, languages := range []string{"", "eng+", "+eng", "eng+eng", "eng por", "../../eng", "a+b+c+d+e+f+g+h+i"} {
		if err := ValidateOCRLanguages(languages); err == nil {
			t.Errorf("ValidateOCRLanguages(%q) accepted an invalid language list", languages)
		}
	}
}

func TestSettingsSaveHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	directory := t.TempDir()
	if err := Save(ctx, directory, Defaults()); err == nil {
		t.Fatal("saved settings with canceled context")
	}
	if _, err := os.Stat(Path(directory)); !os.IsNotExist(err) {
		t.Fatalf("config exists after canceled save: %v", err)
	}
}
