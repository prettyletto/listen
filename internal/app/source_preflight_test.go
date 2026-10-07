package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/prettyletto/listen/internal/apperr"
	"github.com/prettyletto/listen/internal/paths"
)

func TestSelectedSourcePreflightRejectsInvalidSourcesBeforeSpeechSetupCheck(t *testing.T) {
	userPaths := preflightTestPaths(t)
	readyChecks := 0
	deps := runtimeDeps{speechReady: func(context.Context, paths.Paths) (bool, error) {
		readyChecks++
		return false, nil
	}}
	missing := filepath.Join(t.TempDir(), "missing.txt")

	needsSetup, err := selectedSourceNeedsSetup(context.Background(), deps, userPaths, listenOptions{source: missing})
	if needsSetup || !apperr.HasCode(err, "source_missing") {
		t.Fatalf("missing source preflight = setup:%v err:%v, want source_missing before setup", needsSetup, err)
	}
	if readyChecks != 0 {
		t.Fatalf("speech setup readiness checked %d times for a missing source", readyChecks)
	}

	unsupported := filepath.Join(t.TempDir(), "broken.pdf")
	if err := os.WriteFile(unsupported, []byte("this is not a PDF"), 0o600); err != nil {
		t.Fatal(err)
	}
	needsSetup, err = selectedSourceNeedsSetup(context.Background(), deps, userPaths, listenOptions{source: unsupported})
	if needsSetup || !apperr.HasCode(err, "source_unsupported") {
		t.Fatalf("unsupported source preflight = setup:%v err:%v, want source_unsupported before setup", needsSetup, err)
	}
	if readyChecks != 0 {
		t.Fatalf("speech setup readiness checked %d times for unsupported input", readyChecks)
	}

	needsSetup, err = selectedSourceNeedsSetup(context.Background(), deps, userPaths, listenOptions{source: "-"})
	if needsSetup || !apperr.HasCode(err, "source_unavailable") {
		t.Fatalf("stdin home preflight = setup:%v err:%v, want source_unavailable", needsSetup, err)
	}
	if readyChecks != 0 {
		t.Fatalf("speech setup readiness checked %d times for stdin", readyChecks)
	}
}

func TestSelectedSourcePreflightRejectsEmptyAndUnsupportedFoldersBeforeSetupCheck(t *testing.T) {
	userPaths := preflightTestPaths(t)
	readyChecks := 0
	deps := runtimeDeps{speechReady: func(context.Context, paths.Paths) (bool, error) {
		readyChecks++
		return false, nil
	}}

	emptyFile := filepath.Join(t.TempDir(), "empty.txt")
	if err := os.WriteFile(emptyFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	emptyFolder := filepath.Join(t.TempDir(), "empty docs")
	if err := os.Mkdir(emptyFolder, 0o700); err != nil {
		t.Fatal(err)
	}
	unsupportedFolder := filepath.Join(t.TempDir(), "unsupported docs")
	if err := os.Mkdir(unsupportedFolder, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unsupportedFolder, "broken.pdf"), []byte("not a PDF"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name string
		path string
		code string
	}{
		{name: "empty file", path: emptyFile, code: "source_empty"},
		{name: "empty folder", path: emptyFolder, code: "source_unsupported"},
		{name: "unsupported folder", path: unsupportedFolder, code: "source_unsupported"},
	} {
		t.Run(test.name, func(t *testing.T) {
			needsSetup, err := selectedSourceNeedsSetup(context.Background(), deps, userPaths, listenOptions{source: test.path})
			if needsSetup || !apperr.HasCode(err, test.code) {
				t.Fatalf("%s preflight = setup:%v err:%v, want %s before setup", test.name, needsSetup, err, test.code)
			}
		})
	}
	if readyChecks != 0 {
		t.Fatalf("speech setup readiness checked %d times for empty or unsupported sources", readyChecks)
	}
}

func TestSelectedSourcePreflightReportsUnreadableSourcesBeforeSetupCheck(t *testing.T) {
	userPaths := preflightTestPaths(t)
	readyChecks := 0
	deps := runtimeDeps{speechReady: func(context.Context, paths.Paths) (bool, error) {
		readyChecks++
		return false, nil
	}}
	filePath := filepath.Join(t.TempDir(), "unreadable.txt")
	if err := os.WriteFile(filePath, []byte("cannot be read"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filePath, 0); err != nil {
		t.Skipf("file permission changes are unavailable: %v", err)
	}
	probe, err := os.Open(filePath)
	if err == nil {
		_ = probe.Close()
		_ = os.Chmod(filePath, 0o600)
		t.Skip("current user can read mode-000 files")
	}
	t.Cleanup(func() { _ = os.Chmod(filePath, 0o600) })

	needsSetup, err := selectedSourceNeedsSetup(context.Background(), deps, userPaths, listenOptions{source: filePath})
	if needsSetup || !apperr.HasCode(err, "source_unreadable") {
		t.Fatalf("unreadable file preflight = setup:%v err:%v, want source_unreadable before setup", needsSetup, err)
	}
	if readyChecks != 0 {
		t.Fatalf("speech setup readiness checked %d times for an unreadable file", readyChecks)
	}
}

func TestSelectedSourcePreflightReportsUnreadableFolderBeforeSetupCheck(t *testing.T) {
	userPaths := preflightTestPaths(t)
	readyChecks := 0
	deps := runtimeDeps{speechReady: func(context.Context, paths.Paths) (bool, error) {
		readyChecks++
		return false, nil
	}}
	folder := filepath.Join(t.TempDir(), "private docs")
	if err := os.Mkdir(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "README.md"), []byte("# Private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(folder, 0); err != nil {
		t.Skipf("directory permission changes are unavailable: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(folder, 0o700) })
	if _, err := os.ReadDir(folder); err == nil {
		t.Skip("current user can read a mode-000 directory")
	}

	needsSetup, err := selectedSourceNeedsSetup(context.Background(), deps, userPaths, listenOptions{source: folder})
	if needsSetup || !apperr.HasCode(err, "source_unreadable") {
		t.Fatalf("unreadable folder preflight = setup:%v err:%v, want source_unreadable before setup", needsSetup, err)
	}
	if readyChecks != 0 {
		t.Fatalf("speech setup readiness checked %d times for an unreadable folder", readyChecks)
	}
}

func TestSelectedSourcePreflightAllowsReadableTextAndDocumentationFolders(t *testing.T) {
	userPaths := preflightTestPaths(t)
	readyChecks := 0
	deps := runtimeDeps{speechReady: func(context.Context, paths.Paths) (bool, error) {
		readyChecks++
		return false, nil
	}}
	textPath := filepath.Join(t.TempDir(), "notes ü.txt")
	if err := os.WriteFile(textPath, []byte("A readable note for listening."), 0o600); err != nil {
		t.Fatal(err)
	}
	documentationRoot := filepath.Join(t.TempDir(), "docs")
	if err := os.MkdirAll(filepath.Join(documentationRoot, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(documentationRoot, "README.md"), []byte("# My notes\n\nA local document."), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(documentationRoot, ".git", "ignored.md"), []byte("ignored"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{textPath, documentationRoot} {
		needsSetup, err := selectedSourceNeedsSetup(context.Background(), deps, userPaths, listenOptions{source: path})
		if err != nil || !needsSetup {
			t.Errorf("readable source %q = setup:%v err:%v, want setup required", path, needsSetup, err)
		}
	}
	if readyChecks != 2 {
		t.Fatalf("speech setup readiness checked %d times, want once per valid source", readyChecks)
	}
}

func TestSelectedSourcePreflightResolvesDocumentationFolderSymlinks(t *testing.T) {
	root := t.TempDir()
	documentationRoot := filepath.Join(root, "docs")
	if err := os.Mkdir(documentationRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(documentationRoot, "guide.md"), []byte("# Guide\n\nReadable content."), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "linked docs")
	if err := os.Symlink(documentationRoot, link); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}

	if err := validateSourceBeforeSetup(context.Background(), listenOptions{source: link}); err != nil {
		t.Fatalf("preflight documentation folder symlink: %v", err)
	}
}

func preflightTestPaths(t *testing.T) paths.Paths {
	t.Helper()
	root := t.TempDir()
	return paths.Paths{
		ConfigDir: filepath.Join(root, "config"),
		DataDir:   filepath.Join(root, "data"),
		CacheDir:  filepath.Join(root, "cache"),
	}
}
