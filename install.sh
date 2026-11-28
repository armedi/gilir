#!/bin/sh
# Install the prebuilt Gilir binary from GitHub Releases on macOS or Linux.
#
#   curl -fsSL https://raw.githubusercontent.com/armedi/gilir/main/install.sh | sh
#
# Options, after `sh -s --`:
#   --version <v>   Install a specific release, for example 0.3.0 (default: latest)
#   --dir <path>    Install into <path>/gilir (default: $XDG_BIN_HOME or ~/.local/bin)
#   --help          Show this help
#
# Environment:
#   GILIR_VERSION        Same as --version
#   GILIR_INSTALL_DIR    Same as --dir
#   GILIR_RELEASES_URL   Release origin, for tests and mirrors. Unset in normal use.
#
# The installer downloads one static binary, verifies it against the release's
# SHA256SUMS, and replaces <dir>/gilir atomically. It never needs Node.js and
# never calls sudo: choose a writable --dir instead.

set -eu

repo=armedi/gilir
releases=${GILIR_RELEASES_URL:-"https://github.com/$repo/releases"}
while :; do
    case $releases in
        */) releases=${releases%/} ;;
        *) break ;;
    esac
done
default_releases="https://github.com/$repo/releases"

err() {
    printf 'gilir-install: %s\n' "$*" >&2
    exit 1
}

usage() {
    cat <<'EOF'
Install the prebuilt Gilir binary from GitHub Releases on macOS or Linux.

Usage:
  curl -fsSL https://raw.githubusercontent.com/armedi/gilir/main/install.sh | sh
  curl -fsSL ... | sh -s -- --version 0.3.0 --dir "$HOME/bin"

Options:
  --version <v>   Install a specific release, for example 0.3.0 (default: latest)
  --dir <path>    Install into <path>/gilir (default: $XDG_BIN_HOME or ~/.local/bin)
  -h, --help      Show this help

Environment:
  GILIR_VERSION        Same as --version
  GILIR_INSTALL_DIR    Same as --dir
  GILIR_RELEASES_URL   Release origin, for tests and mirrors. Unset in normal use.

Supported platforms: macOS and Linux on x64 and arm64. Windows uses
`npm install -g @armedi/gilir`, or download gilir-win32-<arch>.exe from Releases.
EOF
}

have() {
    command -v "$1" >/dev/null 2>&1
}

require() {
    for tool in "$@"; do
        have "$tool" || err "required command '$tool' not found in PATH"
    done
}

# All network access goes through one helper so a mirror can use http:// and
# file:// origins while normal installs stay https-only.
fetch() {
    if [ "$releases" = "$default_releases" ]; then
        curl -fsSL --retry 3 --connect-timeout 20 --proto =https --proto-redir =https "$@"
    else
        curl -fsSL --retry 3 --connect-timeout 20 "$@"
    fi
}

# Hash the content, not the path: macOS shasum escapes filenames that contain a
# backslash, which a plain `shasum -a 256 "$file"` would print as a prefix.
sha256_of() {
    if have sha256sum; then
        sha256sum < "$1" | awk '{print $1}'
    elif have shasum; then
        shasum -a 256 < "$1" | awk '{print $1}'
    else
        err 'need sha256sum or shasum to verify the download'
    fi
}

lowercase() {
    printf '%s' "$1" | tr '[:upper:]' '[:lower:]'
}

detect_platform() {
    os=$(uname -s)
    case $os in
        Darwin) os=darwin ;;
        Linux) os=linux ;;
        *)
            err "unsupported OS '$os'; this installer supports macOS and Linux. On Windows use npm install -g @armedi/gilir, or download gilir-win32-<arch>.exe from $releases"
            ;;
    esac

    cpu=$(uname -m)
    case $cpu in
        x86_64 | amd64) cpu=x64 ;;
        arm64 | aarch64) cpu=arm64 ;;
        *) err "unsupported CPU architecture '$cpu'" ;;
    esac

    # A shell running under Rosetta reports x86_64 on Apple silicon; the arm64
    # binary runs natively and is the one to install.
    if [ "$os" = darwin ] && have sysctl; then
        translated=$(sysctl -n sysctl.proc_translated 2>/dev/null || true)
        if [ "$translated" = 1 ]; then
            cpu=arm64
        fi
    fi

    file="gilir-$os-$cpu"
}

resolve_latest() {
    effective=$(fetch -o /dev/null -w '%{url_effective}' "$releases/latest") ||
        err "could not query the latest release from $releases/latest"
    case $effective in
        "$releases/tag/v"*) printf '%s' "${effective#"$releases/tag/v"}" ;;
        *) err "could not determine the latest release from $effective" ;;
    esac
}

validate_version() {
    case $1 in
        '' | *[!0-9A-Za-z.+-]*) err "invalid version '$1'" ;;
    esac
}

# Read `<sha256>  <file>` lines, the sha256sum format in SHA256SUMS.
checksum_from_file() {
    awk -v f="$2" '$2 == f || $2 == "*" f { print $1; exit }' "$1"
}

# Releases published before SHA256SUMS was uploaded still expose each asset's
# digest through the GitHub API. Only used as a fallback for those releases.
# The API may answer with compact or pretty JSON, so this reads the whole body,
# then matches a name and a digest only inside the same asset object.
checksum_from_api() {
    release=$1
    want=$2
    body=$(fetch "https://api.github.com/repos/$repo/releases/tags/v$release") || return 0
    printf '%s\n' "$body" | awk -v want="$want" '
        { whole = whole $0 }
        END {
            gsub(/[ \t\r\n]+/, "", whole)
            start = index(whole, "\"assets\":[")
            if (start == 0) exit
            list = substr(whole, start + 10)
            stop = index(list, "]")
            if (stop == 0) exit
            count = split(substr(list, 1, stop - 1), objects, "},{")
            for (i = 1; i <= count; i++) {
                if (!match(objects[i], "\"name\":\"[^\"]*\"")) continue
                name = substr(objects[i], RSTART + 8, RLENGTH - 9)
                if (name != want) continue
                if (!match(objects[i], "\"digest\":\"sha256:[0-9a-f]+\"")) exit
                digest = substr(objects[i], RSTART, RLENGTH)
                sub(/.*sha256:/, "", digest)
                sub(/".*/, "", digest)
                print digest
                exit
            }
        }
    '
}

is_sha256() {
    case $1 in
        *[!0-9a-f]* | '') return 1 ;;
    esac
    [ "${#1}" -eq 64 ]
}

install_binary() {
    dir=$1
    source=$2

    mkdir -p "$dir" 2>/dev/null || err "cannot create $dir; pass --dir with a writable location"
    [ -d "$dir" ] || err "$dir is not a directory"
    [ -w "$dir" ] || err "cannot write to $dir; pass --dir with a writable location (no sudo needed)"

    # Stage inside a private directory in the target filesystem: nothing else can
    # plant a symlink for the copy to follow, and the final rename stays atomic.
    stage_dir=$(mktemp -d "$dir/.gilir-install.XXXXXX") || err "cannot write to $dir"
    cp "$source" "$stage_dir/gilir" || err "cannot write to $dir"
    chmod 755 "$stage_dir/gilir" || err "cannot set permissions on $stage_dir/gilir"

    # rename(2) replaces a regular file atomically. Any other existing entry is
    # removed first, so mv cannot move the staged file *into* it.
    if [ -L "$dir/gilir" ] || { [ -e "$dir/gilir" ] && [ ! -f "$dir/gilir" ]; }; then
        rm -f "$dir/gilir" || err "cannot replace $dir/gilir"
    fi
    mv -f "$stage_dir/gilir" "$dir/gilir" || err "cannot replace $dir/gilir"
    rmdir "$stage_dir" 2>/dev/null || true
    stage_dir=
}

path_hint() {
    dir=$1
    case ":$PATH:" in
        *":$dir:"*) return 0 ;;
    esac

    pretty=$dir
    if [ -n "${HOME:-}" ]; then
        case $dir in
            "$HOME"/*) pretty="\$HOME/${dir#"$HOME"/}" ;;
        esac
    fi

    shell=${SHELL:-}
    shell=${shell##*/}
    case $shell in
        fish) hint="fish_add_path $pretty" ;;
        zsh) hint="echo 'export PATH=\"$pretty:\$PATH\"' >> ~/.zshrc" ;;
        bash) hint="echo 'export PATH=\"$pretty:\$PATH\"' >> ~/.bashrc" ;;
        *) hint="export PATH=\"$pretty:\$PATH\"" ;;
    esac

    printf '\n%s is not on PATH. Add it with:\n  %s\nand restart your shell.\n' "$dir" "$hint"
    printf 'OpenCode agents run gilir too, so the OpenCode server needs it on PATH as well.\n'
}

cleanup() {
    status=$?
    trap - EXIT INT TERM
    if [ -n "${stage_dir:-}" ]; then
        rm -rf "$stage_dir" 2>/dev/null || true
    fi
    if [ -n "${tmp:-}" ]; then
        rm -rf "$tmp" 2>/dev/null || true
    fi
    exit "$status"
}

main() {
    version=${GILIR_VERSION:-}
    version_set=0
    if [ -n "$version" ]; then
        version_set=1
        version_arg=$version
    fi
    dir=${GILIR_INSTALL_DIR:-}
    dir_set=0
    if [ -n "$dir" ]; then
        dir_set=1
    fi

    while [ $# -gt 0 ]; do
        case $1 in
            --version)
                [ $# -ge 2 ] || err "--version needs a value"
                version=$2
                version_arg=$version
                version_set=1
                shift 2
                ;;
            --version=*)
                version=${1#*=}
                version_arg=$version
                version_set=1
                shift
                ;;
            --dir)
                [ $# -ge 2 ] || err "--dir needs a value"
                dir=$2
                dir_set=1
                shift 2
                ;;
            --dir=*)
                dir=${1#*=}
                dir_set=1
                shift
                ;;
            -h | --help)
                usage
                exit 0
                ;;
            *)
                err "unknown argument '$1'; run with --help"
                ;;
        esac
    done

    require curl uname mktemp awk tr cp mv rm mkdir chmod
    detect_platform

    version=${version#v}
    if [ "$version_set" = 1 ]; then
        [ -n "$version" ] || err "invalid version '$version_arg'"
        validate_version "$version"
    else
        version=$(resolve_latest)
        [ -n "$version" ] || err "invalid version '$version'"
        validate_version "$version"
    fi

    if [ "$dir_set" = 0 ]; then
        [ -n "${HOME:-}" ] ||
            err 'HOME is not set; pass --dir with an install directory'
        dir=${XDG_BIN_HOME:-$HOME/.local/bin}
    fi
    case $dir in
        "~")
            [ -n "${HOME:-}" ] || err 'HOME is not set; pass --dir with an install directory'
            dir=$HOME
            ;;
        "~"/*)
            [ -n "${HOME:-}" ] || err 'HOME is not set; pass --dir with an install directory'
            dir=$HOME/${dir#"~/"}
            ;;
    esac
    while :; do
        case $dir in
            */) dir=${dir%/} ;;
            *) break ;;
        esac
    done
    [ -n "$dir" ] || err '--dir must not be empty'

    existing=$(command -v gilir 2>/dev/null || true)

    tmp=$(mktemp -d "${TMPDIR:-/tmp}/gilir-install.XXXXXX") ||
        err 'cannot create a temporary directory'
    stage_dir=
    trap cleanup EXIT INT TERM

    base="$releases/download/v$version"
    sums="$tmp/SHA256SUMS"
    if fetch -o "$sums" "$base/SHA256SUMS" 2>/dev/null; then
        expected=$(checksum_from_file "$sums" "$file")
    elif [ "$releases" = "$default_releases" ]; then
        expected=$(checksum_from_api "$version" "$file")
    else
        expected=
    fi

    expected=$(lowercase "$expected")
    is_sha256 "$expected" ||
        err "release v$version has no usable checksum for $file; install it with npm install -g @armedi/gilir@$version"

    printf 'Downloading gilir %s (%s)...\n' "$version" "$file"
    binary="$tmp/$file"
    fetch -o "$binary" "$base/$file" ||
        err "could not download $base/$file"

    actual=$(lowercase "$(sha256_of "$binary")")
    is_sha256 "$actual" || err "could not hash $binary"
    [ "$actual" = "$expected" ] || err "checksum mismatch for $file: expected $expected, got $actual"

    chmod 755 "$binary"
    "$binary" --help >/dev/null 2>&1 ||
        err "the $file binary does not run on this system"

    install_binary "$dir" "$binary"
    printf 'Installed gilir %s to %s/gilir\n' "$version" "$dir"

    path_hint "$dir"

    if [ -n "$existing" ] && [ "$existing" != "$dir/gilir" ]; then
        printf '\nNote: %s comes first on PATH. Remove it (for the npm launcher: npm uninstall -g @armedi/gilir)\n' "$existing"
    fi
    printf 'If the Gilir service is running, run "gilir service restart" to use the new binary.\n'
}

main "$@"
