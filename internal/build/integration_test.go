package build

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"audiobooks/internal/device"
	"audiobooks/internal/library"
)

func TestIntegrationRepresentativeBuilds(t *testing.T) {
	if os.Getenv("AUDIOBOOK_INTEGRATION") != "1" {
		t.Skip("set AUDIOBOOK_INTEGRATION=1 to run FFmpeg and MakeDaisy builds")
	}
	for _, tool := range []string{"ffmpeg", "ffprobe", "lame", "python3"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("%s unavailable: %v", tool, err)
		}
	}

	repository, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	runner := NewRunner()
	ctx := context.Background()

	t.Run("single-file DAISY", func(t *testing.T) {
		source := makeTone(t, "single.mp3", 2, "Single Test", "1")
		book := testBook(t, "Single Test", []string{source})
		target := t.TempDir()
		work := t.TempDir()
		request := integrationRequest(repository, book, ModeDaisy)
		if err := runner.buildDaisy(ctx, request, target, work, func(Event) {}); err != nil {
			t.Fatal(err)
		}
		assertFile(t, filepath.Join(target, "ncc.html"))
		assertFile(t, filepath.Join(target, "01.mp3"))
	})

	t.Run("multi-file DAISY", func(t *testing.T) {
		first := makeTone(t, "01.mp3", 1, "Part One", "1")
		second := makeTone(t, "02.mp3", 1, "Part Two", "2")
		book := testBook(t, "Multipart Test", []string{first, second})
		target := t.TempDir()
		work := t.TempDir()
		request := integrationRequest(repository, book, ModeDaisy)
		if err := runner.buildDaisy(ctx, request, target, work, func(Event) {}); err != nil {
			t.Fatal(err)
		}
		assertFile(t, filepath.Join(target, "ncc.html"))
		assertFile(t, filepath.Join(target, "01.mp3"))
		assertFile(t, filepath.Join(target, "02.mp3"))
	})

	t.Run("multi-file Library Mode", func(t *testing.T) {
		first := makeTone(t, "01.mp3", 1, "Part One", "1")
		second := makeTone(t, "02.mp3", 1, "Part Two", "2")
		book := testBook(t, "Library Test", []string{first, second})
		target := t.TempDir()
		work := t.TempDir()
		request := integrationRequest(repository, book, ModeLibrary)
		if err := runner.buildLibrary(ctx, request, target, work, func(Event) {}); err != nil {
			t.Fatal(err)
		}
		matches, err := filepath.Glob(filepath.Join(target, "audio+podcasts", "*", "01.mp3"))
		if err != nil || len(matches) != 1 {
			t.Fatalf("Library Mode output = %v, error = %v", matches, err)
		}
	})
}

func makeTone(t *testing.T, name string, seconds int, title, track string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=22050",
		"-t", string(rune('0'+seconds)), "-ac", "1", "-b:a", "64k",
		"-metadata", "album=Integration Test",
		"-metadata", "title="+title,
		"-metadata", "artist=Test Author",
		"-metadata", "track="+track,
		path, "-y")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate tone: %v\n%s", err, output)
	}
	return path
}

func testBook(t *testing.T, name string, files []string) library.Book {
	t.Helper()
	var size int64
	for _, file := range files {
		info, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		size += info.Size()
	}
	return library.Book{Name: name, Path: filepath.Dir(files[0]), Files: files, SizeBytes: size}
}

func integrationRequest(repository string, book library.Book, mode Mode) Request {
	return Request{
		Book: book,
		Device: device.Device{
			Identifier: "disk99",
			Protocol:   "USB",
			Name:       "TEST",
			MountPoint: "/Volumes/TEST",
		},
		Mode:       mode,
		Metadata:   library.Metadata{Title: book.Name, Creator: "Test Author"},
		Repository: repository,
	}
}

func assertFile(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("missing %s: %v", path, err)
	}
	if info.Size() == 0 {
		t.Fatalf("%s is empty", path)
	}
}
