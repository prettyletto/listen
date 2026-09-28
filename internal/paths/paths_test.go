package paths

import (
	"path/filepath"
	"testing"
)

func TestForHomeUsesXDGValuesAndDefaults(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		got, err := ForHome("/home/reader", func(string) string { return "" })
		if err != nil {
			t.Fatal(err)
		}
		want := Paths{
			CacheDir:  "/home/reader/.cache/listen",
			DataDir:   "/home/reader/.local/share/listen",
			ConfigDir: "/home/reader/.config/listen",
		}
		if got != want {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	})

	t.Run("absolute XDG paths", func(t *testing.T) {
		values := map[string]string{
			"XDG_CACHE_HOME":  "/state/cache",
			"XDG_DATA_HOME":   "/state/data",
			"XDG_CONFIG_HOME": "/state/config",
		}
		got, err := ForHome("/home/reader", func(key string) string { return values[key] })
		if err != nil {
			t.Fatal(err)
		}
		want := Paths{
			CacheDir:  filepath.Join("/state/cache", "listen"),
			DataDir:   filepath.Join("/state/data", "listen"),
			ConfigDir: filepath.Join("/state/config", "listen"),
		}
		if got != want {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	})

	t.Run("relative XDG paths are ignored", func(t *testing.T) {
		got, err := ForHome("/home/reader", func(string) string { return "relative/path" })
		if err != nil {
			t.Fatal(err)
		}
		if got.CacheDir != filepath.Join("/home/reader", ".cache", "listen") {
			t.Fatalf("relative cache path was accepted: %q", got.CacheDir)
		}
	})
}

func TestForHomeRequiresAbsoluteHome(t *testing.T) {
	if _, err := ForHome("reader", nil); err == nil {
		t.Fatal("expected an error for a relative home")
	}
}
