package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	tea "charm.land/bubbletea/v2"

	"audiobooks/internal/build"
	"audiobooks/internal/device"
	"audiobooks/internal/library"
	"audiobooks/internal/tui"
)

func main() {
	var (
		plain    = flag.Bool("plain", false, "run the accessible plain-terminal workflow")
		dryRun   = flag.Bool("dry-run", false, "show the complete workflow without changing the USB")
		booksDir = flag.String("books", "", "audiobook library directory")
		verbose  = flag.Bool("verbose", false, "enable verbose output in plain mode")
		shortV   = flag.Bool("v", false, "enable verbose output in plain mode")
	)
	flag.Parse()

	repository, err := findRepository()
	if err != nil {
		fatal(err)
	}
	if *plain {
		args := flag.Args()
		if *verbose || *shortV {
			args = append([]string{"--verbose"}, args...)
		}
		cmd := exec.Command(filepath.Join(repository, "load_audiobook.sh"), args...)
		cmd.Dir = repository
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fatal(err)
		}
		return
	}
	if *booksDir == "" {
		*booksDir = filepath.Join(repository, "books")
	}

	model := tui.New(tui.Dependencies{
		Context:    context.Background(),
		BooksDir:   *booksDir,
		Repository: repository,
		DryRun:     *dryRun,
		Devices:    device.ExecRunner{},
		Builder:    build.NewRunner(),
		ScanBooks:  library.Scan,
		ProbeBook:  library.Probe,
	})
	if _, err := tea.NewProgram(model).Run(); err != nil {
		fatal(err)
	}
}

func findRepository() (string, error) {
	if executable, err := os.Executable(); err == nil {
		for _, candidate := range []string{
			filepath.Dir(executable),
			filepath.Dir(filepath.Dir(executable)),
		} {
			if _, err := os.Stat(filepath.Join(candidate, "load_audiobook.sh")); err == nil {
				return candidate, nil
			}
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(cwd, "load_audiobook.sh")); err != nil {
		return "", fmt.Errorf("run from the audiobooks repository: %w", err)
	}
	return cwd, nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "Error:", err)
	os.Exit(1)
}
