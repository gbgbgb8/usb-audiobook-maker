# NLS audiobook USB loader (macOS)

Load your own MP3/M4B/M4A audiobooks onto **2–4GB USB sticks** for the **NLS DS1** (and similar) talking-book player.

Tested on the **DS1** with **Library Mode** (plain MP3s + resume) and **DAISY 2.02 via [MakeDaisy](https://github.com/Memotech-Bill/MakeDaisy)**. Option **7** (DAISY) gives the best cartridge-like DS1 experience: time remaining + current-position resume. Generated DAISY chapter MP3s are independently decodable to avoid glitches at chapter boundaries.

The default interface is a Bubble Tea TUI with a compact recipe dashboard,
visual confirmation, phase progress, and a fast “next USB, same book” batch
workflow. The original linear terminal workflow remains available as the
screen-reader-friendly fallback.

The TUI shows phase, elapsed time, validation, and recent logs. Use `./bin/audiobooks --plain --verbose` for detailed output in the accessible workflow.

## Requirements

- **macOS** (uses `diskutil` to format sticks)
- **[Homebrew](https://brew.sh)**
- **Xcode Command Line Tools** (`xcode-select --install` if needed)
- A **2–4GB USB stick** (larger sticks often cause “Cartridge Error” on the DS1)
- Your own **DRM-free** audiobook files

## Quick install

```bash
git clone <your-repo-url> audiobooks
cd audiobooks
./setup.sh
```

Put books in `books/`, then:

```bash
./bin/audiobooks
```

Use `./bin/audiobooks --dry-run` to exercise the complete interface without
changing a USB drive. Use `./load_audiobook.sh` or
`./bin/audiobooks --plain` for the accessible plain-terminal workflow.

Full guide, options, and troubleshooting: **[about.md](about.md)**.

## What setup does

- Installs **ffmpeg**, **lame**, **mpg123** via Homebrew (if missing)
- Installs **Go** and builds `bin/audiobooks`
- Clones **MakeDaisy** into `tools/MakeDaisy/` (not stored in this repo)
- Builds **id3dump** and patches MakeDaisy for Python 3 + quieter encoding

The main script can still install some packages on its own, but **`./setup.sh` once** is the reliable path for a fresh Mac.

## Options (short)

| # | Use |
|---|-----|
| **7** | Default — best DS1 experience, DAISY time remaining + resume (slower; uses MakeDaisy) |
| **1** | Fast fallback — good when you do not need DAISY (`audio+podcasts/<book>/`) |

## License

This project is [MIT](LICENSE). MakeDaisy is MIT — see `tools/MakeDaisy/LICENCE.txt` after setup.

## Credits

- [MakeDaisy](https://github.com/Memotech-Bill/MakeDaisy) — DAISY 2.02 for hardware players  
- NLS / Library of Congress documentation  
