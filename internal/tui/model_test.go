package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"audiobooks/internal/build"
	"audiobooks/internal/device"
	"audiobooks/internal/library"
)

type fakeBuilder struct {
	requests []build.Request
}

func (f *fakeBuilder) Run(_ context.Context, request build.Request) <-chan build.Event {
	f.requests = append(f.requests, request)
	events := make(chan build.Event, 1)
	events <- build.Event{Done: true, Message: "complete"}
	close(events)
	return events
}

func TestConfirmationDefaultsToCancelAndRequiresDeliberateSelection(t *testing.T) {
	builder := &fakeBuilder{}
	model := confirmationModel(builder)

	updated, _ := model.handleKey(key(tea.KeyEnter))
	got := updated.(Model)
	if got.step != stepReview {
		t.Fatalf("default confirmation step = %v, want review", got.step)
	}
	if len(builder.requests) != 0 {
		t.Fatal("default Cancel selection started a build")
	}

	got.step = stepConfirm
	updated, _ = got.handleKey(key(tea.KeyRight))
	got = updated.(Model)
	if !got.confirmYes {
		t.Fatal("right arrow did not select destructive action")
	}
	updated, command := got.handleKey(key(tea.KeyEnter))
	got = updated.(Model)
	if got.step != stepRunning {
		t.Fatalf("confirmed step = %v, want running", got.step)
	}
	if command == nil || len(builder.requests) != 1 {
		t.Fatal("visual confirmation did not start one build")
	}
	if builder.requests[0].Device.Identifier != "disk11" {
		t.Fatalf("build target = %q", builder.requests[0].Device.Identifier)
	}
}

func TestDryRunUsesSameVisualConfirmation(t *testing.T) {
	builder := &fakeBuilder{}
	model := confirmationModel(builder)
	model.deps.DryRun = true

	updated, _ := model.handleKey(key(tea.KeyEnter))
	got := updated.(Model)
	if got.step != stepReview || len(builder.requests) != 0 {
		t.Fatalf("dry run bypassed default Cancel: step=%v requests=%d", got.step, len(builder.requests))
	}
	got.step = stepConfirm
	updated, _ = got.handleKey(key(tea.KeyRight))
	got = updated.(Model)
	updated, _ = got.handleKey(key(tea.KeyEnter))
	got = updated.(Model)
	if got.step != stepRunning || len(builder.requests) != 1 {
		t.Fatalf("confirmed dry run did not start: step=%v requests=%d", got.step, len(builder.requests))
	}
	if !builder.requests[0].DryRun {
		t.Fatal("dry-run request lost its flag")
	}
}

func TestModeToggleKeepsOnlySupportedModes(t *testing.T) {
	model := confirmationModel(&fakeBuilder{})
	model.step = stepReview
	model.mode = build.ModeDaisy

	updated, _ := model.handleKey(textKey("m"))
	got := updated.(Model)
	if got.mode != build.ModeLibrary {
		t.Fatalf("mode = %v, want Library", got.mode)
	}
	updated, _ = got.handleKey(textKey("m"))
	got = updated.(Model)
	if got.mode != build.ModeDaisy {
		t.Fatalf("mode = %v, want DAISY", got.mode)
	}

	updated, _ = got.handleKey(key(tea.KeyRight))
	got = updated.(Model)
	if got.mode != build.ModeLibrary {
		t.Fatalf("right arrow mode = %v, want Library", got.mode)
	}
	updated, _ = got.handleKey(key(tea.KeyLeft))
	got = updated.(Model)
	if got.mode != build.ModeDaisy {
		t.Fatalf("left arrow mode = %v, want DAISY", got.mode)
	}
}

func TestCompatibleFAT32TargetDefaultsToKeepFormat(t *testing.T) {
	model := confirmationModel(&fakeBuilder{})
	model.step = stepDevices
	model.devices[0].Filesystem = "DOS_FAT_32"
	model.devices[0].Scheme = "FDisk_partition_scheme"
	model.format = true

	updated, _ := model.handleKey(key(tea.KeyEnter))
	got := updated.(Model)
	if got.step != stepReview {
		t.Fatalf("step = %v, want review", got.step)
	}
	if got.format {
		t.Fatal("compatible FAT32 target defaulted to reformat")
	}
}

func TestNextCartridgeKeepsBookAndRescansDevices(t *testing.T) {
	model := confirmationModel(&fakeBuilder{})
	model.step = stepDone
	model.completed = true
	model.bookCursor = 0
	model.metadata = library.Metadata{Title: "Book"}

	updated, command := model.handleKey(textKey("n"))
	got := updated.(Model)
	if got.step != stepLoading || command == nil {
		t.Fatalf("next cartridge step=%v command=%v", got.step, command)
	}
	if got.bookCursor != 0 || got.metadata.Title != "Book" {
		t.Fatal("next cartridge did not preserve book configuration")
	}
}

func TestBookRefreshSelectsNewlyAddedBook(t *testing.T) {
	model := confirmationModel(&fakeBuilder{})
	model.step = stepBooks
	model.books = []library.Book{
		{Name: "Current", Path: "/books/current", Files: []string{"current.mp3"}},
		{Name: "Older", Path: "/books/older", Files: []string{"older.mp3"}},
	}
	model.bookCursor = 1
	model.deps.ScanBooks = func(string) ([]library.Book, error) {
		return []library.Book{
			{Name: "New Book", Path: "/books/new", Files: []string{"new.mp3"}},
			{Name: "Current", Path: "/books/current", Files: []string{"current.mp3"}},
			{Name: "Older", Path: "/books/older", Files: []string{"older.mp3"}},
		}, nil
	}

	updated, command := model.handleKey(textKey("r"))
	got := updated.(Model)
	if !got.refreshing || command == nil {
		t.Fatal("refresh did not start")
	}
	updated, _ = got.Update(command())
	got = updated.(Model)
	if got.refreshing {
		t.Fatal("refresh did not finish")
	}
	if len(got.books) != 3 || got.books[got.bookCursor].Name != "New Book" {
		t.Fatalf("refreshed selection = %q, want New Book", got.books[got.bookCursor].Name)
	}
	if got.bookStatus != "Found 1 new audiobook" {
		t.Fatalf("refresh status = %q", got.bookStatus)
	}
}

func TestBookRefreshPreservesSelectionWhenNothingWasAdded(t *testing.T) {
	model := confirmationModel(&fakeBuilder{})
	model.step = stepBooks
	model.books = []library.Book{
		{Name: "First", Path: "/books/first", Files: []string{"first.mp3"}},
		{Name: "Selected", Path: "/books/selected", Files: []string{"selected.mp3"}},
	}
	model.bookCursor = 1
	model.deps.ScanBooks = func(string) ([]library.Book, error) {
		return []library.Book{
			{Name: "Selected", Path: "/books/selected", Files: []string{"selected.mp3"}},
			{Name: "First", Path: "/books/first", Files: []string{"first.mp3"}},
		}, nil
	}

	updated, command := model.handleKey(textKey("r"))
	got := updated.(Model)
	updated, _ = got.Update(command())
	got = updated.(Model)
	if got.books[got.bookCursor].Name != "Selected" {
		t.Fatalf("refreshed selection = %q, want Selected", got.books[got.bookCursor].Name)
	}
	if got.bookStatus != "Library is up to date" {
		t.Fatalf("refresh status = %q", got.bookStatus)
	}
}

func TestCoreViewsFitStandardTerminal(t *testing.T) {
	model := confirmationModel(&fakeBuilder{})
	model.width = 80
	model.height = 24
	model.metadata = library.Metadata{
		Title:    "A Rule Against Murder",
		Creator:  "Louise Penny",
		Narrator: "Ralph Cosham",
	}
	model.devices[0].Filesystem = "DOS_FAT_32"
	model.devices[0].Scheme = "FDisk_partition_scheme"

	model.step = stepBooks
	books := model.booksView()
	assertFits(t, "books", books, 80, 24)
	if !strings.Contains(books, "r refresh") {
		t.Fatalf("book view does not advertise refresh:\n%s", books)
	}
	model.step = stepReview
	review := model.reviewView()
	assertFits(t, "review", review, 80, 24)
	if !strings.Contains(review, "LISTENING MODE") ||
		!strings.Contains(review, "●") ||
		!strings.Contains(review, "○") {
		t.Fatalf("review does not render one radio-style mode selector:\n%s", review)
	}
	model.step = stepConfirm
	assertFits(t, "confirm", model.confirmView(), 80, 24)
	model.step = stepRunning
	model.started = time.Now().Add(-time.Minute)
	model.phase = "daisy"
	model.progress = 0.52
	model.logs = []string{"Checking tools", "Preparing source", "Building DAISY package"}
	assertFits(t, "running", model.runningView(), 80, 24)
	model.step = stepDone
	model.completed = true
	model.logs = []string{"Validated output", "Synced USB", "Ejected USB"}
	assertFits(t, "done", model.doneView(), 80, 24)
}

func confirmationModel(builder build.Service) Model {
	model := New(Dependencies{
		Context:    context.Background(),
		Repository: "/repo",
		Builder:    builder,
	})
	model.step = stepConfirm
	model.books = []library.Book{{Name: "Book", Files: []string{"book.mp3"}}}
	model.devices = []device.Device{{
		Identifier: "disk11",
		Protocol:   "USB",
		Name:       "NO NAME",
		MountPoint: "/Volumes/NO NAME",
	}}
	model.mode = build.ModeDaisy
	model.format = true
	return model
}

func key(code rune) tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: code})
}

func textKey(value string) tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: []rune(value)[0], Text: value})
}

func assertFits(t *testing.T, name, view string, width, height int) {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(view, "\n"), "\n")
	if len(lines) > height {
		t.Fatalf("%s height = %d, want <= %d\n%s", name, len(lines), height, view)
	}
	for i, line := range lines {
		if got := lipgloss.Width(line); got > width {
			t.Fatalf("%s line %d width = %d, want <= %d: %q", name, i+1, got, width, line)
		}
	}
}
