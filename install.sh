#!/usr/bin/env bash
set -euo pipefail

version="${JABRIDGE_VERSION:-1.0.0}"
prefix="${PREFIX:-/usr/local}"
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

if [[ ! -f "$script_dir/go.mod" ]]; then
    printf '%s\n' "Jabridge must currently be installed from a source checkout." >&2
    printf '%s\n' "Clone the repository, then run: sudo ./install.sh" >&2
    exit 2
fi

if ! command -v go >/dev/null 2>&1; then
    printf '%s\n' "Go 1.23.2 or newer is required." >&2
    exit 2
fi

build_dir="$(mktemp -d)"
cleanup() {
    rm -f -- "$build_dir/jabridge" "$build_dir/jafw"
    rmdir -- "$build_dir" 2>/dev/null || true
}
trap cleanup EXIT

module_path="github.com/Watchdog0x/jabridge"
ldflags="-s -w -X ${module_path}/internal/buildinfo.Version=${version}"

(
    cd "$script_dir"
    CGO_ENABLED=0 go test ./...
    CGO_ENABLED=0 go build -trimpath -ldflags="$ldflags" -o "$build_dir/jabridge" .
    CGO_ENABLED=0 go build -trimpath -ldflags="$ldflags" -o "$build_dir/jafw" ./cmd/jafw
)

install -Dm755 "$build_dir/jabridge" "$prefix/bin/jabridge"
install -Dm755 "$build_dir/jafw" "$prefix/bin/jafw"
install -Dm644 "$script_dir/internal/completion/jabridge.bash" "$prefix/share/bash-completion/completions/jabridge"
install -Dm644 "$script_dir/internal/completion/jafw.bash" "$prefix/share/bash-completion/completions/jafw"

printf 'Installed Jabridge %s to %s/bin\n' "$version" "$prefix"
printf 'Installed Bash completion to %s/share/bash-completion/completions\n' "$prefix"
printf '%s\n' "No proprietary Jabra library or firmware updater was installed."
