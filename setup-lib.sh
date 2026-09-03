#!/usr/bin/env bash
# Populate lib/ for local builds (not committed to git).
#  - libjabra.so   from install.sh embedded blob or /usr/lib/jabra
#  - libcurl.so.4  system copy or Ubuntu package when CURL_OPENSSL_4 is missing (Fedora)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT"
mkdir -p lib

UBUNTU_LIBCURL_DEB="http://archive.ubuntu.com/ubuntu/pool/main/c/curl/libcurl4t64_8.5.0-2ubuntu10_amd64.deb"
JABRA_MD5="5092ca68e1b6081de35ccd001f2d974f"

system_libcurl_paths() {
	# Common locations on Linux amd64
	printf '%s\n' \
		/usr/lib/x86_64-linux-gnu/libcurl.so.4 \
		/usr/lib64/libcurl.so.4 \
		/usr/lib64/libcurl.so \
		/lib/x86_64-linux-gnu/libcurl.so.4
}

has_curl_openssl4() {
	local so="$1"
	[[ -e "$so" ]] || return 1
	so="$(readlink -f "$so")"
	readelf -V "$so" 2>/dev/null | grep -q 'CURL_OPENSSL_4'
}

find_system_libcurl_openssl4() {
	local p
	while IFS= read -r p; do
		if has_curl_openssl4 "$p"; then
			echo "$p"
			return 0
		fi
	done < <(system_libcurl_paths)
	return 1
}

ensure_libjabra() {
	if [[ ! -f lib/libjabra.so ]]; then
	if [[ -f /usr/lib/jabra/libjabra.so ]]; then
		cp -a /usr/lib/jabra/libjabra.so lib/
		echo "Copied /usr/lib/jabra/libjabra.so -> lib/libjabra.so"
	else
		python3 - "$ROOT/install.sh" "$ROOT/lib/libjabra.so" <<'PY'
import re
import sys

install_sh, out_path = sys.argv[1], sys.argv[2]
hex_blob = None
with open(install_sh, encoding="utf-8", errors="replace") as f:
    for line in f:
        if line.startswith("libjabraSo="):
            hex_blob = line.split("=", 1)[1].strip().strip('"')
            break
if not hex_blob:
    sys.exit("libjabraSo= not found in install.sh")
data = bytes(int(h, 16) for h in re.findall(r"0x([0-9a-fA-F]{2})", hex_blob))
with open(out_path, "wb") as out:
    out.write(data)
print(len(data))
PY
		echo "Created lib/libjabra.so ($(wc -c < lib/libjabra.so) bytes)"
	fi

	local md5
	md5="$(md5sum lib/libjabra.so | awk '{print $1}')"
	if [[ "$md5" != "$JABRA_MD5" ]]; then
		echo "Checksum mismatch for lib/libjabra.so" >&2
		rm -f lib/libjabra.so
		exit 2
	fi
	fi

	# Binary links against libjabra.so.1 (SONAME), same as install.sh.
	ln -sf libjabra.so lib/libjabra.so.1
}

fetch_ubuntu_libcurl() {
	local tmp deb
	if ! command -v curl >/dev/null 2>&1; then
		echo "curl required to download compatible libcurl" >&2
		exit 1
	fi
	if ! command -v ar >/dev/null 2>&1 || ! command -v zstd >/dev/null 2>&1; then
		echo "Need ar and zstd (binutils, zstd). Or: sudo dnf install binutils zstd" >&2
		exit 1
	fi

	tmp="$(mktemp -d)"
	deb="$tmp/libcurl.deb"

	echo "Downloading Ubuntu libcurl (CURL_OPENSSL_4) ..."
	curl -fsSL -o "$deb" "$UBUNTU_LIBCURL_DEB"
	(
		cd "$tmp"
		ar x libcurl.deb
		zstd -d -f data.tar.zst -o data.tar
		tar xf data.tar ./usr/lib/x86_64-linux-gnu/libcurl.so.4.8.0
	)
	cp -a "$tmp/usr/lib/x86_64-linux-gnu/libcurl.so.4.8.0" lib/libcurl.so.4.8.0
	ln -sf libcurl.so.4.8.0 lib/libcurl.so.4
	rm -rf "$tmp"
	echo "Created lib/libcurl.so.4 from Ubuntu package"
}

ensure_libcurl() {
	if [[ -f lib/libcurl.so.4 ]] && has_curl_openssl4 lib/libcurl.so.4; then
		return 0
	fi
	rm -f lib/libcurl.so.4

	local sys
	if sys="$(find_system_libcurl_openssl4)"; then
		if [[ -L "$sys" ]]; then
			local real
			real="$(readlink -f "$sys")"
			cp -a "$real" lib/libcurl.so.4.8.0
			ln -sf libcurl.so.4.8.0 lib/libcurl.so.4
		else
			cp -a "$sys" lib/libcurl.so.4
		fi
		echo "Copied $sys -> lib/libcurl.so.4"
		return 0
	fi

	if [[ "$(uname -m)" != "x86_64" ]]; then
		echo "No compatible libcurl on this system and Ubuntu bundle is x86_64 only." >&2
		echo "Build on Debian/Ubuntu or use: curl -so- …/install.sh | sudo bash" >&2
		exit 1
	fi

	fetch_ubuntu_libcurl
}

ensure_libjabra
ensure_libcurl

echo "lib/ is ready for: go build -o jLink ."
