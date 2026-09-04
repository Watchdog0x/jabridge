# Headset settings from PR #27

PR #27 is the SDK-backed reference tested by delacor on an Evolve2 65. This
table compares the screenshot with native Go settings. A native definition
does not mean its operation has passed on a real Evolve2 65.

| Screenshot setting | Native setting | Status |
| --- | --- | --- |
| Answer call by rotating boom arm | boom-arm-answer | Implemented; retest needed |
| Boom arm mute | boom-arm-action | Implemented; retest needed |
| Boom arm guidance | boom-arm-guidance | Implemented; retest needed |
| Audio guidance | voice-prompts | Implemented; retest needed |
| Audio protection | audio-protection | Implemented; retest needed |
| Auto reject call | auto-reject-call | Implemented; retest needed |
| Auto sleep | auto-sleep | Implemented; retest needed |
| Button sounds | button-sounds | 0/255 encoding implemented; retest needed |
| Headset name | headset-name | CLI editing; TUI text editor missing |
| Headset busylight | in-call-busylight | Implemented; retest needed |
| Mute reminder tone | mute-reminder | Implemented; retest needed |
| Pairing list | No headset-list equivalent | Missing; dongle list is a different database |
| Auto resume audio by motion detection | auto-pause-music is a related candidate | Exact semantic mapping and device read-back needed |
| SideTone | sidetone | Implemented; retest needed |
| SideTone level | sidetone-level | Implemented; retest needed |
| Tone setting | sound-mode | Implemented; retest needed |
| Firmware upgrade lock | firmware-upgrade-lock | Implemented; retest needed |
| Prioritize computer audio | prioritize-computer-audio | Implemented; retest needed |

Other gaps: PR #27 has a separate value picker, grouped settings, help text,
restart hints and runtime protection-state handling. The native TUI currently
cycles choices. It respects explicit read-only catalog access, but runtime
protection discovery is not complete.

The debug report lists every exposed model property, its setting ID, choices,
type, access and restart metadata. Unknown fields remain unknown. Native read
results are shown separately; undocumented settings cannot be discovered by
guessing commands. No vendor runtime is required on the tester's machine.

[Reference PR and hardware evidence](https://github.com/Watchdog0x/jabridge/pull/27)
and [native test reports](https://github.com/Watchdog0x/jabridge/issues/34).
