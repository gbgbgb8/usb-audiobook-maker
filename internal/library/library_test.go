package library

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestScanFindsSingleAndMultipartBooks(t *testing.T) {
	root := t.TempDir()
	single := filepath.Join(root, "Single Book.m4b")
	writeTestFile(t, single, 9)
	multipart := filepath.Join(root, "Multipart")
	if err := os.Mkdir(multipart, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(multipart, "02.mp3"), 2)
	writeTestFile(t, filepath.Join(multipart, "01.mp3"), 1)
	writeTestFile(t, filepath.Join(multipart, "cover.jpg"), 50)
	writeTestFile(t, filepath.Join(root, "notes.txt"), 10)

	now := time.Now()
	if err := os.Chtimes(single, now.Add(time.Hour), now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	books, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 2 {
		t.Fatalf("got %d books, want 2", len(books))
	}
	if books[0].Name != "Single Book" {
		t.Fatalf("newest book = %q, want Single Book", books[0].Name)
	}
	if got := books[1].Files; len(got) != 2 ||
		filepath.Base(got[0]) != "01.mp3" ||
		filepath.Base(got[1]) != "02.mp3" {
		t.Fatalf("multipart order = %v", got)
	}
	if books[1].SizeBytes != 3 {
		t.Fatalf("multipart size = %d, want 3", books[1].SizeBytes)
	}
}

func TestFormatHelpers(t *testing.T) {
	if got := FormatDuration(10*time.Hour + 7*time.Minute); got != "10h 07m" {
		t.Fatalf("FormatDuration = %q", got)
	}
	if got := FormatBytes(4 * 1024 * 1024 * 1024); got != "4.0 GB" {
		t.Fatalf("FormatBytes = %q", got)
	}
}

func TestPreferredTitleUsesAlbumForMultipartBook(t *testing.T) {
	tags := map[string]string{
		"title": "[1-2] A Rule Against Murder",
		"album": "A Rule Against Murder",
	}
	multipart := Book{Name: "Louise Penny - A Rule Against Murder", Files: []string{"01.mp3", "02.mp3"}}
	if got := preferredTitle(multipart, tags); got != "A Rule Against Murder" {
		t.Fatalf("multipart title = %q", got)
	}
	single := Book{Name: "Book", Files: []string{"book.mp3"}}
	if got := preferredTitle(single, tags); got != "[1-2] A Rule Against Murder" {
		t.Fatalf("single-file title = %q", got)
	}
}

func writeTestFile(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}
