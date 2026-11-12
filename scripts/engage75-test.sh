#!/usr/bin/env bash
# One explicit, logged firmware trial. No automatic retries or app installation.
set -euo pipefail
umask 077

bundle_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
cd -- "$bundle_dir"
report="$bundle_dir/jabridge-engage75-result.txt"
marker="$bundle_dir/.engage75-test-started"
active="$bundle_dir/.engage75-test-running"
download=''
archive="$bundle_dir/Jabra_Engage_75_5.20.1.zip"
firmware_sha='210d0402470502aa28716a98a67a4343e6f29d98f0b7681b676e66a12d2c3a98'
version='Jabridge 1.6.2-issue62.9'

if [[ "$(uname -s)" != Linux || "$(uname -m)" != x86_64 ]]; then
  printf '%s\n' 'This test build needs Linux on an Intel or AMD 64-bit computer.'
  exit 1
fi
for program in sha256sum curl tee; do
  if ! command -v "$program" >/dev/null 2>&1; then
    printf 'This test needs %s. Please tell me it is missing.\n' "$program"
    exit 1
  fi
done
if ! sha256sum --check --status SHA256SUMS; then
  printf '%s\n' 'The test files did not pass their checks. Download and extract them again.'
  exit 1
fi
# Own this folder before changing permissions, waiting for acceptance or
# touching shared report/archive files. A second window must not get past it.
if ! mkdir -- "$active" 2>/dev/null; then
  printf '%s\n' 'A test is already open or was interrupted. Do not start another.' \
    'Use the first test window, or send the existing result file.'
  exit 1
fi
cleanup() {
  if [[ -n "$download" ]]; then rm -f -- "$download"; fi
  rmdir -- "$active" 2>/dev/null || true
}
trap cleanup EXIT
# GitHub ZIP extraction may not retain executable permission; only this local
# test copy is changed. Setup/self-update are disabled in the packaged binary.
chmod u+x ./jabridge
if [[ "$(./jabridge --version)" != "$version" ]]; then
  printf '%s\n' 'This is not the expected test build. Please send me this message.'
  exit 1
fi
printf '\n%s\n' 'For a first test, press Enter.' \
  'If an earlier test stopped, power the base off for a few minutes, reconnect it and wait for its normal screen.'
printf '%s' 'For that checked new attempt, type NEW ATTEMPT: '
mode=''
new_attempt=false
if ! IFS= read -r mode; then printf '%s\n' 'Cancelled. No firmware update was started.'; exit 0; fi
case "$mode" in
  '') ;;
  'NEW ATTEMPT') new_attempt=true ;;
  *) printf '%s\n' 'Cancelled. No firmware update was started.'; exit 0 ;;
esac
if [[ "$new_attempt" == false && ( -e "$marker" || -L "$marker" || -e "$report" ) ]]; then
  printf '%s\n' 'An earlier result is saved. Start again only with NEW ATTEMPT after the base is back to normal.'
  exit 1
fi
if [[ -L "$marker" || ( -e "$marker" && ! -d "$marker" ) ]]; then
  printf '%s\n' 'The saved attempt folder needs checking. Keep the earlier result.'
  exit 1
fi
printf '\n%s\n' \
  'This test installs Engage 75 firmware 5.20.1.' \
  'Keep the headset docked and the base plugged in. End calls and close other Jabra tools.' \
  'The device will restart during the update.' \
  'The base reconnects as a different USB device during the update.' \
  'Recovery after a partially written update has not been tested.' \
  'Your installed Jabridge app will not be replaced.'
printf '\nType UPDATE to accept this firmware test, or press Enter to cancel: '
answer=''
if ! IFS= read -r answer || [[ "$answer" != UPDATE ]]; then
  printf '%s\n' 'Cancelled. No firmware update was started.'
  exit 0
fi

if [[ -L "$report" || -L "$archive" ]]; then
  printf '%s\n' 'The result or firmware file is a link. Extract the test into a fresh folder.'
  exit 1
fi
# Keep every accepted attempt and result. Native validation decides whether
# the saved device/firmware state permits a new attempt; this is no resume.
if [[ "$new_attempt" == true ]]; then
  if [[ ! -d "$marker" ]]; then mkdir -- "$marker"; fi
  mktemp -d "$marker/new-attempt.XXXXXX" >/dev/null
  report="$(mktemp "$bundle_dir/jabridge-engage75-result-new.XXXXXX.txt")"
else
  if ! mkdir -- "$marker"; then
    printf '%s\n' 'The test could not be reserved. No firmware update was started.'
    exit 1
  fi
fi
attempt_args=()
if [[ "$new_attempt" == true ]]; then attempt_args+=(--new-attempt); fi
printf '%s\n' "$version" 'Source: 51ccda9c34901ea420e950b406d1654458ff3fdd' > "$report"
printf '%s\n' 'Getting the official 5.20.1 firmware...' | tee -a "$report"
download="$(mktemp "$bundle_dir/.engage75-download.XXXXXX")"
if ! curl --proto '=https' --proto-redir '=https' --location --fail --silent --show-error \
  --max-time 600 --output "$download" \
  'https://sdkbackend.jabra.com/v4/download/1113/5.20.1' 2>&1 | tee -a "$report"; then
  printf '%s\n' 'The firmware download failed. No firmware update was started.' | tee -a "$report"
  exit 1
fi
if [[ "$(sha256sum "$download" | cut -d ' ' -f 1)" != "$firmware_sha" ]]; then
  printf '%s\n' 'The firmware file did not match the checked package. No update was started.' | tee -a "$report"
  exit 1
fi
mv -- "$download" "$archive"
printf '%s\n' 'Starting the firmware test. Leave the base connected and headset docked.' | tee -a "$report"
export JABRIDGE_HISTORY=off
if ./jabridge firmware install "$archive" --preview-engage75-legacy --i-accept-risk "${attempt_args[@]}" 2>&1 | tee -a "$report"; then
  printf '\nThe test finished. Please attach %s to issue #62.\n' "$(basename -- "$report")" | tee -a "$report"
else
  result=$?
  printf '\n%s\n' 'The test stopped. Leave the base connected and headset docked.' \
    'Keep this result before trying anything else.' | tee -a "$report"
  printf 'Attach %s to issue #62.\n' "$(basename -- "$report")" | tee -a "$report"
  exit "$result"
fi
