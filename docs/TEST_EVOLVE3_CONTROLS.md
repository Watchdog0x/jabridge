# Evolve3 65 controls preview

Testing is optional. Use the separate review build and isolated service steps
in TEST_LINK390.md. The preview does not install itself or update your normal
app. Check its version first:

```bash
./jabridge --version
```

On Evolve3 65 firmware 1.0.1, please try these in the headset Settings menu:

1. Turn Power saving on, off, then on. Check Jabra Plus shows the same state.
2. Change Noise control button cycle. Try one mode, then all three. Check
   Jabra Plus and the headset button follow the chosen cycle.
3. Change one equalizer band to +3 dB, then -6 dB, then 0 dB. Check Jabra Plus
   shows the same values and sound changes. Other bands should stay put.
   Arrows move 1 dB; Shift+arrows move 0.5 dB. Enter saves, Esc cancels.
4. Voice prompt language is shown in Settings. Changing it needs direct USB
   and separate firmware confirmation. Follow TEST_VOICE_LANGUAGE.md only
   if you want to try the untested update path.

The command line offers the same ordinary settings:

```bash
./jabridge settings set headset.power-saving on
./jabridge settings set headset.noise-cycle anc,hearthrough,off
./jabridge settings set headset.equalizer-60hz 3
```

After changing settings, check sound, microphone and reconnecting. Please say
which tests passed and whether settings survive turning the headset off and on.

For Link 390 USB ID 0b0e:2e56, check the remembered-headset list and Find headset.
Put the headset in pairing mode for search. This preview only reads and
searches; it does not offer pairing, forget or reset. Keep saved pairings.

If a setting fails or differs from Jabra Plus, share one latest debug report
using the review service's JABRIDGE_SOCKET as described in TEST_LINK390.md.
No new report is needed if everything works.
