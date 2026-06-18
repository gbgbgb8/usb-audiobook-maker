package build

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"audiobooks/internal/device"
	"audiobooks/internal/library"
)

type CommandRunner interface {
	Run(context.Context, io.Writer, string, ...string) error
}

type ExecCommandRunner struct{}

func (ExecCommandRunner) Run(ctx context.Context, output io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = output
	cmd.Stderr = output
	return cmd.Run()
}

type Runner struct {
	Commands CommandRunner
	LookPath func(string) (string, error)
}

type chapter struct {
	Start float64
	Title string
}

func NewRunner() *Runner {
	return &Runner{
		Commands: ExecCommandRunner{},
		LookPath: exec.LookPath,
	}
}

func (r *Runner) Run(ctx context.Context, request Request) <-chan Event {
	events := make(chan Event, 32)
	go func() {
		defer close(events)
		send := func(event Event) {
			event.At = time.Now()
			events <- event
		}
		if err := validate(request); err != nil {
			send(Event{Err: err, Done: true})
			return
		}
		if request.DryRun {
			r.runDry(request, send)
			return
		}
		if err := r.run(ctx, request, send); err != nil {
			send(Event{Err: err, Done: true})
			return
		}
		send(Event{Phase: "complete", Message: "USB ejected and safe to unplug", Percent: 1, Done: true})
	}()
	return events
}

func validate(request Request) error {
	if len(request.Book.Files) == 0 {
		return errors.New("no source audio files selected")
	}
	if !request.Device.SafeTarget() {
		return fmt.Errorf("refusing unsafe target %q: only external USB whole disks are allowed", request.Device.Identifier)
	}
	if request.Mode != ModeLibrary && request.Mode != ModeDaisy {
		return fmt.Errorf("unsupported mode %d", request.Mode)
	}
	if request.Repository == "" {
		return errors.New("repository path is required")
	}
	return nil
}

func (r *Runner) runDry(request Request, send func(Event)) {
	steps := []string{
		"validate external USB target " + request.Device.Identifier,
	}
	if request.Format {
		steps = append(steps, "format "+request.Device.Identifier+" as MBR + FAT32")
	}
	if request.Mode == ModeDaisy {
		steps = append(steps,
			"prepare DAISY source using the selected file order",
			"build DAISY 2.02 with MakeDaisy",
			"copy package to the USB root",
		)
	} else {
		steps = append(steps,
			"convert to 22.05 kHz mono 64 kb/s",
			"split into two-hour files under audio+podcasts/<book>",
		)
	}
	steps = append(steps, "clean macOS metadata and eject the USB")
	for i, step := range steps {
		send(Event{
			Phase:   "dry-run",
			Message: step,
			Percent: float64(i+1) / float64(len(steps)),
		})
	}
	send(Event{Phase: "complete", Message: "Dry run completed; no files or devices were changed", Percent: 1, Done: true})
}

func (r *Runner) run(ctx context.Context, request Request, send func(Event)) error {
	send(Event{Phase: "preflight", Message: "Checking tools, source files, and free space", Percent: 0.01})
	if err := r.preflight(request); err != nil {
		return err
	}

	work, err := os.MkdirTemp("", "audiobooks-tui-*")
	if err != nil {
		return fmt.Errorf("create temporary directory: %w", err)
	}
	defer os.RemoveAll(work)

	target := request.Device.MountPoint
	if request.Format {
		send(Event{Phase: "format", Message: "Unmounting selected USB", Percent: 0.03})
		if err := r.command(ctx, send, "diskutil", "unmountDisk", "force", request.Device.Identifier); err != nil {
			return fmt.Errorf("unmount USB: %w", err)
		}
		send(Event{Phase: "format", Message: "Formatting USB as MBR + FAT32", Percent: 0.05})
		name := volumeName(request.Device.Name)
		if err := r.command(ctx, send, "diskutil", "eraseDisk", "FAT32", name, "MBRFormat", request.Device.Identifier); err != nil {
			return fmt.Errorf("format USB: %w", err)
		}
		send(Event{Phase: "format", Message: "Waiting for the formatted volume to mount", Percent: 0.08})
		// Resolve the mount point from diskutil rather than assuming
		// /Volumes/<name>: macOS appends a suffix when another mounted
		// volume already uses the same name.
		mounted, err := r.waitForMount(ctx, request.Device.Identifier+"s1")
		if err != nil {
			return fmt.Errorf("%w; if the USB disappeared from Disk Utility, unplug and reinsert it before retrying", err)
		}
		target = mounted
	}
	if target == "" {
		return errors.New("selected USB has no mount point; enable formatting or mount it first")
	}
	send(Event{Phase: "prepare", Message: "Clearing previous cartridge content", Percent: 0.12})
	if err := clearTarget(target); err != nil {
		return err
	}
	if err := checkTargetCapacity(target, request); err != nil {
		return err
	}

	switch request.Mode {
	case ModeLibrary:
		if err := r.buildLibrary(ctx, request, target, work, send); err != nil {
			return err
		}
	case ModeDaisy:
		if err := r.buildDaisy(ctx, request, target, work, send); err != nil {
			return err
		}
	}
	audioFiles, err := validateTargetOutput(target, request.Mode)
	if err != nil {
		return err
	}
	validation := fmt.Sprintf("Validated %d audio file(s)", audioFiles)
	if request.Mode == ModeDaisy {
		validation += " and ncc.html"
	}
	send(Event{Phase: "validate", Message: validation, Percent: 0.91})

	send(Event{Phase: "finalize", Message: "Cleaning macOS metadata", Percent: 0.93})
	_ = r.commandQuiet(ctx, "dot_clean", "-m", target)
	_ = os.RemoveAll(filepath.Join(target, ".Spotlight-V100"))
	_ = os.RemoveAll(filepath.Join(target, ".fseventsd"))
	_ = os.RemoveAll(filepath.Join(target, ".Trashes"))
	_ = os.Remove(filepath.Join(target, ".metadata_never_index"))
	_ = r.commandQuiet(ctx, "xattr", "-cr", target)
	_ = r.commandQuiet(ctx, "mdutil", "-i", "off", target)
	send(Event{Phase: "eject", Message: "Syncing and ejecting USB", Percent: 0.97})
	if err := r.command(ctx, send, "sync"); err != nil {
		return fmt.Errorf("sync USB: %w", err)
	}
	if err := r.eject(ctx, send, request.Device.Identifier); err != nil {
		return err
	}
	return nil
}

func (r *Runner) eject(ctx context.Context, send func(Event), identifier string) error {
	if err := r.command(ctx, send, "diskutil", "eject", identifier); err != nil {
		send(Event{Phase: "eject", Message: "USB is busy; force-unmounting after sync", Percent: 0.98})
		if unmountErr := r.command(ctx, send, "diskutil", "unmountDisk", "force", identifier); unmountErr != nil {
			return fmt.Errorf("eject USB: %w; force unmount also failed: %v", err, unmountErr)
		}
		if retryErr := r.command(ctx, send, "diskutil", "eject", identifier); retryErr != nil {
			return fmt.Errorf("eject USB after force unmount: %w", retryErr)
		}
	}
	return nil
}

func (r *Runner) preflight(request Request) error {
	lookPath := r.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	required := []string{"ffmpeg", "diskutil", "dot_clean", "xattr", "mdutil", "sync"}
	if request.Mode == ModeDaisy {
		required = append(required, "python3", "lame")
	}
	for _, tool := range required {
		if _, err := lookPath(tool); err != nil {
			return fmt.Errorf("%s is required; run ./setup.sh: %w", tool, err)
		}
	}
	for _, source := range request.Book.Files {
		info, err := os.Stat(source)
		if err != nil {
			return fmt.Errorf("source audio unavailable %q: %w", source, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("source audio is not a regular file: %q", source)
		}
	}
	if request.Mode == ModeDaisy {
		script := filepath.Join(request.Repository, "tools", "MakeDaisy", "makedaisy")
		info, err := os.Stat(script)
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("MakeDaisy script unavailable; run ./setup.sh: %w", err)
		}
		id3dump := filepath.Join(request.Repository, "tools", "MakeDaisy", "id3dump")
		info, err = os.Stat(id3dump)
		if err != nil {
			return fmt.Errorf("MakeDaisy id3dump unavailable; run ./setup.sh: %w", err)
		}
		if info.Mode()&0o111 == 0 {
			return fmt.Errorf("MakeDaisy id3dump is not executable: %s", id3dump)
		}
	}
	estimated := estimatedOutputBytes(request)
	if estimated > 0 {
		needed := estimated * 2
		if request.Mode == ModeDaisy && request.Book.MultiPart() {
			// Multi-part sources are copied at their original bitrate into
			// the work directory before MakeDaisy re-encodes them.
			needed += request.Book.SizeBytes
		}
		free, err := freeBytes(os.TempDir())
		if err == nil && free < needed {
			return fmt.Errorf("not enough temporary disk space: have %s, need about %s",
				library.FormatBytes(free), library.FormatBytes(needed))
		}
	}
	return nil
}

func checkTargetCapacity(target string, request Request) error {
	estimated := estimatedOutputBytes(request)
	if estimated <= 0 {
		return nil
	}
	free, err := freeBytes(target)
	if err != nil {
		return fmt.Errorf("check USB free space: %w", err)
	}
	if free < estimated {
		return fmt.Errorf("not enough room on the USB: have %s, need about %s",
			library.FormatBytes(free), library.FormatBytes(estimated))
	}
	return nil
}

func validateTargetOutput(target string, mode Mode) (int, error) {
	var audioFiles int
	err := filepath.WalkDir(target, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path != target && entry.IsDir() && strings.HasPrefix(entry.Name(), ".") {
			return filepath.SkipDir
		}
		if !entry.IsDir() && strings.EqualFold(filepath.Ext(entry.Name()), ".mp3") &&
			!strings.HasPrefix(entry.Name(), "._") {
			audioFiles++
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("validate cartridge output: %w", err)
	}
	if audioFiles == 0 {
		return 0, errors.New("cartridge validation failed: no MP3 audio files were created")
	}
	if mode == ModeDaisy {
		info, err := os.Stat(filepath.Join(target, "ncc.html"))
		if err != nil || info.Size() == 0 {
			return 0, errors.New("cartridge validation failed: DAISY ncc.html is missing or empty")
		}
	}
	return audioFiles, nil
}

func estimatedOutputBytes(request Request) int64 {
	if request.Metadata.Duration <= 0 {
		return 0
	}
	kbps := int64(64)
	if request.Mode == ModeDaisy {
		kbps = 56
	}
	bytes := float64(request.Metadata.Duration) / float64(time.Second) * float64(kbps*1000) / 8
	return int64(bytes * 1.2)
}

func freeBytes(path string) (int64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	return int64(stat.Bavail) * int64(stat.Bsize), nil
}

func (r *Runner) buildLibrary(ctx context.Context, request Request, target, work string, send func(Event)) error {
	folder := safeName(request.Book.Name, 40) + "-" + shortHash(request.Book.Name, request.Book.SizeBytes)
	output := filepath.Join(target, "audio+podcasts", folder)
	if err := os.MkdirAll(output, 0o755); err != nil {
		return fmt.Errorf("create Library Mode output: %w", err)
	}
	send(Event{Phase: "convert", Message: "Converting and splitting into two-hour segments", Percent: 0.25})
	args := concatFilterArgs(request.Book.Files, "22050", "64k")
	args = append(args,
		"-f", "segment",
		"-segment_time", "7200",
		"-segment_start_number", "1",
		"-segment_format", "mp3",
		"-reset_timestamps", "1",
		"-y", filepath.Join(output, segmentPattern(request.Metadata.Duration, 2*time.Hour)+".mp3"),
	)
	if err := r.command(ctx, send, "ffmpeg", args...); err != nil {
		return fmt.Errorf("build Library Mode audio: %w", err)
	}
	send(Event{Phase: "copy", Message: "Library Mode cartridge created", Percent: 0.88})
	return nil
}

func (r *Runner) buildDaisy(ctx context.Context, request Request, target, work string, send func(Event)) error {
	makeDaisy := filepath.Join(request.Repository, "tools", "MakeDaisy", "makedaisy")
	if _, err := os.Stat(makeDaisy); err != nil {
		return fmt.Errorf("MakeDaisy unavailable; run ./setup.sh: %w", err)
	}
	source := filepath.Join(work, "Source")
	output := filepath.Join(work, "Daisy")
	if err := os.MkdirAll(source, 0o755); err != nil {
		return err
	}
	if err := writeDaisyMetadata(work, request); err != nil {
		return err
	}

	send(Event{Phase: "source", Message: "Preparing DAISY source files", Percent: 0.22})
	if len(request.Book.Files) > 1 {
		pattern := "%02d"
		if len(request.Book.Files) >= 100 {
			pattern = "%03d"
		}
		for i, file := range request.Book.Files {
			dest := filepath.Join(source, fmt.Sprintf(pattern+".mp3", i+1))
			if strings.EqualFold(filepath.Ext(file), ".mp3") {
				if err := copyFile(file, dest); err != nil {
					return err
				}
				continue
			}
			if err := r.command(ctx, send, "ffmpeg", "-i", file, "-ar", "22050", "-ac", "1",
				"-b:a", "64k", "-f", "mp3", "-map_metadata", "0", "-id3v2_version", "3", "-y", dest); err != nil {
				return fmt.Errorf("convert DAISY source %q: %w", filepath.Base(file), err)
			}
		}
	} else {
		args := daisySourceArgs(request.Book.Files)
		chapters := sourceChapters(ctx, request.Book.Files[0])
		pattern := segmentPattern(request.Metadata.Duration, time.Hour)
		if len(chapters) >= 100 {
			pattern = "%03d"
		}
		if len(chapters) >= 2 {
			var times []string
			for _, chapter := range chapters[1:] {
				times = append(times, strconv.FormatFloat(chapter.Start, 'f', 3, 64))
			}
			args = append(args, "-f", "segment", "-segment_times", strings.Join(times, ","),
				"-segment_start_number", "1", "-segment_format", "mp3",
				"-reset_timestamps", "1", "-y", filepath.Join(source, pattern+".mp3"))
		} else {
			args = append(args, "-f", "segment", "-segment_time", "3600",
				"-segment_start_number", "1", "-segment_format", "mp3",
				"-reset_timestamps", "1", "-y", filepath.Join(source, pattern+".mp3"))
		}
		if err := r.command(ctx, send, "ffmpeg", args...); err != nil {
			return fmt.Errorf("prepare DAISY source: %w", err)
		}
		for i, chapter := range chapters {
			title := strings.TrimSpace(strings.ReplaceAll(chapter.Title, ",", ";"))
			if title == "" {
				title = fmt.Sprintf("Chapter %02d", i+1)
			}
			path := filepath.Join(source, fmt.Sprintf(pattern+".txt", i+1))
			if err := os.WriteFile(path, []byte("Title: "+title+"\n"), 0o644); err != nil {
				return err
			}
		}
	}

	send(Event{Phase: "daisy", Message: "Building DAISY 2.02 package", Percent: 0.52})
	if err := r.commandInDir(ctx, send, work, "python3", makeDaisy, "-it", "-sf", "Source", "Daisy"); err != nil {
		return fmt.Errorf("MakeDaisy failed: %w", err)
	}
	if _, err := os.Stat(filepath.Join(output, "ncc.html")); err != nil {
		return errors.New("MakeDaisy did not produce ncc.html")
	}

	send(Event{Phase: "copy", Message: "Copying DAISY package to USB root", Percent: 0.85})
	entries, err := os.ReadDir(output)
	if err != nil {
		return err
	}
	partition := partitionID(request.Device)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		updated, err := r.copyToTarget(ctx, filepath.Join(output, entry.Name()), target, entry.Name(), partition, send)
		if err != nil {
			return err
		}
		target = updated
	}
	return nil
}

// copyToTarget copies one file onto the USB volume, retrying transient device
// failures. A flaky stick can briefly drop off the bus mid-write (macOS
// reports "device not configured", i.e. ENXIO); a long DAISY build should not
// be thrown away over a momentary blip. Between attempts it syncs, asks
// diskutil to remount the partition, and re-resolves the mount point in case
// macOS brought the volume back at a different path. It returns the mount
// point to keep using. A truly vanished stick still fails after the retries.
func (r *Runner) copyToTarget(ctx context.Context, source, targetDir, name, partition string, send func(Event)) (string, error) {
	const attempts = 4
	var err error
	for attempt := 1; attempt <= attempts; attempt++ {
		if err = copyFile(source, filepath.Join(targetDir, name)); err == nil {
			return targetDir, nil
		}
		if !isTransientDeviceError(err) || attempt == attempts {
			return targetDir, err
		}
		send(Event{Phase: "copy", Message: fmt.Sprintf(
			"USB write of %s failed (%v); re-checking the volume and retrying (%d/%d)",
			name, err, attempt, attempts)})
		_ = r.commandQuiet(ctx, "sync")
		_ = r.commandQuiet(ctx, "diskutil", "mount", partition)
		select {
		case <-ctx.Done():
			return targetDir, ctx.Err()
		case <-time.After(2 * time.Second):
		}
		if point, mErr := device.MountPoint(ctx, commandOutput{r.Commands}, partition); mErr == nil && point != "" {
			if info, sErr := os.Stat(point); sErr == nil && info.IsDir() {
				targetDir = point
			}
		}
	}
	return targetDir, err
}

// isTransientDeviceError reports whether a copy failed because the volume
// dropped off the bus rather than because of bad data or a full disk.
func isTransientDeviceError(err error) bool {
	return errors.Is(err, syscall.ENXIO) || errors.Is(err, syscall.EIO)
}

// partitionID is the volume slice to mount for a target device, e.g. disk8s1.
func partitionID(d device.Device) string {
	if d.VolumeID != "" {
		return d.VolumeID
	}
	return d.Identifier + "s1"
}

func (r *Runner) command(ctx context.Context, send func(Event), name string, args ...string) error {
	return r.commandInDir(ctx, send, "", name, args...)
}

func (r *Runner) commandQuiet(ctx context.Context, name string, args ...string) error {
	return r.Commands.Run(ctx, io.Discard, name, args...)
}

func (r *Runner) commandInDir(ctx context.Context, send func(Event), dir, name string, args ...string) error {
	if dir == "" {
		var buffer syncBuffer
		err := r.Commands.Run(ctx, &buffer, name, args...)
		for _, line := range buffer.Lines() {
			send(Event{Phase: "log", Message: line})
		}
		return err
	}
	if _, ok := r.Commands.(ExecCommandRunner); !ok {
		return r.command(ctx, send, name, args...)
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	pipe, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	cmd.Stdout = cmd.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	scanner := bufio.NewScanner(pipe)
	for scanner.Scan() {
		send(Event{Phase: "log", Message: scanner.Text()})
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return cmd.Wait()
}

func concatFilterArgs(files []string, sampleRate, bitrate string) []string {
	args := []string{"-hide_banner", "-loglevel", "error"}
	for _, file := range files {
		args = append(args, "-i", file)
	}
	if len(files) == 1 {
		return append(args, "-map", "0:a", "-ar", sampleRate, "-ac", "1", "-b:a", bitrate)
	}
	var filter strings.Builder
	for i := range files {
		fmt.Fprintf(&filter, "[%d:a]aresample=%s,aformat=sample_fmts=s16:channel_layouts=mono[a%d];", i, sampleRate, i)
	}
	for i := range files {
		fmt.Fprintf(&filter, "[a%d]", i)
	}
	fmt.Fprintf(&filter, "concat=n=%d:v=0:a=1[out]", len(files))
	return append(args, "-filter_complex", filter.String(), "-map", "[out]", "-ar", sampleRate, "-ac", "1", "-b:a", bitrate)
}

func daisySourceArgs(files []string) []string {
	args := concatFilterArgs(files, "22050", "64k")
	// Segment files must decode independently. Otherwise an MP3 frame at a
	// chapter boundary can reference the previous segment's bit reservoir.
	return append(args, "-reservoir", "0")
}

func writeDaisyMetadata(dir string, request Request) error {
	meta := request.Metadata
	creator := firstNonEmpty(meta.Creator, "Unknown")
	narrator := firstNonEmpty(meta.Narrator, "Unknown")
	publisher := firstNonEmpty(meta.Publisher, "Private")
	content := fmt.Sprintf(
		"Identifier: %s\nTitle: %s\nCreator: %s\nPublisher: %s\nRights: Personal use\nSource publisher: %s\nSource edition: 1\nNarrator: %s\n",
		daisyUID(request), firstNonEmpty(meta.Title, request.Book.Name), creator, publisher, publisher, narrator,
	)
	if err := os.WriteFile(filepath.Join(dir, "daisy.txt"), []byte(content), 0o644); err != nil {
		return fmt.Errorf("write DAISY metadata: %w", err)
	}
	return nil
}

func daisyUID(request Request) string {
	value := fmt.Sprintf("%s|%s|%s|%d", request.Metadata.Title, request.Metadata.Creator, request.Book.Name, request.Book.SizeBytes)
	sum := sha1.Sum([]byte(value))
	return "urn:nlsbook:" + hex.EncodeToString(sum[:8])
}

func sourceChapters(ctx context.Context, source string) []chapter {
	type chapterJSON struct {
		StartTime string            `json:"start_time"`
		Tags      map[string]string `json:"tags"`
	}
	type outputJSON struct {
		Chapters []chapterJSON `json:"chapters"`
	}
	data, err := exec.CommandContext(ctx, "ffprobe", "-v", "quiet", "-show_chapters", "-of", "json", source).Output()
	if err != nil {
		return nil
	}
	var output outputJSON
	if json.Unmarshal(data, &output) != nil {
		return nil
	}
	chapters := make([]chapter, 0, len(output.Chapters))
	for _, item := range output.Chapters {
		start, err := strconv.ParseFloat(item.StartTime, 64)
		if err != nil {
			continue
		}
		chapters = append(chapters, chapter{Start: start, Title: item.Tags["title"]})
	}
	return chapters
}

func clearTarget(target string) error {
	entries, err := os.ReadDir(target)
	if err != nil {
		return fmt.Errorf("read target volume: %w", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if err := os.RemoveAll(filepath.Join(target, entry.Name())); err != nil {
			return fmt.Errorf("clear target item %q: %w", entry.Name(), err)
		}
	}
	return nil
}

func copyFile(source, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.Create(destination)
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		output.Close()
		return err
	}
	return output.Close()
}

func (r *Runner) waitForMount(ctx context.Context, partition string) (string, error) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(15 * time.Second)
	defer timeout.Stop()
	requested := false
	for {
		point, err := device.MountPoint(ctx, commandOutput{r.Commands}, partition)
		if err == nil && point != "" {
			if info, statErr := os.Stat(point); statErr == nil && info.IsDir() {
				return point, nil
			}
		}
		if !requested {
			requested = true
			_ = r.commandQuiet(ctx, "diskutil", "mount", partition)
			continue
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-timeout.C:
			return "", fmt.Errorf("formatted volume %s did not mount", partition)
		case <-ticker.C:
		}
	}
}

// commandOutput adapts the build CommandRunner to device.Runner for
// diskutil queries that need captured output.
type commandOutput struct{ commands CommandRunner }

func (c commandOutput) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	var buffer syncBuffer
	err := c.commands.Run(ctx, &buffer, name, args...)
	return []byte(buffer.String()), err
}

func volumeName(name string) string {
	// FAT32 labels: 11 characters, uppercase only — diskutil rejects
	// lowercase letters when erasing as FAT32.
	name = strings.ToUpper(strings.ReplaceAll(safeName(name, 11), " ", "_"))
	if name == "" {
		return "NLSBOOK"
	}
	return name
}

func safeName(name string, limit int) string {
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune(" ._-", r) {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
		if b.Len() >= limit {
			break
		}
	}
	return strings.Trim(strings.Join(strings.Fields(b.String()), " "), " .")
}

// segmentPattern widens the segment index when a book will produce 100 or
// more segments, so the files still sort in play order on the player.
func segmentPattern(total, segment time.Duration) string {
	if segment > 0 && total >= 100*segment {
		return "%03d"
	}
	return "%02d"
}

func shortHash(name string, size int64) string {
	sum := sha1.Sum([]byte(name + "|" + strconv.FormatInt(size, 10)))
	return hex.EncodeToString(sum[:3])
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func (b *syncBuffer) Lines() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	value := strings.TrimSpace(b.b.String())
	if value == "" {
		return nil
	}
	return strings.Split(value, "\n")
}
