package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestCatalogUpsertUsesCleanAbsolutePathAndPersistsEntry(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	sourcePath := filepath.Join(root, "folder", "..", "book ü.epub")
	openedAt := time.Date(2026, time.September, 30, 9, 15, 0, 0, time.FixedZone("test", -3*60*60))

	catalog := New(dataDir)
	entry, err := catalog.Upsert(context.Background(), Entry{
		Path: sourcePath, Title: "A book", TargetLanguage: "pt-BR", CodeMode: true,
		ResumeLabel: "Chapter 2", LastOpenedAt: openedAt,
	})
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	wantPath, err := filepath.Abs(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Path != wantPath || entry.ID() != wantPath {
		t.Fatalf("canonical path/id = %q / %q, want %q", entry.Path, entry.ID(), wantPath)
	}
	if entry.LastOpenedAt.Location() != time.UTC || !entry.LastOpenedAt.Equal(openedAt) {
		t.Fatalf("last opened = %v, want %v in UTC", entry.LastOpenedAt, openedAt)
	}
	if entry.UpdatedAt.IsZero() {
		t.Fatal("Upsert() left UpdatedAt empty")
	}

	got, err := New(dataDir).List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(got) != 1 || got[0] != entry {
		t.Fatalf("List() = %#v, want %#v", got, []Entry{entry})
	}
	info, err := os.Stat(filepath.Join(dataDir, FileName))
	if err != nil {
		t.Fatalf("stat catalog: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("catalog permissions = %04o, want 0600", mode)
	}
}

func TestCatalogUpsertUpdatesDuplicatePathAndListSortsByLastOpened(t *testing.T) {
	dataDir := t.TempDir()
	root := t.TempDir()
	firstPath := filepath.Join(root, "one.txt")
	secondPath := filepath.Join(root, "two.txt")
	firstTime := time.Now().Add(-time.Hour).UTC()
	secondTime := time.Now().UTC()
	catalog := New(dataDir)

	if _, err := catalog.Upsert(context.Background(), Entry{Path: firstPath, Title: "Old", LastOpenedAt: firstTime}); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.Upsert(context.Background(), Entry{Path: secondPath, Title: "Second", LastOpenedAt: secondTime}); err != nil {
		t.Fatal(err)
	}
	updated, err := catalog.Upsert(context.Background(), Entry{
		Path: filepath.Join(root, "folder", "..", "one.txt"), Title: "Updated",
		TargetLanguage: "es", CodeMode: true, ResumeLabel: "Page 4", Complete: true,
	})
	if err != nil {
		t.Fatalf("duplicate Upsert() error = %v", err)
	}
	if updated.Path != firstPath || updated.TargetLanguage != "es" || !updated.CodeMode || !updated.Complete {
		t.Fatalf("duplicate Upsert() result = %#v", updated)
	}
	entries, err := catalog.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(entries) != 2 || entries[0].Path != firstPath || entries[0].Title != "Updated" || entries[1].Path != secondPath {
		t.Fatalf("List() = %#v, want updated first entry followed by second", entries)
	}
}

func TestCatalogPathIdentityPreservesSymlinks(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	actual := filepath.Join(root, "book.txt")
	alias := filepath.Join(root, "alias.txt")
	if err := os.WriteFile(actual, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(actual, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	catalog := New(dataDir)
	for _, path := range []string{actual, alias} {
		if _, err := catalog.Upsert(context.Background(), Entry{Path: path}); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := catalog.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("symlink and target collapsed to %d entries, want 2", len(entries))
	}
}

func TestCatalogRemoveReturnsEntryForUndoAndLeavesSourceAndCache(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	sourcePath := filepath.Join(root, "a book.txt")
	cacheMarker := filepath.Join(root, "cache", "session.json")
	if err := os.WriteFile(sourcePath, []byte("book"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(cacheMarker), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cacheMarker, []byte("checkpoint"), 0o600); err != nil {
		t.Fatal(err)
	}
	lastOpened := time.Now().Add(-time.Hour).UTC()
	catalog := New(dataDir)
	original, err := catalog.Upsert(context.Background(), Entry{
		Path: sourcePath, Title: "Book", TargetLanguage: "pt-BR", ResumeLabel: "Chapter 3",
		LastOpenedAt: lastOpened, Complete: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	removed, found, err := catalog.Remove(context.Background(), sourcePath)
	if err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if !found || removed.Path != original.Path || removed.ResumeLabel != original.ResumeLabel || !removed.Complete {
		t.Fatalf("Remove() = %#v, %t, want the complete removed entry", removed, found)
	}
	if _, err := os.Stat(sourcePath); err != nil {
		t.Fatalf("Remove() changed source file: %v", err)
	}
	if _, err := os.Stat(cacheMarker); err != nil {
		t.Fatalf("Remove() changed cached data: %v", err)
	}
	entries, err := catalog.List(context.Background())
	if err != nil || len(entries) != 0 {
		t.Fatalf("List() after Remove() = %#v, %v; want empty", entries, err)
	}

	if _, err := catalog.Upsert(context.Background(), removed); err != nil {
		t.Fatalf("Upsert(removed) undo error = %v", err)
	}
	entries, err = catalog.List(context.Background())
	if err != nil || len(entries) != 1 || entries[0].ResumeLabel != "Chapter 3" || !entries[0].Complete || !entries[0].LastOpenedAt.Equal(lastOpened) {
		t.Fatalf("List() after undo = %#v, %v", entries, err)
	}

	if _, found, err := catalog.Remove(context.Background(), filepath.Join(root, "missing.txt")); err != nil || found {
		t.Fatalf("Remove(missing) = found %t, err %v; want not found", found, err)
	}
}

func TestCatalogRejectsUnknownVersionAndDuplicateStoredPaths(t *testing.T) {
	dataDir := t.TempDir()
	catalog := New(dataDir)
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dataDir, FileName)
	if err := os.WriteFile(file, []byte(`{"catalog_version":99,"entries":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.List(context.Background()); err == nil || !strings.Contains(err.Error(), "unsupported catalog version") {
		t.Fatalf("List() error = %v, want unsupported-version error", err)
	}

	canonical := filepath.Join(t.TempDir(), "book.txt")
	data, err := jsonCatalog(canonical, canonical)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.List(context.Background()); err == nil || !strings.Contains(err.Error(), "duplicate source path") {
		t.Fatalf("List() error = %v, want duplicate-path error", err)
	}
}

func TestCatalogLockWaitHonorsCancellation(t *testing.T) {
	dataDir := t.TempDir()
	catalog := New(dataDir)
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	held, err := acquireLock(context.Background(), catalog.path+".lock")
	if err != nil {
		t.Fatal(err)
	}
	defer held.close()

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := catalog.Upsert(ctx, Entry{Path: filepath.Join(dataDir, "book.txt")})
		result <- err
	}()
	time.Sleep(40 * time.Millisecond)
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Upsert() error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Upsert() did not stop waiting for the catalog lock after cancellation")
	}
}

func TestCatalogConcurrentProcessWriters(t *testing.T) {
	const helperDirKey = "LISTEN_INTERNAL_CATALOG_HELPER_DIR"
	if dataDir := os.Getenv(helperDirKey); dataDir != "" {
		index, err := strconv.Atoi(os.Getenv("LISTEN_INTERNAL_CATALOG_HELPER_INDEX"))
		if err != nil {
			t.Fatal(err)
		}
		_, err = New(dataDir).Upsert(context.Background(), Entry{
			Path:  filepath.Join(os.Getenv("LISTEN_INTERNAL_CATALOG_SOURCE_DIR"), fmt.Sprintf("source-%02d.txt", index)),
			Title: fmt.Sprintf("Source %d", index),
		})
		if err != nil {
			t.Fatal(err)
		}
		return
	}

	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	sourceDir := filepath.Join(root, "sources")
	if err := os.MkdirAll(sourceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	const writers = 8
	commands := make([]*exec.Cmd, 0, writers)
	outputs := make([]*strings.Builder, 0, writers)
	for index := 0; index < writers; index++ {
		command := exec.Command(os.Args[0], "-test.run=^TestCatalogConcurrentProcessWriters$")
		command.Env = append(os.Environ(),
			helperDirKey+"="+dataDir,
			"LISTEN_INTERNAL_CATALOG_SOURCE_DIR="+sourceDir,
			"LISTEN_INTERNAL_CATALOG_HELPER_INDEX="+strconv.Itoa(index),
		)
		output := &strings.Builder{}
		command.Stdout, command.Stderr = output, output
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		commands = append(commands, command)
		outputs = append(outputs, output)
	}
	for index, command := range commands {
		if err := command.Wait(); err != nil {
			t.Errorf("writer %d failed: %v\n%s", index, err, outputs[index].String())
		}
	}
	entries, err := New(dataDir).List(context.Background())
	if err != nil {
		t.Fatalf("List() after process writers: %v", err)
	}
	if len(entries) != writers {
		t.Fatalf("got %d entries after concurrent process writes, want %d: %#v", len(entries), writers, entries)
	}
}

func jsonCatalog(paths ...string) ([]byte, error) {
	entries := make([]Entry, 0, len(paths))
	for _, path := range paths {
		entries = append(entries, Entry{Path: path})
	}
	return json.Marshal(diskCatalog{Version: catalogVersion, Entries: entries})
}
