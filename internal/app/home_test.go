package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prettyletto/listen/internal/catalog"
	"github.com/prettyletto/listen/internal/session"
)

func TestReadDirectoryHidesDotfilesThenShowsThemOnRequest(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "notes folder"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"book ü.epub", ".private.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("text"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	gotPath, entries, note := readDirectory(root, false)
	if note != "" || gotPath != root {
		t.Fatalf("readDirectory() = path %q, note %q", gotPath, note)
	}
	if len(entries) != 2 || entries[0].Name != "notes folder" || !entries[0].Directory || entries[1].Name != "book ü.epub" {
		t.Fatalf("visible entries = %#v, want folder then Unicode file", entries)
	}
	_, entries, note = readDirectory(root, true)
	if note != "" || len(entries) != 3 || entries[1].Name != ".private.txt" {
		t.Fatalf("hidden entries = %#v, note %q", entries, note)
	}
}

func TestReadDirectoryReportsPermissionOrMissingRecoveryWithoutLosingCurrentFolder(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-here")
	path, entries, note := readDirectory(missing, false)
	if path != missing || len(entries) != 0 || note == "" {
		t.Fatalf("missing directory result = %q, %#v, %q", path, entries, note)
	}
}

func TestHomeEntriesDistinguishMissingSourcesFromExistingSources(t *testing.T) {
	root := t.TempDir()
	existing := filepath.Join(root, "book.txt")
	if err := os.WriteFile(existing, []byte("book"), 0o600); err != nil {
		t.Fatal(err)
	}
	items := homeEntries([]catalog.Entry{
		{Path: existing, Title: "Present"},
		{Path: filepath.Join(root, "moved.txt"), Title: "Moved"},
	})
	if len(items) != 2 || items[0].Missing || !items[1].Missing {
		t.Fatalf("source availability = %#v, want only the vanished source marked missing", items)
	}
}

func TestHomeSourceTitleStripsOnlyRecognizedArchiveMetadata(t *testing.T) {
	const archive = "Fundamentals of Software Architecture{Mark Richards, Neal Ford}(2025, O&_039_Reilly Media){107968149} libgen.li.epub"
	want := "Fundamentals of Software Architecture"
	path := filepath.Join("/books", archive)
	if got := sourceTitle(path); got != want {
		t.Fatalf("sourceTitle() = %q, want %q", got, want)
	}
	if got := homeSourceTitle(strings.TrimSuffix(archive, ".epub"), path); got != want {
		t.Fatalf("legacy catalog title = %q, want %q", got, want)
	}

	for _, title := range []string{
		"A title (revised edition)",
		"A book {with braces} (2025)",
		"Notes{not-author-metadata}(2025, edition){id}.epub",
	} {
		if got := cleanArchiveTitle(title); got != title {
			t.Errorf("cleanArchiveTitle(%q) = %q; ordinary filename should remain unchanged", title, got)
		}
	}
	const metadataTitle = "A Different Title"
	if got := homeSourceTitle(metadataTitle, path); got != metadataTitle {
		t.Fatalf("parser-supplied title changed from %q to %q", metadataTitle, got)
	}

	// This EPUB's dc:title is "Fundamentals of Software Architecture (for
	// Raymond Rhine)". Older checkpoints recorded that document title as the
	// position, so keep it out of the chapter/page slot in the library.
	items := homeEntries([]catalog.Entry{{
		Path: path, Title: strings.TrimSuffix(archive, ".epub"),
		ResumeLabel: "Fundamentals of Software Architecture (for Raymond Rhine)",
	}})
	if len(items) != 1 || items[0].Title != want || items[0].ResumeLabel != "Saved place" {
		t.Fatalf("legacy archive home entry = %#v, want title %q and generic saved-place label", items, want)
	}
	if !bookLevelCheckpoint("Fundamentals of Software Architecture (for Raymond Rhine)", want) {
		t.Fatal("reader-attributed document title should use the generic location copy")
	}
	if bookLevelCheckpoint("Fundamentals of Software Architecture (Chapter 2)", want) || bookLevelCheckpoint("Chapter 2", want) {
		t.Fatal("a distinct chapter title was treated as the book title")
	}
}

func TestCheckpointLabelDoesNotPresentBookTitleAsChapter(t *testing.T) {
	cacheRoot := t.TempDir()
	sourcePath := filepath.Join(t.TempDir(), "book.epub")
	store, err := session.NewStore(cacheRoot, sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		state session.State
		want  string
	}{
		{name: "document only", state: session.State{DocumentID: "book", SectionPath: []string{"Fundamentals of Software Architecture"}}, want: "Saved place"},
		{name: "chapter under book", state: session.State{DocumentID: "book", SectionPath: []string{"Fundamentals of Software Architecture", "Chapter 3"}}, want: "Chapter 3"},
		{name: "pdf page", state: session.State{DocumentID: "book", Page: 41, SectionPath: []string{"Fundamentals of Software Architecture"}}, want: "PDF page 41"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := store.Save(context.Background(), test.state); err != nil {
				t.Fatal(err)
			}
			if got := checkpointLabel(cacheRoot, sourcePath); got != test.want {
				t.Fatalf("checkpointLabel() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestRelinkTransfersCheckpointToNewPathWithoutChangingItsAnchor(t *testing.T) {
	cacheRoot := t.TempDir()
	oldPath := filepath.Join(t.TempDir(), "old.epub")
	newPath := filepath.Join(t.TempDir(), "new.epub")
	oldStore, err := session.NewStore(cacheRoot, oldPath)
	if err != nil {
		t.Fatal(err)
	}
	want := session.State{
		DocumentID: "same-content", SegmentID: "chapter-two", SegmentSeq: 14,
		SectionPath: []string{"Book", "Chapter 2"}, Offset: 4200 * time.Millisecond,
		TargetLanguage: "pt-BR",
	}
	if err := oldStore.Save(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	if err := transferCheckpoint(context.Background(), cacheRoot, oldPath, newPath); err != nil {
		t.Fatalf("transferCheckpoint() error = %v", err)
	}
	newStore, err := session.NewStore(cacheRoot, newPath)
	if err != nil {
		t.Fatal(err)
	}
	got, found, err := newStore.Load(context.Background())
	if err != nil || !found {
		t.Fatalf("new checkpoint found=%v, err=%v", found, err)
	}
	if got.SourcePath != newPath || got.DocumentID != want.DocumentID || got.SegmentID != want.SegmentID || got.Offset != want.Offset || got.TargetLanguage != want.TargetLanguage {
		t.Fatalf("transferred checkpoint = %#v, want anchor and settings from %#v", got, want)
	}
}

func TestRelinkKeepsAnExistingDestinationCheckpoint(t *testing.T) {
	cacheRoot := t.TempDir()
	oldPath := filepath.Join(t.TempDir(), "old.epub")
	newPath := filepath.Join(t.TempDir(), "new.epub")
	oldStore, err := session.NewStore(cacheRoot, oldPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := oldStore.Save(context.Background(), session.State{DocumentID: "old-content", SegmentID: "old-place", Offset: 5 * time.Second}); err != nil {
		t.Fatal(err)
	}
	destinationStore, err := session.NewStore(cacheRoot, newPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := destinationStore.Save(context.Background(), session.State{DocumentID: "new-content", SegmentID: "new-place", Offset: 12 * time.Second}); err != nil {
		t.Fatal(err)
	}
	if err := transferCheckpoint(context.Background(), cacheRoot, oldPath, newPath); err != nil {
		t.Fatalf("transferCheckpoint() error = %v", err)
	}
	got, found, err := destinationStore.Load(context.Background())
	if err != nil || !found || got.SegmentID != "new-place" || got.Offset != 12*time.Second {
		t.Fatalf("destination checkpoint = %#v, found=%v, err=%v; want its existing place", got, found, err)
	}
}

func TestRelinkRequiresMatchingRememberedReadingOptions(t *testing.T) {
	entry := catalog.Entry{TargetLanguage: "pt-BR", CodeMode: true}
	if !readingOptionsMatch(entry, listenOptions{targetLanguage: "pt-BR", code: true}) {
		t.Fatal("matching translation and code choices were rejected")
	}
	if readingOptionsMatch(entry, listenOptions{targetLanguage: "pt-BR"}) || readingOptionsMatch(entry, listenOptions{code: true}) {
		t.Fatal("different translation or code choices were allowed to carry a checkpoint")
	}
	if got := catalogEntryForUI(catalog.Entry{Path: "/old/book.epub", Title: "Book", ResumeLabel: "Chapter 2", Complete: true}); got.Path != "/old/book.epub" || !got.Complete {
		t.Fatalf("undo snapshot conversion lost catalog entry: %#v", got)
	}
	if got := relinkNote("Finished listening.", true, catalog.Entry{Title: "Book"}); got == "Finished listening." {
		t.Fatalf("relink note did not explain the replacement: %q", got)
	}
}

func TestRelinkKeepsOldEntryWhenReplacementWasNotSavedAndRemovesItAfterSave(t *testing.T) {
	ctx := context.Background()
	library := catalog.New(t.TempDir())
	oldPath := filepath.Join(t.TempDir(), "missing-old.epub")
	newPath := filepath.Join(t.TempDir(), "replacement.epub")
	oldEntry, err := library.Upsert(ctx, catalog.Entry{Path: oldPath, Title: "Moved book"})
	if err != nil {
		t.Fatalf("seed old entry: %v", err)
	}
	entries := []catalog.Entry{oldEntry}

	unchanged, removed, err := removeRelinkedCatalogEntry(ctx, library, entries, oldPath, newPath, false)
	if err != nil || removed || len(unchanged) != 1 || unchanged[0].Path != oldEntry.Path {
		t.Fatalf("failed replacement save removed the old entry: entries=%#v removed=%v err=%v", unchanged, removed, err)
	}
	stored, err := library.List(ctx)
	if err != nil || len(stored) != 1 || stored[0].Path != oldEntry.Path {
		t.Fatalf("old recovery entry after failed replacement save = %#v, err=%v", stored, err)
	}

	if _, err := library.Upsert(ctx, catalog.Entry{Path: newPath, Title: "Replacement"}); err != nil {
		t.Fatalf("save replacement entry: %v", err)
	}
	updated, removed, err := removeRelinkedCatalogEntry(ctx, library, entries, oldPath, newPath, true)
	if err != nil || !removed || len(updated) != 0 {
		t.Fatalf("successful replacement save did not remove old entry: entries=%#v removed=%v err=%v", updated, removed, err)
	}
	stored, err = library.List(ctx)
	if err != nil || len(stored) != 1 || stored[0].Path != newPath {
		t.Fatalf("catalog after successful replacement save = %#v, err=%v", stored, err)
	}
	failureNote := "Finished listening, but this source could not be added to the listening home."
	if got := relinkHomeNote(failureNote, false, true, catalog.Entry{Title: "Moved book"}); got != failureNote || strings.Contains(got, "relinked") {
		t.Fatalf("failed replacement save note falsely reports relinking: %q", got)
	}
	if got := relinkHomeNote("Finished listening.", true, true, catalog.Entry{Title: "Moved book"}); !strings.Contains(got, "relinked") {
		t.Fatalf("successful replacement save note omits relinking: %q", got)
	}
}
