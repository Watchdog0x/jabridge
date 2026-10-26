# Test the Link 390 fixes

This is a review build, not a release. Download the artifact for your Linux
architecture from the draft PR's checks and extract it into a separate folder.
It cannot install itself or update your installed app.

```sh
chmod +x ./jabridge
./jabridge --version
```

## Search and pairing: PR #53

This test is for Link 390 USB ID `0b0e:2e57`. No firmware update is needed.
Stop your normal Jabridge service and close other device-control apps:

```sh
jabridge service stop
```

In the review-build folder, start its separate service in one terminal:

```sh
JABRIDGE_SOCKET="$XDG_RUNTIME_DIR/jabridge-link390-review.sock" \
  ./jabridge --daemon
```

Open its menu in another terminal:

```sh
JABRIDGE_SOCKET="$XDG_RUNTIME_DIR/jabridge-link390-review.sock" \
  ./jabridge
```

Put the headset in pairing mode, search, select it and connect. If classic and
LE entries both appear, note which one you choose. Report whether it connects
without unplugging the dongle.

If you choose to repeat your earlier reset test, check search and pairing again
afterward. Reset remains disabled in this build. The earlier comparison can be
repeated with the method you already used; it is optional.

Save the latest report using the review service:

```sh
JABRIDGE_SOCKET="$XDG_RUNTIME_DIR/jabridge-link390-review.sock" \
  ./jabridge debug --output jabridge-link390-review.txt
```

When finished, close the review menu and press Ctrl+C in the review service's
terminal. Start your normal service again:

```sh
jabridge service start
```

## Firmware package: issue #54

**This firmware path has never run on physical hardware. It could leave the
dongle needing recovery with Jabra's updater.** Testing is optional; skip it if
you are not prepared for that. Your current firmware can keep being used.

Use the original ZIP already downloaded from Jabra. Keep it unchanged. Run the
review binary from a terminal with its separate firmware opt-in. Replace the
ZIP path below with the location of your downloaded file:

```sh
JABRIDGE_ENABLE_LINK390_CSR7_FIRMWARE=I_ACCEPT_UNTESTED_FIRMWARE \
  ./jabridge firmware install \
  firmware/Jabra_Link_390_v3.2.0_2E50_vector.zip
```

The installer checks the official release and connected dongle, then asks for
confirmation before writing. Keep USB connected. Save the terminal output and
a fresh debug report, and report the firmware version and whether normal
pairing and audio still work.

If the commit is unconfirmed, keep the ZIP and recovery record. Running the
same install again only reads and checks the installed version after a
commit-started record; it never repeats that commit automatically.
