package library

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

var audioExtensions = map[string]bool{
	".mp3": true,
	".m4a": true,
	".m4b": true,
}

type Book struct {
	Name       string
	Path       string
	Files      []string
	SizeBytes  int64
	ModifiedAt time.Time
	Metadata   Metadata
}

type Metadata struct {
	Title     string
	Creator   string
	Narrator  string
	Publisher string
	Duration  time.Duration
}

func (b Book) MultiPart() bool {
	return len(b.Files) > 1
}

func Scan(root string) ([]Book, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read books directory: %w", err)
	}

	var books []Book
	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		if entry.IsDir() {
			files, err := audioFiles(path)
			if err != nil {
				return nil, err
			}
			if len(files) == 0 {
				continue
			}
			// Folder names are used verbatim; only loose files lose their
			// audio extension. A dot inside a folder name is not an extension.
			book, err := makeBook(entry.Name(), path, files)
			if err != nil {
				return nil, err
			}
			books = append(books, book)
			continue
		}
		if !isAudio(entry.Name()) {
			continue
		}
		book, err := makeBook(strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name())), path, []string{path})
		if err != nil {
			return nil, err
		}
		books = append(books, book)
	}

	slices.SortFunc(books, func(a, b Book) int {
		if a.ModifiedAt.Equal(b.ModifiedAt) {
			return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
		}
		if a.ModifiedAt.After(b.ModifiedAt) {
			return -1
		}
		return 1
	})
	return books, nil
}

func Probe(ctx context.Context, book Book) Metadata {
	type probeOutput struct {
		Format struct {
			Duration string            `json:"duration"`
			Tags     map[string]string `json:"tags"`
		} `json:"format"`
	}

	var total time.Duration
	var first probeOutput
	for i, file := range book.Files {
		out, err := exec.CommandContext(ctx, "ffprobe", "-v", "quiet", "-show_entries",
			"format=duration:format_tags=title,album,artist,album_artist,composer,publisher",
			"-of", "json", file).Output()
		if err != nil {
			continue
		}
		var p probeOutput
		if json.Unmarshal(out, &p) != nil {
			continue
		}
		if i == 0 {
			first = p
		}
		var seconds float64
		fmt.Sscanf(p.Format.Duration, "%f", &seconds)
		total += time.Duration(seconds * float64(time.Second))
	}

	tags := first.Format.Tags
	title := preferredTitle(book, tags)
	creator := firstNonEmpty(tags["album_artist"], tags["artist"], tags["composer"])
	narrator := ""
	if tags["artist"] != "" && tags["artist"] != creator {
		narrator = tags["artist"]
	} else if tags["composer"] != "" && tags["composer"] != creator {
		narrator = tags["composer"]
	}
	return Metadata{
		Title:     title,
		Creator:   creator,
		Narrator:  narrator,
		Publisher: firstNonEmpty(tags["publisher"], "Private"),
		Duration:  total,
	}
}

func preferredTitle(book Book, tags map[string]string) string {
	if book.MultiPart() {
		return firstNonEmpty(tags["album"], book.Name, tags["title"])
	}
	return firstNonEmpty(tags["title"], tags["album"], book.Name)
}

func audioFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read book directory %q: %w", dir, err)
	}
	var files []string
	for _, entry := range entries {
		if !entry.IsDir() && isAudio(entry.Name()) {
			files = append(files, filepath.Join(dir, entry.Name()))
		}
	}
	slices.SortFunc(files, func(a, b string) int {
		return strings.Compare(strings.ToLower(filepath.Base(a)), strings.ToLower(filepath.Base(b)))
	})
	return files, nil
}

func makeBook(name, path string, files []string) (Book, error) {
	book := Book{Name: name, Path: path, Files: files}
	for _, file := range files {
		info, err := os.Stat(file)
		if err != nil {
			return Book{}, fmt.Errorf("stat audiobook file %q: %w", file, err)
		}
		book.SizeBytes += info.Size()
		if info.ModTime().After(book.ModifiedAt) {
			book.ModifiedAt = info.ModTime()
		}
	}
	return book, nil
}

func isAudio(name string) bool {
	return audioExtensions[strings.ToLower(filepath.Ext(name))]
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func FormatDuration(d time.Duration) string {
	if d <= 0 {
		return "unknown duration"
	}
	hours := int(d.Hours())
	minutes := int(d.Minutes()) % 60
	if hours > 0 {
		return fmt.Sprintf("%dh %02dm", hours, minutes)
	}
	return fmt.Sprintf("%dm", minutes)
}

func FormatBytes(size int64) string {
	const mib = 1024 * 1024
	const gib = 1024 * mib
	if size >= gib {
		return fmt.Sprintf("%.1f GB", float64(size)/gib)
	}
	return fmt.Sprintf("%.0f MB", float64(size)/mib)
}
