#!/usr/bin/env bash
# Copy or convert an audiobook onto an NLS cartridge – firmware-friendly
set -euo pipefail; shopt -s nullglob
export COPYFILE_DISABLE=1

VERBOSE=0
usage() {
    cat <<'EOF'
Usage: ./load_audiobook.sh [--verbose|-v] [--help|-h]

Default mode keeps output short and progress-focused.
Use --verbose for detailed command output and troubleshooting details.
EOF
}

while [[ $# -gt 0 ]]; do
    case $1 in
        -v|--verbose)
            VERBOSE=1
            shift
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            echo "Unknown option: $1"
            usage
            exit 1
            ;;
    esac
done

detail() {
    # The explicit return keeps a quiet detail from tripping set -e.
    [[ $VERBOSE -eq 1 ]] || return 0
    printf '%s\n' "$*"
}

run_logged() {
    local desc=$1 log rc
    shift
    if [[ $VERBOSE -eq 1 ]]; then
        "$@"
        return $?
    fi
    log=$(mktemp)
    if "$@" >"$log" 2>&1; then
        rm -f "$log"
        return 0
    fi
    rc=$?
    echo "❌ $desc failed."
    echo "Last output:"
    tail -40 "$log" 2>/dev/null || true
    rm -f "$log"
    return $rc
}

############################################################ 0. deps
if [[ $(uname -s) != Darwin ]]; then
    echo "❌ macOS only (diskutil USB formatting)."
    exit 1
fi
command -v brew >/dev/null || {
    echo "❌ Homebrew required. Install from https://brew.sh then run: ./setup.sh"
    exit 1
}
command -v ffmpeg >/dev/null || { echo "Installing ffmpeg…"; brew install ffmpeg; }
command -v diskutil >/dev/null || { echo "❌ diskutil not found."; exit 1; }
command -v ffprobe >/dev/null || { echo "❌ ffprobe missing (brew install ffmpeg or run ./setup.sh)"; exit 1; }
SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)

ffprobe_tag() {
    ffprobe -v quiet -show_entries "format_tags=$2" \
        -of default=nw=1:nokey=1 "$1" 2>/dev/null | head -n1 || true
}

# ensure_makedaisy — Option 7 builds DAISY 2.02 via tools/MakeDaisy (hardware-player oriented).
ensure_makedaisy() {
    local root prefix
    command -v python3 >/dev/null || {
        echo "❌ python3 required for Option 7."
        exit 1
    }
    if ! xcode-select -p >/dev/null 2>&1; then
        echo "❌ Xcode Command Line Tools required (gcc builds id3dump)."
        echo "   Run: xcode-select --install"
        echo "   Or run ./setup.sh from the repo root."
        exit 1
    fi
    command -v lame >/dev/null || { echo "Installing lame…"; brew install lame; }
    root="$SCRIPT_DIR/tools/MakeDaisy"
    [[ -f "$root/makedaisy" ]] || {
        echo "❌ MakeDaisy not installed."
        echo "   Run once from this folder: ./setup.sh"
        exit 1
    }
    MAKEDAISY="$root/makedaisy"
    ID3DUMP="$root/id3dump"
    if [[ ! -x $ID3DUMP ]]; then
        echo "🔧 Building id3dump (run ./setup.sh to do this ahead of time)…"
        brew list mpg123 >/dev/null 2>&1 || brew install mpg123
        prefix=$(brew --prefix mpg123 2>/dev/null)
        gcc -o "$ID3DUMP" "$root/id3dump.c" -I"$prefix/include" -L"$prefix/lib" -lmpg123 \
            || { echo "❌ Failed to build id3dump — run ./setup.sh"; exit 1; }
    fi
    echo "✅ MakeDaisy ready"
}

# write_daisy_txt DIR — MakeDaisy metadata file (daisy.txt) in DIR.
write_daisy_txt() {
    local dir=$1
    cat > "$dir/daisy.txt" <<EOF
Identifier: $DAISY_UID
Title: $DAISY_TITLE
Creator: ${DAISY_CREATOR:-Unknown}
Publisher: $DAISY_PUBLISHER
Rights: Personal use
Source publisher: $DAISY_PUBLISHER
Source edition: 1
Narrator: ${DAISY_READER:-Unknown}
EOF
}

extract_audiobook_metadata() {
    local src=$1
    local title album artist album_artist composer pub

    title=$(ffprobe_tag "$src" title)
    album=$(ffprobe_tag "$src" album)
    artist=$(ffprobe_tag "$src" artist)
    album_artist=$(ffprobe_tag "$src" album_artist)
    composer=$(ffprobe_tag "$src" composer)
    pub=$(ffprobe_tag "$src" publisher)

    DAISY_TITLE=${title:-${album:-$BASENAME}}
    DAISY_CREATOR=${album_artist:-${artist:-${composer:-}}}
    if [[ -n $artist && -n $album_artist && $artist != "$album_artist" ]]; then
        DAISY_READER=$artist
    elif [[ -n $composer && $composer != "$DAISY_CREATOR" ]]; then
        DAISY_READER=$composer
    else
        DAISY_READER=
    fi
    DAISY_PUBLISHER=${pub:-Private}

    echo ""
    if [[ $VERBOSE -eq 1 ]]; then
        echo "📖 Metadata from source tags:"
        echo "   Title:     $DAISY_TITLE"
        echo "   Creator:   ${DAISY_CREATOR:-(none)}"
        echo "   Reader:    ${DAISY_READER:-(none)}"
        echo "   Publisher: $DAISY_PUBLISHER"
    else
        echo "📖 Metadata: $DAISY_TITLE — ${DAISY_CREATOR:-Unknown}"
    fi
    local t c r
    read -erp "Title [$DAISY_TITLE]: " t || true
    DAISY_TITLE=${t:-$DAISY_TITLE}
    read -erp "Creator (author) [${DAISY_CREATOR:-}]: " c || true
    [[ -n $c ]] && DAISY_CREATOR=$c
    read -erp "Reader (narrator) [${DAISY_READER:-}]: " r || true
    [[ -n $r ]] && DAISY_READER=$r
    return 0
}

# read_source_chapters SRC
# Prints one line per chapter: "<start_seconds>\t<title>". Empty output if none.
# CSV columns from ffprobe -show_chapters: id,time_base,start,start_time,end,end_time,title
read_source_chapters() {
    ffprobe -v quiet -show_chapters -of csv=p=0 "$1" 2>/dev/null \
        | awk -F',' 'NF>=7 {
            start=$4;
            title="";
            for (i=7; i<=NF; i++) title = title (i>7 ? "," : "") $i;
            gsub(/^"|"$/, "", title);
            printf "%s\t%s\n", start, title;
        }' || true
}

# ffmpeg_concat_convert OUT_MP3 SAMPLE_RATE BITRATE
# Converts the book in the global SRC_FILES[] array to a single mono MP3 at OUT_MP3.
# One source file -> straight convert; multiple -> concatenate (in array order) in a
# single pass via the concat filter (each part normalized first so differing inputs
# join cleanly). Returns ffmpeg's exit status.
ffmpeg_concat_convert() {
    local out=$1 sr=$2 br=$3
    local n=${#SRC_FILES[@]} i inputs=() filter=""
    for ((i = 0; i < n; i++)); do inputs+=(-i "${SRC_FILES[$i]}"); done
    if [[ $n -le 1 ]]; then
        ffmpeg "${inputs[@]}" -ar "$sr" -ac 1 -ab "$br" -f mp3 \
            -map_metadata 0 -id3v2_version 3 \
            -y "$out" >/dev/null 2>&1
        return $?
    fi
    for ((i = 0; i < n; i++)); do
        filter+="[$i:a]aresample=$sr,aformat=sample_fmts=s16:channel_layouts=mono[a$i];"
    done
    for ((i = 0; i < n; i++)); do filter+="[a$i]"; done
    filter+="concat=n=$n:v=0:a=1[out]"
    ffmpeg "${inputs[@]}" -filter_complex "$filter" -map "[out]" \
        -ar "$sr" -ac 1 -ab "$br" -f mp3 -id3v2_version 3 \
        -y "$out" >/dev/null 2>&1
}

# concat_copy OUT
# Joins SRC_FILES[] into OUT via stream copy (concat demuxer) — fast, no re-encode.
# Works when the parts share a codec (typical for split MP3s). Returns nonzero on failure.
concat_copy() {
    local out=$1 listf f rc
    listf=$(mktemp)
    for f in "${SRC_FILES[@]}"; do
        printf "file '%s'\n" "${f//\'/\'\\\'\'}" >> "$listf"
    done
    ffmpeg -f concat -safe 0 -i "$listf" -c copy -y "$out" >/dev/null 2>&1
    rc=$?
    rm -f "$listf"
    return $rc
}

# create_nls_segments OUT_DIR [sample_rate] [bitrate] [segment_seconds] [cut_times_csv]
# Converts (and concatenates multi-part) the global SRC_FILES[] book, then writes
# 01.mp3, 02.mp3, … (dynamic width) in OUT_DIR; prints segment count to stdout.
# If cut_times_csv is non-empty, splits at those explicit times instead of fixed length.
create_nls_segments() {
    local out_dir=$1
    local sample_rate=${2:-22050} bitrate=${3:-64k} segment_length=${4:-7200}
    local cut_times=${5:-}
    local work part=1 file count width

    mkdir -p "$out_dir"
    work=$(mktemp -d)

    if ! ffmpeg_concat_convert "$work/converted.mp3" "$sample_rate" "$bitrate"; then
        rm -rf "$work"
        echo "❌ Error converting audio format" >&2
        return 1
    fi

    if [[ -n $cut_times ]]; then
        if ! ffmpeg -i "$work/converted.mp3" -f segment -segment_times "$cut_times" \
               -c copy -map 0 -segment_format mp3 \
               -y "$work/seg_%03d.mp3" >/dev/null 2>&1; then
            rm -rf "$work"
            echo "❌ Error splitting on chapter boundaries" >&2
            return 1
        fi
    else
        if ! ffmpeg -i "$work/converted.mp3" -f segment -segment_time "$segment_length" \
               -c copy -map 0 -segment_format mp3 \
               -y "$work/seg_%03d.mp3" >/dev/null 2>&1; then
            rm -rf "$work"
            echo "❌ Error splitting into segments" >&2
            return 1
        fi
    fi

    count=0
    for file in "$work"/seg_*.mp3; do
        [[ -f $file ]] && count=$((count + 1))
    done
    width=2
    [[ $count -ge 100 ]] && width=3

    for file in "$work"/seg_*.mp3; do
        [[ -f $file ]] || continue
        cp "$file" "$out_dir/$(printf "%0${width}d.mp3" $part)"
        part=$((part + 1))
    done
    rm -rf "$work"
    echo $((part - 1))
}

# Free bytes on the filesystem backing a path.
fs_free_bytes() {
    df -Pk "$1" 2>/dev/null | awk 'NR==2 {print $4*1024; exit}'
}

# Rough DAISY audio size in bytes for the whole book (sum of SRC_FILES[] durations)
# at the given kbps (default 64), +20% overhead.
estimate_daisy_bytes() {
    local kbps=${1:-64} total=0 dur f
    for f in "${SRC_FILES[@]}"; do
        dur=$(ffprobe -v quiet -show_entries format=duration -of default=nw=1:nokey=1 "$f" 2>/dev/null || true)
        [[ -z $dur ]] && continue
        total=$(python3 -c "print($total + float('$dur'))" 2>/dev/null || echo "$total")
    done
    [[ $total == 0 ]] && { echo 0; return; }
    python3 -c "print(int($total * $kbps * 1000 / 8 * 1.2))" 2>/dev/null || echo 0
}

# Build a filesystem-safe, unique folder name for an audiobook. Each stick gets
# its own uniquely-named folder under audio+podcasts/ so the DS1 treats every
# book as distinct and keeps an INDEPENDENT resume position (no cross-stick
# position bleed, and no factory reset needed between books). The short hash of
# name+total-size guarantees two similarly-named books never collide.
make_book_folder_name() {
    local base=$1 sz=${2:-0} clean hash
    clean=$(printf '%s' "$base" | tr -c 'A-Za-z0-9 ._-' '_')
    clean=$(printf '%s' "$clean" | tr -s '_' | tr -s ' ')
    clean=${clean# }; clean=${clean% }; clean=${clean%.}
    [[ -z $clean ]] && clean=Audiobook
    clean=${clean:0:40}
    hash=$(printf '%s|%s' "$base" "$sz" | shasum 2>/dev/null | cut -c1-6)
    [[ -z $hash ]] && hash=000000
    printf '%s-%s' "$clean" "$hash"
}

# Build a stable DAISY id that is unique enough for a shelf of USB sticks.
# Metadata alone can collide, so include the selected book name and total bytes.
make_daisy_uid() {
    local title=$1 creator=$2 base=$3 sz=${4:-0} hash
    hash=$(printf '%s|%s|%s|%s' "$title" "$creator" "$base" "$sz" | shasum 2>/dev/null | cut -c1-16)
    [[ -z $hash ]] && hash=$(printf '%s|%s|%s|%s' "$title" "$creator" "$base" "$sz" | cksum | cut -d' ' -f1)
    [[ -z $hash ]] && hash=0000000000000000
    printf 'urn:nlsbook:%s' "$hash"
}

############################################################ 1. pick audiobook
# Sources live in books/. A "book" is either a loose audio file in books/, or a
# per-book subfolder holding its parts (multi-file books). Subfolders are
# concatenated, in filename order, into one continuous book.
BOOKS_DIR="$SCRIPT_DIR/books"
[[ -d "$BOOKS_DIR" ]] || BOOKS_DIR="$SCRIPT_DIR"   # fallback: loose files in repo root

book_labels=()
book_kinds=()
book_paths=()

# Loose audio files directly in books/
while IFS= read -r -d '' f; do
    book_paths+=("$f"); book_kinds+=("file"); book_labels+=("$(basename "$f")")
done < <(find "$BOOKS_DIR" -mindepth 1 -maxdepth 1 -type f \
    \( -iname '*.mp3' -o -iname '*.m4b' -o -iname '*.m4a' \) -print0 2>/dev/null)

# Subfolders that contain at least one audio file (multi-part books)
while IFS= read -r -d '' d; do
    first_audio=$(find "$d" -maxdepth 1 -type f \
        \( -iname '*.mp3' -o -iname '*.m4b' -o -iname '*.m4a' \) -print -quit 2>/dev/null)
    if [[ -n $first_audio ]]; then
        cnt=$(find "$d" -maxdepth 1 -type f \
            \( -iname '*.mp3' -o -iname '*.m4b' -o -iname '*.m4a' \) 2>/dev/null | wc -l | tr -d '[:space:]')
        book_paths+=("$d"); book_kinds+=("dir"); book_labels+=("$(basename "$d")/  ($cnt parts)")
    fi
done < <(find "$BOOKS_DIR" -mindepth 1 -maxdepth 1 -type d ! -name '.*' -print0 2>/dev/null)

[[ ${#book_paths[@]} -eq 0 ]] && { echo "No audiobooks found in $BOOKS_DIR"; exit 1; }

# Default to the most recently modified book.
newest_idx=0; newest_m=-1
for i in "${!book_paths[@]}"; do
    m=$(stat -f '%m' "${book_paths[$i]}" 2>/dev/null || echo 0)
    [[ $m -gt $newest_m ]] && { newest_m=$m; newest_idx=$i; }
done
default_choice=$((newest_idx + 1))

echo "Audiobooks in ${BOOKS_DIR#"$SCRIPT_DIR"/}:"
for i in "${!book_labels[@]}"; do
    marker=""; [[ $i -eq $newest_idx ]] && marker="  (newest)"
    printf "%d) %s%s\n" "$((i + 1))" "${book_labels[$i]}" "$marker"
done
read -erp "Book # [$default_choice]: " bchoice; bchoice=${bchoice:-$default_choice}
[[ $bchoice =~ ^[0-9]+$ ]] || { echo "Invalid choice"; exit 1; }
idx=$((bchoice - 1))
[[ $idx -ge 0 && $idx -lt ${#book_paths[@]} ]] || { echo "Invalid choice"; exit 1; }

SRC_FILES=()
if [[ ${book_kinds[$idx]} == file ]]; then
    SRC_FILES=("${book_paths[$idx]}")
    BASENAME=$(basename "${book_paths[$idx]}"); BASENAME=${BASENAME%.*}
else
    while IFS= read -r -d '' f; do SRC_FILES+=("$f"); done < <(
        find "${book_paths[$idx]}" -maxdepth 1 -type f \
            \( -iname '*.mp3' -o -iname '*.m4b' -o -iname '*.m4a' \) -print0 2>/dev/null | sort -z)
    BASENAME=$(basename "${book_paths[$idx]}")
fi
[[ ${#SRC_FILES[@]} -ge 1 ]] || { echo "No audio files for that book."; exit 1; }
SRC="${SRC_FILES[0]}"

TOTAL_BYTES=0
for f in "${SRC_FILES[@]}"; do
    b=$(stat -f%z "$f" 2>/dev/null || echo 0); TOTAL_BYTES=$((TOTAL_BYTES + b))
done

if [[ ${#SRC_FILES[@]} -gt 1 ]]; then
    echo "📚 Multi-part book: ${#SRC_FILES[@]} files"
    if [[ $VERBOSE -eq 1 ]]; then
        echo "   Files will be processed in this order:"
        n=1; for f in "${SRC_FILES[@]}"; do printf "   %d. %s\n" "$n" "$(basename "$f")"; n=$((n + 1)); done
    fi
fi

############################################################ 2. pick target volume
volume_is_selectable() {
    local vol=$1 base info
    [[ -d "$vol" && -r "$vol" ]] || return 1
    base=$(basename "$vol")
    case "$base" in
        Macintosh\ HD|Time\ Machine|com.apple.TimeMachine*|.timemachine) return 1 ;;
    esac
    info=$(diskutil info "$vol" 2>/dev/null) || return 1
    # Skip Time Machine backup images and network mounts (not USB sticks)
    echo "$info" | grep -qE 'Protocol: *(Disk Image|Network)' && return 1
    # USB / external drives (many report Removable Media: Fixed)
    echo "$info" | grep -qE 'Protocol: *USB|Device Location: *External' && return 0
    echo "$info" | grep -qE 'Removable Media: *Removable|Internal: *No' && return 0
    return 1
}

all_volumes=()
while IFS= read -r -d '' vol; do
    [[ -d "$vol" ]] && all_volumes+=("$vol")
done < <(
    find /Volumes -mindepth 1 -maxdepth 1 \
        ! -name 'Macintosh HD' \
        ! -name 'Time Machine' \
        ! -name 'com.apple.TimeMachine' \
        -print0 2>/dev/null
)

volumes=()
for vol in "${all_volumes[@]}"; do
    volume_is_selectable "$vol" && volumes+=("$vol")
done

echo "Mounted volumes (USB / external):"
if [[ ${#volumes[@]} -eq 0 ]]; then
    echo "❌ No USB/external removable volumes found. Please insert a USB drive."
    exit 1
fi

for i in "${!volumes[@]}"; do
    echo "$((i+1))) ${volumes[i]}"
done

read -erp "Target # [1]: " choice
choice=${choice:-1}
[[ $choice =~ ^[0-9]+$ ]] || { echo "Invalid choice"; exit 1; }
[[ $choice -ge 1 && $choice -le ${#volumes[@]} ]] || { echo "Invalid choice"; exit 1; }
TARGET="${volumes[$((choice-1))]}"
[[ -d "$TARGET" ]] || { echo "Invalid target: $TARGET"; exit 1; }
mdutil -i off "$TARGET" >/dev/null 2>&1 || true

get_whole_disk() {
    local whole
    whole=$(diskutil info "$1" 2>/dev/null | awk -F': *' '/Part of Whole Disk:|Part of Whole:/ {print $2; exit}')
    whole=${whole// /}
    if [[ -n $whole ]]; then
        echo "$whole"
        return 0
    fi
    local dev
    dev=$(diskutil info "$1" 2>/dev/null | awk '/Device Identifier:/ {print $3; exit}')
    [[ -n $dev ]] && echo "${dev%s[0-9]*}"
}

fat32_volume_name() {
    local name
    name=$(basename "$1")
    # FAT32 labels: 11 characters, uppercase only (diskutil rejects lowercase).
    name=$(printf '%s' "$name" | tr '[:lower:]' '[:upper:]' | tr -c 'A-Z0-9_-' '_')
    name=${name:0:11}
    [[ -z $name ]] && name=NLSBOOK
    echo "$name"
}

format_usb_fat32() {
    local whole vol_name part mp i
    whole=$(get_whole_disk "$1")
    [[ -z $whole ]] && { echo "❌ Could not determine physical disk for $1"; exit 1; }
    vol_name=$(fat32_volume_name "$1")
    echo "1/5 Formatting USB as FAT32..."
    detail "   Disk: $whole"
    detail "   Volume name: $vol_name"
    run_logged "Formatting USB" diskutil eraseDisk FAT32 "$vol_name" MBRFormat "$whole"
    # Resolve the real mount point from diskutil; macOS appends a suffix to
    # /Volumes/<name> when another mounted volume already uses that name.
    part="${whole}s1"
    TARGET=
    for i in 1 2 3 4 5 6 7 8 9 10; do
        mp=$(diskutil info "$part" 2>/dev/null | sed -n 's/^ *Mount Point: *//p' | head -n1 || true)
        [[ -n $mp && -d $mp ]] && { TARGET=$mp; break; }
        sleep 1
    done
    [[ -n $TARGET ]] || { echo "❌ Format finished but the new volume did not mount"; exit 1; }
    drive_info=$(diskutil info "$TARGET" 2>/dev/null)
    echo "✅ Formatted: $vol_name"
}

volume_is_fat32() {
    echo "$1" | grep -qE "File System Personality: *MS-DOS|FAT32|MS-DOS FAT"
}

clean_usb_junk() {
    local t=$1
    dot_clean -m "$t" 2>/dev/null || true
    find "$t" -name '.DS_Store' -delete 2>/dev/null || true
    find "$t" -name '._*' -delete 2>/dev/null || true
    rm -rf "$t/.Spotlight-V100" "$t/.fseventsd" "$t/.Trashes" 2>/dev/null || true
    rm -f "$t/.metadata_never_index" 2>/dev/null || true
    xattr -cr "$t" 2>/dev/null || true
}

eject_target_volume() {
    local target=$1 whole
    sync
    sleep 1
    if diskutil eject "$target" >/dev/null 2>&1; then
        return 0
    fi
    whole=$(get_whole_disk "$target")
    diskutil unmount "$target" >/dev/null 2>&1 || true
    [[ -n $whole ]] && diskutil eject "$whole" >/dev/null 2>&1 && return 0
    return 1
}

############################################################ 3. format check and fix
echo "🔍 Checking USB drive..."

if [[ ! -d "$TARGET" ]]; then
    echo "❌ Error: Target directory $TARGET not found or not accessible"
    exit 1
fi

if ! drive_info=$(diskutil info "$TARGET" 2>/dev/null); then
    echo "❌ Error: Cannot get drive information for $TARGET"
    echo "   This may indicate a permission issue or the drive is not properly mounted"
    exit 1
fi

echo "✅ Drive accessible: $TARGET"

physical_disk=$(get_whole_disk "$TARGET")
# Parse the exact byte count so MB-sized sticks are not mistaken for GB.
drive_size_bytes=$(diskutil info "$physical_disk" 2>/dev/null \
    | sed -n 's/.*Disk Size:.*(\([0-9][0-9]*\) Bytes.*/\1/p' | head -n1 || true)
drive_size_bytes=${drive_size_bytes:-0}
drive_size_gb=$(awk -v b="$drive_size_bytes" 'BEGIN {printf "%.1f", b / 1000000000}')
drive_size_int=$((drive_size_bytes / 1000000000))
if [[ $drive_size_int -gt 4 ]]; then
    echo "⚠️  WARNING: Drive is ${drive_size_gb}GB. USB sticks larger than 4GB usually will NOT work on NLS players."
    echo "   This script was tested and confirmed working with small 2GB USB sticks."
    read -erp "Continue anyway? (y/N): " big_drive_choice
    [[ $big_drive_choice =~ ^[Yy]$ ]] || { echo "Cancelled. Use a 2GB stick and run again."; exit 0; }
elif [[ $drive_size_int -ge 1 ]]; then
    echo "✅ Drive size: ${drive_size_gb}GB (good for NLS players)"
else
    echo "✅ Drive size check skipped"
fi

existing_files=0
if existing_mp3s=$(find "$TARGET" -maxdepth 2 -name "*.mp3" -not -path "*/.*" 2>/dev/null); then
    existing_files=$(grep -c . <<< "$existing_mp3s" || true)
fi

echo ""
echo "USB preparation:"
if volume_is_fat32 "$drive_info"; then
    echo "  Current format: FAT32 (NLS-compatible)"
else
    echo "  Current format: not FAT32 — NLS players require FAT32"
fi
if [[ $existing_files -gt 0 ]]; then
    echo "  Existing content: $existing_files MP3 file(s) on the stick"
else
    echo "  Existing content: none (empty or no MP3s)"
fi
echo "  Formatting erases the entire stick and gives a clean FAT32 volume for the player."

read -erp "Erase entire stick and format as fresh FAT32? (Y/n): " format_choice
format_choice=${format_choice:-Y}

if [[ $format_choice =~ ^[Yy]$ ]]; then
    format_usb_fat32 "$TARGET"
elif ! volume_is_fat32 "$drive_info"; then
    echo "⚠️  Continuing without FAT32 (may cause cartridge errors on the player)"
else
    echo "✅ Keeping current FAT32 volume"
fi

echo "🔄 Continuing to processing options..."

############################################################ 4. processing options
if [[ $VERBOSE -eq 1 ]]; then
    echo "Processing options:"
    echo "1) *Library Mode* 22 kHz mono 64 kb/s, 2-hour segments, unique audio+podcasts/<book> folder (fast)"
    echo "2) *Single File* BOOK.mp3 in USB root (quick test / troubleshoot)"
    echo "3) *NLS-Compatible* 44.1 kHz mono 96 kb/s, 60-min segments (subfolders; headphones)"
    echo "4) *Maximum Compatibility* 22 kHz mono 64 kb/s, 2-hour segments (subfolders)"
    echo "5) *Ultra-Compatible* 22 kHz mono 64 kb/s, 4-hour segments (very long books)"
    echo "6) Copy original (fastest; may not work on DS1)"
    echo "7) *DAISY 2 for NLS* — default; time remaining + resume (MakeDaisy; USB root)"
else
    echo "Processing default: Option 7 DAISY (time remaining + resume). Use --verbose to see all modes."
fi
read -erp "Choice [1/2/3/4/5/6/7] [7]: " mode
mode=${mode:-7}
[[ $mode =~ ^[1-7]$ ]] || { echo "Invalid choice"; exit 1; }

############################################################ 5. create proper folder structure
echo "2/5 Preparing USB layout..."

# Fresh slate: one book per stick, so clear all top-level content (keep macOS
# dotfiles). This also removes leftovers from a previous mode (e.g. old DAISY
# .smil/.opf/.ncx files) that could confuse the player.
find "$TARGET" -mindepth 1 -maxdepth 1 ! -name '.*' -exec rm -rf {} + 2>/dev/null || true

if [[ $mode == 2 || $mode == 6 || $mode == 7 ]]; then
    detail "📁 Using USB root (no subfolders)..."
    BOOK_DIR="$TARGET"
else
    # Each book lives in its own uniquely-named folder under audio+podcasts/ so
    # the player keeps a separate resume position per book across sticks.
    AUDIO_DIR="$TARGET/audio+podcasts"
    BOOK_FOLDER=$(make_book_folder_name "$BASENAME" "$TOTAL_BYTES")
    BOOK_DIR="$AUDIO_DIR/$BOOK_FOLDER"
    mkdir -p "$BOOK_DIR"
    detail "📁 Book folder: audio+podcasts/$BOOK_FOLDER"
fi

############################################################ 6. process based on mode
case $mode in
    1)
        echo "🔧 Library mode (22kHz mono 64kbps, 2-hour segments, audio+podcasts/<book> folder)..."
        TEMP_DIR=$(mktemp -d)
        trap 'rm -rf "$TEMP_DIR"' EXIT
        echo "🎵 Converting and splitting..."
        seg_count=$(create_nls_segments "$TEMP_DIR" 22050 64k 7200) || exit 1
        part=1
        for file in "$TEMP_DIR"/*.mp3; do
            [[ -f $file ]] || continue
            target_file="$BOOK_DIR/$(printf '%02d.mp3' $part)"
            printf "📁 Part %02d -> %s\n" $part "$(basename "$target_file")"
            COPYFILE_DISABLE=1 cp -X "$file" "$target_file"
            ((part++))
        done
        echo "✅ Created $((part - 1)) segments"
        ;;
    3|4|5)
        if [[ $mode == 3 ]]; then
            SAMPLE_RATE=44100
            BITRATE=96k
            SEGMENT_LENGTH=3600
            echo "🔧 NLS-compatible format (44.1kHz mono 96kbps, 60-min segments)..."
        elif [[ $mode == 4 ]]; then
            SAMPLE_RATE=22050
            BITRATE=64k
            SEGMENT_LENGTH=7200
            echo "🔧 Maximum compatibility format (22kHz mono 64kbps, 2-hour segments)..."
        else
            SAMPLE_RATE=22050
            BITRATE=64k
            SEGMENT_LENGTH=14400
            echo "🔧 Ultra-compatible format (22kHz mono 64kbps, 4-hour segments)..."
        fi

        TEMP_DIR=$(mktemp -d)
        trap 'rm -rf "$TEMP_DIR"' EXIT

        echo "🎵 Converting audio format..."
        if ! ffmpeg_concat_convert "$TEMP_DIR/converted.mp3" "$SAMPLE_RATE" "$BITRATE"; then
            echo "❌ Error converting audio format"
            exit 1
        fi

        echo "✂️  Splitting into segments..."
        if ! ffmpeg -i "$TEMP_DIR/converted.mp3" -f segment -segment_time $SEGMENT_LENGTH \
               -c copy -map 0 -segment_format mp3 \
               -y "$TEMP_DIR/seg_%03d.mp3" >/dev/null 2>&1; then
            echo "❌ Error splitting into segments"
            exit 1
        fi

        part=1
        for file in "$TEMP_DIR"/seg_*.mp3; do
            [[ -f "$file" ]] || continue
            target_file="$BOOK_DIR/$(printf '%02d.mp3' $part)"
            printf "📁 Part %02d -> %s\n" $part "$(basename "$target_file")"
            COPYFILE_DISABLE=1 cp -X "$file" "$target_file"
            part=$((part + 1))
        done

        echo "✅ Created $((part-1)) segments"
        ;;
    2)
        echo "🔧 Single file mode (BOOK.mp3 in USB root)..."
        TEMP_DIR=$(mktemp -d)
        trap 'rm -rf "$TEMP_DIR"' EXIT
        if ! ffmpeg_concat_convert "$TEMP_DIR/BOOK.mp3" 22050 64k; then
            echo "❌ Error creating BOOK.mp3"
            exit 1
        fi
        COPYFILE_DISABLE=1 cp -X "$TEMP_DIR/BOOK.mp3" "$BOOK_DIR/BOOK.mp3"
        echo "✅ Created BOOK.mp3"
        ;;
    6)
        if [[ ${#SRC_FILES[@]} -le 1 ]]; then
            echo "📋 Copying original file to USB root..."
            COPYFILE_DISABLE=1 cp -X "$SRC" "$BOOK_DIR/$(basename "$SRC")"
            echo "✅ Copied $(basename "$SRC")"
        else
            echo "📋 Joining ${#SRC_FILES[@]} parts into one file (no re-encode)..."
            ext=${SRC_FILES[0]##*.}
            TEMP_DIR=$(mktemp -d)
            trap 'rm -rf "$TEMP_DIR"' EXIT
            joined="$TEMP_DIR/$BASENAME.$ext"
            if concat_copy "$joined"; then
                COPYFILE_DISABLE=1 cp -X "$joined" "$BOOK_DIR/$BASENAME.$ext"
                echo "✅ Joined parts -> $BASENAME.$ext"
            else
                echo "⚠️  Stream-copy join failed (mixed formats); re-encoding to MP3…"
                if ! ffmpeg_concat_convert "$TEMP_DIR/$BASENAME.mp3" 22050 64k; then
                    echo "❌ Error joining parts"
                    exit 1
                fi
                COPYFILE_DISABLE=1 cp -X "$TEMP_DIR/$BASENAME.mp3" "$BOOK_DIR/$BASENAME.mp3"
                echo "✅ Joined parts -> $BASENAME.mp3"
            fi
        fi
        ;;
    7)
        ensure_makedaisy
        extract_audiobook_metadata "$SRC"

        DAISY_UID=$(make_daisy_uid "$DAISY_TITLE" "$DAISY_CREATOR" "$BASENAME" "$TOTAL_BYTES")

        # Free-space preflight (MakeDaisy outputs ~56 kb/s mono; use 56 for estimate).
        est_bytes=$(estimate_daisy_bytes 56)
        if [[ $est_bytes -gt 0 ]]; then
            est_mb=$((est_bytes / 1024 / 1024))
            echo "🧮 Estimated DAISY size: ~${est_mb} MB"
            stick_free=$(fs_free_bytes "$TARGET")
            tmp_free=$(fs_free_bytes "${TMPDIR:-/tmp}")
            if [[ -n $stick_free && $stick_free -lt $est_bytes ]]; then
                echo "❌ Not enough room on the stick (free: $((stick_free / 1024 / 1024)) MB, need ~${est_mb} MB)."
                echo "   Use a larger stick (still 4GB or less) or a shorter book."
                exit 1
            fi
            need_tmp=$((est_bytes * 2))
            # Multi-part sources are copied at original bitrate into temp first.
            [[ ${#SRC_FILES[@]} -gt 1 ]] && need_tmp=$((need_tmp + TOTAL_BYTES))
            if [[ -n $tmp_free && $tmp_free -lt $need_tmp ]]; then
                echo "❌ Not enough temp space on the Mac (free: $((tmp_free / 1024 / 1024)) MB, need ~$((need_tmp / 1024 / 1024)) MB during build)."
                echo "   Free up disk space and run again."
                exit 1
            fi
        fi

        echo "🔧 DAISY 2.02 for NLS (time remaining + resume)…"
        detail "   Book id: $DAISY_UID"
        TEMP_DIR=$(mktemp -d)
        trap 'rm -rf "$TEMP_DIR"' EXIT
        MAKEDAISY_SRC="$TEMP_DIR/Source"
        MAKEDAISY_OUT="$TEMP_DIR/Daisy"
        mkdir -p "$MAKEDAISY_SRC"
        write_daisy_txt "$TEMP_DIR"

        CUT_TIMES=
        TITLES=()
        seg_count=0

        echo "3/5 Preparing DAISY source..."
        if [[ ${#SRC_FILES[@]} -gt 1 ]]; then
            detail "📑 Multi-part book — one DAISY section per part (${#SRC_FILES[@]} files)…"
            n=1
            for f in "${SRC_FILES[@]}"; do
                dest="$MAKEDAISY_SRC/$(printf '%02d.mp3' $n)"
                case "${f##*.}" in
                    [Mm][Pp]3)
                        COPYFILE_DISABLE=1 cp -X "$f" "$dest"
                        ;;
                    *)
                        ffmpeg -i "$f" -ar 22050 -ac 1 -ab 64k -f mp3 \
                            -map_metadata 0 -id3v2_version 3 -y "$dest" >/dev/null 2>&1 \
                            || { echo "❌ Error converting $(basename "$f") for DAISY"; exit 1; }
                        ;;
                esac
                n=$((n + 1))
            done
            seg_count=$((n - 1))
            detail "✅ Prepared $seg_count part(s) for MakeDaisy"
        else
            chapters=$(read_source_chapters "$SRC")
            nchap=0
            [[ -n $chapters ]] && nchap=$(printf '%s\n' "$chapters" | grep -c . || true)
            if [[ $nchap -ge 2 ]]; then
                detail "📑 Found $nchap chapter(s) — splitting on chapter boundaries…"
                first=1
                while IFS=$'\t' read -r cstart ctitle; do
                    [[ -z $cstart ]] && continue
                    if [[ $first -eq 1 ]]; then
                        first=0
                    else
                        ct=$(python3 -c "print(f'{float(\"$cstart\"):.3f}')" 2>/dev/null || echo "$cstart")
                        [[ -n $CUT_TIMES ]] && CUT_TIMES+=,
                        CUT_TIMES+=$ct
                    fi
                    t=$(printf '%s' "$ctitle" | tr ',' ';' | tr -s '[:space:]' ' ')
                    t=${t# }; t=${t% }
                    [[ -z $t ]] && t=$(printf 'Chapter %02d' $((${#TITLES[@]} + 1)))
                    TITLES+=("$t")
                done < <(printf '%s\n' "$chapters")
                detail "🎵 Preparing source segments (chapter splits)…"
                seg_count=$(create_nls_segments "$MAKEDAISY_SRC" 22050 64k 3600 "$CUT_TIMES") || exit 1
            else
                detail "📑 No embedded chapters — using 1-hour segments…"
                detail "🎵 Preparing source segments…"
                seg_count=$(create_nls_segments "$MAKEDAISY_SRC" 22050 64k 3600) || exit 1
            fi
            [[ $seg_count -gt 0 ]] || { echo "❌ No segments created"; exit 1; }
            detail "✅ Prepared $seg_count source segment(s)"
            # Optional per-segment titles for MakeDaisy (reads 01.txt beside 01.mp3).
            for ((i = 1; i <= seg_count; i++)); do
                if [[ $((i - 1)) -lt ${#TITLES[@]} ]]; then
                    printf 'Title: %s\n' "${TITLES[$((i - 1))]}" > "$MAKEDAISY_SRC/$(printf '%02d.txt' $i)"
                fi
            done
        fi

        echo "4/5 Building DAISY book (this may take a while)..."
        if [[ $VERBOSE -eq 1 ]]; then
            (cd "$TEMP_DIR" && python3 "$MAKEDAISY" -it -sf Source Daisy) || { echo "❌ MakeDaisy failed"; exit 1; }
        else
            makedaisy_log=$(mktemp)
            if ! (cd "$TEMP_DIR" && python3 "$MAKEDAISY" -it -sf Source Daisy) >"$makedaisy_log" 2>&1; then
                echo "❌ MakeDaisy failed."
                echo "Last output:"
                tail -40 "$makedaisy_log" 2>/dev/null || true
                rm -f "$makedaisy_log"
                exit 1
            fi
            rm -f "$makedaisy_log"
        fi
        [[ -f "$MAKEDAISY_OUT/ncc.html" ]] || { echo "❌ MakeDaisy did not produce ncc.html"; exit 1; }
        echo "✅ DAISY package built ($(find "$MAKEDAISY_OUT" -maxdepth 1 -iname '*.mp3' | wc -l | tr -d ' ') audio file(s))"

        echo "5/5 Copying DAISY book to USB..."
        COPYFILE_DISABLE=1 cp -X "$MAKEDAISY_OUT"/* "$TARGET/"
        echo "✅ DAISY book copied"
        ;;
esac

############################################################ 7. file count check
detail "📊 Counting created files..."
file_count=$(find "$TARGET" -maxdepth 3 -iname "*.mp3" ! -iname "._*" 2>/dev/null | wc -l | tr -d '[:space:]' || true)
file_count=${file_count:-0}
[[ $file_count =~ ^[0-9]+$ ]] || file_count=0
daisy_ncx=
if [[ $mode == 7 ]]; then
    daisy_ncx=$(find "$TARGET" -maxdepth 2 \( -iname '*.ncx' -o -iname 'ncc.html' \) ! -iname '._*' 2>/dev/null | head -n1 || true)
    detail "📊 DAISY package: ${file_count} MP3(s)${daisy_ncx:+, nav: $(basename "$daisy_ncx")}"
else
    detail "📊 Total files created: $file_count"
fi
drive_fs=$(echo "$drive_info" | awk -F': *' '/File System Personality:/ {print $2; exit}')
drive_fs=${drive_fs:-Unknown}

if [[ $file_count -gt 30 && $mode != 7 ]]; then
    echo "⚠️  WARNING: $file_count files may be too many for older NLS players"
    echo "   Consider using option 5 for fewer, longer segments"
elif [[ $file_count -gt 30 && $mode == 7 && -z $daisy_ncx ]]; then
    echo "⚠️  WARNING: $file_count MP3s but no ncc.html/NCX found — DAISY unpack may have failed"
fi

############################################################ 8. cleanup and finalize
echo "Cleaning and ejecting USB..."
clean_usb_junk "$TARGET"

# Disable Spotlight indexing on the USB drive to prevent future issues
mdutil -i off "$TARGET" >/dev/null 2>&1 || true

detail "🔄 Finalizing USB drive..."
sync

if [[ $VERBOSE -eq 1 ]]; then
    echo ""
    echo "📁 Final USB structure:"
    if [[ $mode == 7 ]]; then
        find "$TARGET" -maxdepth 1 -type f \
            \( -iname '*.ncx' -o -iname '*.opf' -o -iname 'ncc.html' -o -iname '*.mp3' \) \
            -exec ls -la {} + 2>/dev/null | head -20 || echo "DAISY files in USB root"
    elif [[ $mode == 2 || $mode == 6 ]]; then
        find "$TARGET" -maxdepth 1 -type f -iname '*.mp3' -exec ls -la {} + 2>/dev/null || echo "MP3 files in USB root"
    else
        ls -la "$TARGET/audio+podcasts/" 2>/dev/null || echo "audio+podcasts folder created"
        if [[ -d "$BOOK_DIR" ]]; then
            echo "📖 Book folder (audio+podcasts/${BOOK_FOLDER:-}) contents:"
            find "$BOOK_DIR" -maxdepth 1 -type f -iname '*.mp3' -exec ls -la {} + 2>/dev/null || echo "Files copied successfully"
        fi
    fi
fi

echo ""
if eject_target_volume "$TARGET"; then
    echo "✅ Done. Safe to unplug."
else
    echo "❌ Could not eject automatically."
    echo "   In Finder, click the Eject button next to the USB drive, then unplug."
    exit 1
fi
if [[ $mode == 7 ]]; then
    echo "Mode: DAISY 2.02, Option 7"
    echo "Files: ${file_count} audio file(s)${daisy_ncx:+ + $(basename "$daisy_ncx")}"
else
    echo "Mode: Option $mode"
    echo "Files: ${file_count} audio file(s)"
fi
detail "Drive size: ${drive_size_gb:-unknown}GB"
detail "Format: $drive_fs"

if [[ $VERBOSE -eq 1 ]]; then
    echo ""
    echo "📖 CARTRIDGE ERROR TROUBLESHOOTING:"
    echo "   If you get 'Cartridge Error' on your NLS player:"
    echo "   1. Use a 2GB USB stick (tested and confirmed working; drives over 4GB usually fail)"
    echo "   2. Try option 1 (Library Mode) - fast fallback, book in its own audio+podcasts/<book> folder"
    echo "   3. Try option 2 (Single File) - BOOK.mp3 in USB root"
    echo "   4. For time remaining + resume, try option 7 (MakeDaisy DAISY 2)"
    echo "   5. Say yes to format as FAT32 and do not open the stick in Finder before testing"
    echo "   6. Eject from the Mac before unplugging (this script does that automatically)"
    echo ""
    echo "💡 NLS Player Tips:"
    echo "   • Remove cartridge BEFORE powering off player"
    echo "   • Wait for 'Book loaded' or similar announcement before playing"
    echo "   • Try reseating the cartridge if errors persist"
    echo "   • Some older players work better with fewer, longer audio files"
    echo "   • Small 2GB sticks are ideal; avoid USB drives larger than 4GB"
fi
