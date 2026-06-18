package build

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"audiobooks/internal/device"
	"audiobooks/internal/library"
)

type recordingCommands struct {
	calls []string
	run   func(string) error
}

func (r *recordingCommands) Run(_ context.Context, _ io.Writer, name string, args ...string) error {
	call := name + " " + strings.Join(args, " ")
	r.calls = append(r.calls, call)
	if r.run != nil {
		return r.run(call)
	}
	return nil
}

func TestDryRunDoesNotExecuteCommands(t *testing.T) {
	commands := &recordingCommands{}
	runner := &Runner{Commands: commands}
	request := validRequest()
	request.DryRun = true

	var events []Event
	for event := range runner.Run(context.Background(), request) {
		events = append(events, event)
	}
	if len(commands.calls) != 0 {
		t.Fatalf("dry run executed commands: %v", commands.calls)
	}
	if len(events) < 2 || !events[len(events)-1].Done || events[len(events)-1].Err != nil {
		t.Fatalf("unexpected events: %#v", events)
	}
	if !strings.Contains(events[len(events)-1].Message, "no files or devices were changed") {
		t.Fatalf("missing dry-run completion message: %#v", events[len(events)-1])
	}
}

func TestRunnerRejectsUnsafeDeviceBeforeCommands(t *testing.T) {
	commands := &recordingCommands{}
	runner := &Runner{Commands: commands}
	request := validRequest()
	request.Device.Identifier = "disk11s1"

	event := <-runner.Run(context.Background(), request)
	if event.Err == nil || !strings.Contains(event.Err.Error(), "unsafe target") {
		t.Fatalf("error = %v, want unsafe target", event.Err)
	}
	if len(commands.calls) != 0 {
		t.Fatalf("unsafe request executed commands: %v", commands.calls)
	}
}

func TestPreflightFailureHappensBeforeDestructiveCommands(t *testing.T) {
	source := filepath.Join(t.TempDir(), "book.mp3")
	if err := os.WriteFile(source, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	commands := &recordingCommands{}
	runner := &Runner{
		Commands: commands,
		LookPath: func(tool string) (string, error) {
			if tool == "ffmpeg" {
				return "", os.ErrNotExist
			}
			return "/usr/bin/" + tool, nil
		},
	}
	request := validRequest()
	request.Book.Files = []string{source}
	request.Mode = ModeLibrary
	request.Format = true

	var final Event
	for event := range runner.Run(context.Background(), request) {
		final = event
	}
	if final.Err == nil || !strings.Contains(final.Err.Error(), "ffmpeg is required") {
		t.Fatalf("error = %v, want missing ffmpeg", final.Err)
	}
	if len(commands.calls) != 0 {
		t.Fatalf("preflight failure executed commands: %v", commands.calls)
	}
}

func TestEstimatedOutputMatchesLegacyRatesAndOverhead(t *testing.T) {
	request := validRequest()
	request.Metadata.Duration = time.Hour
	request.Mode = ModeDaisy
	daisy := estimatedOutputBytes(request)
	request.Mode = ModeLibrary
	libraryBytes := estimatedOutputBytes(request)

	if daisy != 30_240_000 {
		t.Fatalf("DAISY estimate = %d, want 30240000", daisy)
	}
	if libraryBytes != 34_560_000 {
		t.Fatalf("Library estimate = %d, want 34560000", libraryBytes)
	}
}

func TestPreflightAllowsReadableMakeDaisyScript(t *testing.T) {
	repository := t.TempDir()
	tools := filepath.Join(repository, "tools", "MakeDaisy")
	if err := os.MkdirAll(tools, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tools, "makedaisy"), []byte("# python"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tools, "id3dump"), []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "book.mp3")
	if err := os.WriteFile(source, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	request := validRequest()
	request.Repository = repository
	request.Book.Files = []string{source}
	runner := &Runner{LookPath: func(tool string) (string, error) {
		return "/usr/bin/" + tool, nil
	}}
	if err := runner.preflight(request); err != nil {
		t.Fatalf("readable MakeDaisy script rejected: %v", err)
	}
}

func TestValidateTargetOutput(t *testing.T) {
	target := t.TempDir()
	if err := os.Mkdir(filepath.Join(target, ".Spotlight-V100"), 0o000); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "01.mp3"), []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	if count, err := validateTargetOutput(target, ModeLibrary); err != nil || count != 1 {
		t.Fatalf("Library output rejected: %v", err)
	}
	if _, err := validateTargetOutput(target, ModeDaisy); err == nil {
		t.Fatal("DAISY output without ncc.html was accepted")
	}
	if err := os.WriteFile(filepath.Join(target, "ncc.html"), []byte("<html>nav</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if count, err := validateTargetOutput(target, ModeDaisy); err != nil || count != 1 {
		t.Fatalf("valid DAISY output rejected: %v", err)
	}
}

func TestEjectFallsBackToForceUnmount(t *testing.T) {
	ejects := 0
	commands := &recordingCommands{}
	commands.run = func(call string) error {
		if call == "diskutil eject disk11" {
			ejects++
			if ejects == 1 {
				return os.ErrPermission
			}
		}
		return nil
	}
	runner := &Runner{Commands: commands}
	var events []Event
	err := runner.eject(context.Background(), func(event Event) {
		events = append(events, event)
	}, "disk11")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"diskutil eject disk11",
		"diskutil unmountDisk force disk11",
		"diskutil eject disk11",
	}
	if strings.Join(commands.calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls = %v, want %v", commands.calls, want)
	}
	if len(events) != 1 || !strings.Contains(events[0].Message, "force-unmounting") {
		t.Fatalf("fallback event = %#v", events)
	}
}

func TestConcatFilterPreservesLegacyAudioSettings(t *testing.T) {
	args := strings.Join(concatFilterArgs([]string{"01.mp3", "02.mp3"}, "22050", "64k"), " ")
	for _, required := range []string{
		"-i 01.mp3", "-i 02.mp3", "concat=n=2:v=0:a=1[out]",
		"-ar 22050", "-ac 1", "-b:a 64k",
	} {
		if !strings.Contains(args, required) {
			t.Fatalf("args %q missing %q", args, required)
		}
	}
}

func TestDaisySegmentEncodingDisablesCrossFileBitReservoir(t *testing.T) {
	got := strings.Join(daisySourceArgs([]string{"book.m4b"}), " ")
	if !strings.Contains(got, "-reservoir 0") {
		t.Fatalf("DAISY segment args %q allow cross-file bit reservoir references", got)
	}
}

func TestStableIdentifiersAndNames(t *testing.T) {
	request := validRequest()
	if got, want := daisyUID(request), daisyUID(request); got != want {
		t.Fatalf("DAISY UID not stable: %q != %q", got, want)
	}
	if got := volumeName("NO NAME"); got != "NO_NAME" {
		t.Fatalf("volumeName = %q", got)
	}
}

func validRequest() Request {
	return Request{
		Book: library.Book{
			Name:      "Test Book",
			Files:     []string{"test.mp3"},
			SizeBytes: 1234,
		},
		Device: device.Device{
			Identifier: "disk11",
			Protocol:   "USB",
			Name:       "NO NAME",
			MountPoint: "/Volumes/NO NAME",
		},
		Mode:       ModeDaisy,
		Metadata:   library.Metadata{Title: "Test Book", Creator: "Author"},
		Repository: "/repo",
	}
}
