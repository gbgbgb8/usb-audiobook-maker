# NLS audiobook USB loader

Load DRM-free MP3/M4B/M4A audiobooks onto an NLS talking-book USB cartridge from your Mac terminal.

The Bubble Tea interface is designed for a simple repeatable workflow: put a
book in `books/`, insert a small USB stick, run `./bin/audiobooks`, and follow
the guided screens. It prepares a compatible MBR/FAT32 stick and builds a DAISY
2.02 cartridge with MakeDaisy, which gives the DS1 time remaining and
current-position resume.

The original `./load_audiobook.sh` prompts remain available as the accessible plain-terminal workflow and should work with macOS screen readers. The TUI can also launch this path with `./bin/audiobooks --plain`. Use `./load_audiobook.sh --verbose` for detailed encoder, disk, file-listing, and troubleshooting output.

**Verified on an NLS DS1** (standard player, not DA1) with **2–4GB USB sticks**:

| Option | Status on DS1 |
|--------|-----------------|
| **1** Library Mode | **Resumes** after power-off; independent positions across many sticks; multi-file books play as **one continuous book** (`audio+podcasts/<book>-<hash>/`). Announces “Audio Files.” |
| **2** Single `BOOK.mp3` | Works — quick troubleshoot / one-file test at USB root |
| **7** DAISY 2 for NLS (MakeDaisy) | **Best cartridge experience** on the DS1: **time remaining**, **resume** (“current position”), updated time after power-off. DAISY 2.02 at USB root. Slower build. |

**What to pick:** press **Enter** for **Option 7** if you want a stick that behaves like an NLS cartridge: time remaining, current-position resume, and one book per stick. Choose **Option 1** only when you want the fastest non-DAISY build.

Sticks **larger than 4GB often work** on the Mac but **2–4GB is the reliable sweet spot** for the DS1 (very large sticks can still cause Cartridge Error).

---

## Quick start

1. Put audiobooks in `books/` (see [Organizing your audiobooks](#organizing-your-audiobooks)).
2. Insert a **2–4GB USB stick** (4GB tested OK). Existing cartridge contents will be replaced.
3. Run `./bin/audiobooks` from the repo root.
4. Select a book and the detected physical USB target.
   If you add another audiobook while the TUI is open, press **r** on the book
   screen to refresh the list. Newly discovered books are selected automatically.
5. Keep **DAISY 2.02** selected, or choose the faster Library Mode.
6. Review the recipe, press **Enter**, select the write action, and confirm.
7. Follow build progress until the completion screen.
8. Wait for **Ejected — safe to unplug**, then insert into the DS1. Do not open the stick in Finder first.

**Eject tip:** Wait for the TUI to eject the stick (or use Finder **Eject**) before unplugging it. Moving the stick between Mac and player without ejecting can trigger macOS “not ejected properly” warnings even when the DS1 plays fine.

---

## Organizing your audiobooks

```
books/
├── The Madness of Crowds꞉ A Novel.mp3               ← single-file
└── Louise Penny - A Rule Against Murder/             ← multi-file (one subfolder per book)
    ├── Louise Penny - A Rule Against Murder [1-2].mp3
    └── Louise Penny - A Rule Against Murder [2-2].mp3
```

- **Single-file:** loose `*.mp3` / `*.m4b` / `*.m4a` in `books/`.
- **Multi-file:** subfolder under `books/`; name parts so they **sort in play order** (`[1-2]`, `01`, `02`, …). The loader uses that sorted order.
- Folder names are used verbatim as the book name, so dots are fine (e.g. `J.R.R. Tolkien - The Hobbit/`).

**Option 1** joins multi-file parts into one continuous audiobook, then segments. **Option 7** keeps **one DAISY section per part** (no join) — good for already-split downloads. Multi-file MP3s are copied into the DAISY source; multi-file M4A/M4B parts are converted to MP3 first.

---

## What the loader does

- Lists books in `books/` (newest first).
- Keeps compatible **MBR + FAT32** sticks as-is; formats incompatible targets, strips macOS junk, disables Spotlight, and **ejects**.
- **Option 1:** FFmpeg → 22 kHz mono 64 kb/s, 2-hour segments, `audio+podcasts/<book>-<hash>/`.
- **Option 7:** [MakeDaisy](https://github.com/Memotech-Bill/MakeDaisy) → DAISY 2.02 (`ncc.html`, SMIL, MP3s) at **USB root**.

---

## Prerequisites

- **macOS** with **Homebrew** and **Xcode Command Line Tools**
- **2–4GB USB stick** for the DS1

**One-time setup** (installs Go, ffmpeg, lame, mpg123, clones MakeDaisy, builds id3dump and the TUI):

```bash
git clone <your-repo-url> audiobooks
cd audiobooks
./setup.sh
```

| Tool | How you get it |
|------|----------------|
| Go | `setup.sh` (builds `bin/audiobooks`) |
| FFmpeg | `setup.sh` or auto on first `./load_audiobook.sh` run |
| LAME / mpg123 | `setup.sh` (Option 7) |
| MakeDaisy | `setup.sh` clones into `tools/MakeDaisy/` (not committed to git) |

```bash
./bin/audiobooks
```

Safe interface test without touching the USB:

```bash
./bin/audiobooks --dry-run
```

---

## Processing options

The TUI presents the two verified everyday choices: **Option 7** (default) and
**Option 1**. The accessible `load_audiobook.sh` workflow retains all seven
legacy and troubleshooting modes.

| # | Name | When to use | Output on stick |
|---|------|-------------|-----------------|
| **1** | **Library Mode** | Fastest non-DAISY build | `audio+podcasts/<book>-<hash>/01.mp3` … |
| **2** | **Single File** | Quick test | `BOOK.mp3` at root |
| **3–5** | Compatibility variants | Alternate bitrates/segment lengths | Under `audio+podcasts/<book>-<hash>/` |
| **6** | **Copy original** | No re-encode (may fail on DS1) | Original file at root |
| **7** | **DAISY 2 for NLS** (default) | **Recommended listening experience** | `ncc.html`, `01.mp3` … at **root** (MakeDaisy) |

### Option 7 (MakeDaisy) details

- Metadata from tags (editable before build); multipart books prefer the album tag so a part marker such as `[1-2]` does not become the book title. The stable `urn:nlsbook:…` identifier is based on title, creator, selected book name, and total source size.
- **Single-file:** embedded chapters when present, else **1-hour** segments. Each
  generated MP3 is independently decodable, avoiding bit-reservoir errors or
  missing audio at chapter boundaries.
- **Multi-file:** one section per part, filename order (`-it -sf` in MakeDaisy). MP3 parts are copied; M4A/M4B parts are converted before MakeDaisy.
- Audio: **56 kb/s mono 44.1 kHz** per section. Preflight checks stick and temp disk (~500–600 MB for a ~20-hour book).
- Slower than Option 1 because MakeDaisy re-encodes each section, but it gives the best DS1 listening experience.

---

## Option 7 on the DS1 (verified)

- **Time remaining** at start (e.g. “20 hours”, “10 minutes”).
- After power-off and reinsert: **“current position”**, brief replay, then **time remaining** with the **correct reduced** time.
- **Play / FF / RW** work. No bookmark button on the **DS1** (DA1 has bookmarks).

Verified full-book tests:

- *Louise Penny — A Rule Against Murder* (2 parts, ~11 hours) on a **4GB** stick built with the Bubble Tea workflow — package validation and automatic eject completed; the DS1 played both sections in order, announced time remaining, and resumed after power-off/reinsert.
- *Louise Penny — The Cruelest Month* (10 parts) on a **3.9GB** stick via Option 7 — confirmed playing perfectly on the DS1.
- June 2026: full TUI workflow (format → DAISY build → eject) re-verified end-to-end on a **2GB** stick after a robustness pass (safer post-format mount detection, FAT32 label handling, and plain-mode script fixes) — the DS1 played the book normally.

---

## Resume & multiple sticks

| Mode | Resume on DS1 | Multi-stick library |
|------|---------------|---------------------|
| **Option 1** | Yes — per `audio+podcasts/<book>-<hash>/` folder | Yes — verified across 2GB + 4GB sticks, no factory reset |
| **Option 7** | Yes — per unique DAISY book id at USB root | One book per stick; recommended DS1 cartridge mode |

**Option 1 multi-file:** 2-part and 53-part books joined seamlessly (e.g. *Memory Man* → 7 two-hour segments on one stick).

---

## USB sticks

| Do | Don't |
|----|--------|
| **2–4GB** (4GB tested) | 32GB+ often **Cartridge Error** on DS1 |
| TUI/script **format + eject** | Browse stick in Finder before first play |
| One book per stick | Multiple books on one stick |

The loader replaces the cartridge contents. The TUI shows only physical
external USB disks and uses a visual confirmation with **Cancel selected by
default**. Compatible MBR/FAT32 sticks keep their format automatically;
incompatible sticks default to **Erase + Write**.

---

## Typical TUI run

```bash
./bin/audiobooks
```

The guided screens cover book selection, physical USB selection, a compact
recipe dashboard, optional metadata editing, visual confirmation, phase
progress, output validation, and eject.

After a successful build, press **n** to write the same audiobook to another
USB stick without reselecting the book or settings. The TUI waits for the next
stick and detects it automatically.

### Accessible plain-terminal run

```bash
./load_audiobook.sh
```

Default Option **7** (MakeDaisy) on a multi-part book:

```
Book # [7]:
📚 Multi-part book: 10 files
Target # [1]:
Erase entire stick and format as fresh FAT32? (Y/n):
1/5 Formatting USB as FAT32...
✅ Formatted: MKDAISY
Choice [1/2/3/4/5/6/7] [7]:
2/5 Preparing USB layout...
🔧 DAISY 2.02 for NLS (time remaining + resume)…
3/5 Preparing DAISY source...
4/5 Building DAISY book (this may take a while)...
✅ DAISY package built (10 audio file(s))
5/5 Copying DAISY book to USB...
Cleaning and ejecting USB...
✅ Done. Safe to unplug.
Mode: DAISY 2.02, Option 7
Files: 10 audio file(s) + ncc.html
```

For debugging or detailed logs:

```bash
./load_audiobook.sh --verbose
```

---

## Troubleshooting

### Cartridge error

1. **2–4GB stick**, fresh **FAT32** format.
2. **Option 7** first. If testing hardware compatibility, try **Option 1** or **2**.
3. **Eject** before unplugging from Mac.

### Build fails mid-copy with “device not configured” or “input/output error”

The USB volume dropped off the bus while the package was being written. The TUI
now retries each file a few times (re-syncing and remounting between attempts),
so a momentary blip recovers on its own. If the build still fails and the stick
**disappears from `diskutil list external physical`**, the device has fully
disconnected — that is failing hardware or a bad connection, not the loader.
Plug the stick **directly into a Mac port** (no hub or dock) and retry, or use a
different **2–4GB** stick. A stick that drops off *earlier on each attempt* is
dying — replace it.

### Playback

| Issue | Try |
|-------|-----|
| “Audio Files” but want time remaining | **Option 7** |
| Restarts at 0:00 after power-off | **Option 7** or **Option 1** (with this script) |
| macOS “not ejected properly” | Eject in Finder before unplugging; harmless if DS1 plays OK |

### Option 7 on the Mac

- Needs `tools/MakeDaisy`; first run builds `id3dump` (needs **mpg123**).
- Long books: many **lame** passes — allow time and temp disk space.
- If a USB controller disappears immediately after formatting, unplug and
  reinsert the stick, restart the TUI, and toggle formatting off with `f`.
  The freshly formatted FAT32 volume can then be used without erasing again.

---

## FAQ

**Option 1 vs Option 7?**  
**7** for the best DS1 experience (time remaining + resume). **1** for speed when you do not need DAISY.

**One book per stick?**  
Yes — most reliable.

**Will 10 sticks confuse resume (Option 1)?**  
No — unique `audio+podcasts/<book>-<hash>/` per stick.

**Will 10 sticks confuse resume (Option 7)?**  
They should not. Option 7 writes a unique DAISY book id using the title, creator, selected book name, and total source size.

**Will the DS1 read my tags aloud?**  
You usually hear the **recording** (publisher intro, etc.), not a separate spoken title from ID3.

---

## License

MIT — no warranty.

---

## Credits

- NLS / Library of Congress player documentation
- [MakeDaisy](https://github.com/Memotech-Bill/MakeDaisy) (Memotech Bill) — Option 7 DAISY 2.02
- Homebrew, FFmpeg, LAME, mpg123
- **NLS DS1** verified: Options **1**, **2**, **7**; multi-file Option 1; MakeDaisy Option 7 resume on 2GB and 4GB sticks (including full *Louise Penny* 2-part and 10-part builds)
