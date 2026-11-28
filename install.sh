#!/bin/sh
# Install the prebuilt Gilir binary from GitHub Releases.
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

err() {
    printf 'gilir-install: %s\n' "$*" >&2
    exit 1
}

usage() {
    cat <<'EOF'
Install the prebuilt Gilir binary from GitHub Releases.

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

Supported platforms: macOS and Linux on x64 and arm64. On Windows use
`npm install -g @armedi/gilir` or download gilir-win32-<arch>.exe from Releases.
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
    if [ "$releases" = "https://github.com/$repo/releases" ]; then
        curl -fsSL --retry 3 --connect-timeout 20 --proto =https --proto-redir =https "$@"
    else
        curl -fsSL --retry 3 --connect-timeout 20 "$@"
    fi
}

sha256_of() {
    if have sha256sum; then
        sha256sum "$1" | awk '{print $1}'
    elif have shasum; then
        shasum -a 256 "$1" | awk '{print $1}'
    else
        err "need sha256sum or shasum to verify the download"
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
        *) err "unsupported OS '$os'; on Windows use npm install -g @armedi/gilir, or download gilir-win32-<arch>.exe from $releases" ;;
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
    latest=${effective##*/tag/}
    [ "$latest" != "$effective" ] && [ -n "$latest" ] ||
        err "could not determine the latest release from $effective"
    printf '%s' "${latest#v}"
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

# Releases cut before SHA256SUMS was uploaded still expose each asset's digest
# through the GitHub API. Only used as a fallback for those releases. The API
# may return compact or pretty-printed JSON, so this normalizes both.
checksum_from_api() {
    release=$1
    want=$2
    body=$(fetch "https://api.github.com/repos/$repo/releases/tags/v$release") || return 0
    printf '%s\n' "$body" | awk -v want="$want" '
        {
            gsub(/[ \t]*"digest"[ \t]*:[ \t]*"sha256:/, "\nDIGEST ")
            gsub(/[ \t]*"name"[ \t]*:[ \t]*"gilir-/, "\nNAME gilir-")
            count = split($0, parts, "\n")
            for (i = 1; i <= count; i++) {
                part = parts[i]
                if (part ~ /^NAME /) {
                    name = substr(part, 6)
                    sub(/".*/, "", name)
                } else if (part ~ /^DIGEST /) {
                    digest = substr(part, 8)
                    sub(/".*/, "", digest)
                    if (name == want) {
                        print digest
                        exit
                    }
                }
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

    # Stage inside the target directory so the final rename is atomic and never
    # leaves a half-written binary, even while gilir is running.
    stage="$dir/.gilir-install.$$"
    cp "$source" "$stage" || err "cannot write to $dir"
    chmod 755 "$stage"
    mv -f "$stage" "$dir/gilir" || err "cannot replace $dir/gilir"
    stage=
}

path_hint() {
    dir=$1
    case ":$PATH:" in
        *":$dir:"*) return 0 ;;
    esac

    pretty=$dir
    case $dir in
        "$HOME"/*) pretty="\$HOME/${dir#"$HOME"/}" ;;
    esac

    case ${SHELL##*/} in
        fish) hint="fish_add_path $pretty" ;;
        zsh) hint="echo 'export PATH=\"$pretty:\$PATH\"' >> ~/.zshrc" ;;
        bash) hint="echo 'export PATH=\"$pretty:\$PATH\"' >> ~/.bashrc" ;;
        *) hint="export PATH=\"$pretty:\$PATH\"" ;;
    esac

    printf '\n%s is not on PATH. Add it with:\n  %s\nand restart your shell.\n' "$dir" "$hint"
    printf 'OpenCode agents run gilir too, so the OpenCode server needs it on PATH as well.\n'
}

main() {
    version=${GILIR_VERSION:-}
    dir=${GILIR_INSTALL_DIR:-${XDG_BIN_HOME:-$HOME/.local/bin}}

    while [ $# -gt 0 ]; do
        case $1 in
            --version)
                [ $# -ge 2 ] || err "--version needs a value"
                version=$2
                shift 2
                ;;
            --version=*)
                version=${1#*=}
                shift
                ;;
            --dir)
                [ $# -ge 2 ] || err "--dir needs a value"
                dir=$2
                shift 2
                ;;
            --dir=*)
                dir=${1#*=}
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

    require curl uname mktemp awk tr cp mv chmod
    detect_platform

    version=${version#v}
    if [ -n "$version" ]; then
        validate_version "$version"
    else
        version=$(resolve_latest)
        validate_version "$version"
    fi

    case $dir in
        "~") dir=$HOME ;;
        "~"/*) dir=$HOME/${dir#"~/"} ;;
    esac
    while :; do
        case $dir in
            */) dir=${dir%/} ;;
            *) break ;;
        esac
    done
    [ -n "$dir" ] || dir=/

    existing=$(command -v gilir 2>/dev/null || true)

    tmp=$(mktemp -d "${TMPDIR:-/tmp}/gilir-install.XXXXXX")
    stage=
    # shellcheck disable=SC2064
    trap "rm -rf '$tmp'; [ -z \"\$stage\" ] || rm -f \"\$stage\"" EXIT
    trap 'exit 130' INT
    trap 'exit 143' TERM

    base="$releases/download/v$version"
    sums="$tmp/SHA256SUMS"
    if fetch -o "$sums" "$base/SHA256SUMS" 2>/dev/null; then
        expected=$(checksum_from_file "$sums" "$file")
    elif [ "$releases" = "https://github.com/$repo/releases" ]; then
        # Releases published before SHA256SUMS was uploaded still expose each
        # asset's digest through the GitHub API.
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
