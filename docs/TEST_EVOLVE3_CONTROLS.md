# Evolve3 65 controls preview

Testing is optional. Implemented controls can be edited on matching exact
firmware profiles without a setting-test option. Power-saving and equalizer
writes still need physical testing. Use the extracted binary in a separate
folder; do not run setup or replace your normal installation for this test.
Stop the normal service and close other device-control apps:

```sh
jabridge service stop
./jabridge --version
JABRIDGE_SOCKET="$XDG_RUNTIME_DIR/jabridge-controls-test.sock" ./jabridge --daemon
```

In a second terminal in the same folder:

```sh
JABRIDGE_SOCKET="$XDG_RUNTIME_DIR/jabridge-controls-test.sock" ./jabridge
```

The service decides which controls and values are supported. Client flags do
not override model, firmware, device binding or verification checks.

On Evolve3 65 firmware 1.0.1, please try these in the headset Settings menu:

1. Turn Power saving on, off, then on. Check Jabra Plus shows the same state.
2. Check Noise control button cycle offers only the confirmed choices for
   Evolve3 65 firmware 1.0.1 through Link 390 PID 2e56 firmware 3.2.0:
   HearThrough + ANC + Off, HearThrough + ANC, and HearThrough + Off.
   Check Jabra Plus and the headset button follow each chosen cycle.
   Other setups show the full cycle and the current value only.
3. Change one equalizer band to +3 dB, then -6 dB, then 0 dB. Check Jabra Plus
   shows the same values and sound changes. Other bands should stay put.
   Arrows move 1 dB; Shift+arrows move 0.5 dB. Enter saves, Esc cancels.
4. Voice prompt language is shown in Settings. Changing it needs direct USB
   and separate firmware confirmation. Follow TEST_VOICE_LANGUAGE.md only
   if you want to try the untested update path.

With the test service running, commands use that same socket. For example:

```bash
JABRIDGE_SOCKET="$XDG_RUNTIME_DIR/jabridge-controls-test.sock" ./jabridge settings set headset.power-saving on
JABRIDGE_SOCKET="$XDG_RUNTIME_DIR/jabridge-controls-test.sock" ./jabridge settings set headset.noise-cycle anc,hearthrough,off
JABRIDGE_SOCKET="$XDG_RUNTIME_DIR/jabridge-controls-test.sock" ./jabridge settings set headset.equalizer-60hz 3
```

After changing settings, check sound, microphone and reconnecting. Please say
which tests passed and whether settings survive turning the headset off and on.

Link 390 USB ID 2e56 list/search stay disabled in 1.5.0. These setting tests
use an already-connected headset; do not reset it or change saved pairings.

If a setting fails or differs from Jabra Plus, share one latest debug report
using this test service's JABRIDGE_SOCKET.
No new report is needed if everything works.

When finished, close the menu and press Ctrl+C in the test service terminal.
Then restore your normal service:

```sh
jabridge service start
```
