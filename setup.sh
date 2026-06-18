#!/usr/bin/env bash
# One-time setup for the Bubble Tea and plain-terminal workflows (macOS + Homebrew).
set -euo pipefail

ROOT=$(cd "$(dirname "$0")" && pwd)
cd "$ROOT"

echo "📦 NLS audiobook stick — setup"
echo "   Repo: $ROOT"
echo ""

if [[ $(uname -s) != Darwin ]]; then
    echo "❌ This project is macOS-only (uses diskutil for USB sticks)."
    exit 1
fi

if ! command -v brew >/dev/null; then
    echo "❌ Homebrew is required. Install from https://brew.sh then run ./setup.sh again."
    exit 1
fi

if ! xcode-select -p >/dev/null 2>&1; then
    echo "❌ Xcode Command Line Tools are required (for gcc when building id3dump)."
    echo "   Run: xcode-select --install"
    exit 1
fi

if ! command -v python3 >/dev/null; then
    echo "❌ python3 is required (Option 7 / MakeDaisy)."
    exit 1
fi

install_brew_pkg() {
    local pkg=$1
    if brew list "$pkg" >/dev/null 2>&1; then
        echo "✅ $pkg"
    else
        echo "⬇️  Installing $pkg…"
        brew install "$pkg"
    fi
}

echo "Checking Homebrew packages…"
install_brew_pkg ffmpeg
install_brew_pkg lame
install_brew_pkg mpg123
install_brew_pkg go

chmod +x load_audiobook.sh

patch_makedaisy() {
    local f=$ROOT/tools/MakeDaisy/makedaisy
    [[ -f $f ]] || return 0
    # Python 3: file() → open()
    if grep -q 'file (sTags)' "$f" 2>/dev/null; then
        sed -i '' 's/file (sTags)/open (sTags)/g' "$f"
        sed -i '' 's/file (sInfo)/open (sInfo)/g' "$f"
        echo "🔧 Patched MakeDaisy for Python 3"
    fi
    # Quieter lame (avoid megabytes of encode progress on screen readers)
    if ! grep -q 'lame -q' "$f" 2>/dev/null; then
        sed -i '' 's|TIDYMP3   = "lame -a|TIDYMP3   = "lame -q -a|' "$f"
        sed -i '' 's|resample 44.1"|resample 44.1 2>/dev/null"|' "$f"
        echo "🔧 Patched MakeDaisy for quiet lame"
    fi
}

build_id3dump() {
    local root=$ROOT/tools/MakeDaisy
    local prefix
    prefix=$(brew --prefix mpg123 2>/dev/null)
    echo "🔧 Building id3dump…"
    gcc -o "$root/id3dump" "$root/id3dump.c" \
        -I"$prefix/include" -L"$prefix/lib" -lmpg123 \
        || { echo "❌ id3dump build failed"; exit 1; }
    echo "✅ id3dump built"
}

if [[ ! -f tools/MakeDaisy/makedaisy ]]; then
    echo "⬇️  Cloning MakeDaisy…"
    mkdir -p tools
    git clone --depth 1 https://github.com/Memotech-Bill/MakeDaisy.git tools/MakeDaisy
fi
patch_makedaisy

if [[ ! -x tools/MakeDaisy/id3dump ]]; then
    build_id3dump
else
    echo "✅ id3dump"
fi

mkdir -p books
touch books/.gitkeep

echo "🔧 Building Bubble Tea interface…"
mkdir -p bin
go build -o bin/audiobooks ./cmd/audiobooks
echo "✅ bin/audiobooks"

echo ""
echo "✅ Setup complete."
echo "   Put audiobooks in books/ (see about.md), insert a 2–4GB USB stick, then run:"
echo "   ./bin/audiobooks"
echo "   Accessible plain-terminal fallback: ./load_audiobook.sh"
