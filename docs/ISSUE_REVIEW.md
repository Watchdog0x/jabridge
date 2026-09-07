# Issue review — 7 September 2026

All 13 issues open at the start of this review were checked, including their
latest replies. RC18 addresses concrete transport, settings and CLI defects.
Source implementation and real-device confirmation are separate steps.

| Issue | Current result | Remaining work |
| --- | --- | --- |
| [#3](https://github.com/Watchdog0x/jabridge/issues/3) Speak 510 | Tester confirms USB/TUI detection, permissions and volume events. RC18 adds descriptor-based management, settings writes and corrected variant/ACK parsing. | Confirm actual variant, installed firmware and reversible settings on Speak 510. Protocol-1 firmware updater remains unimplemented. |
| [#4](https://github.com/Watchdog0x/jabridge/issues/4) System Bluetooth | HID evidence collection exists; USB/Link support is separate. | BlueZ/device control implementation and physical testing. |
| [#5](https://github.com/Watchdog0x/jabridge/issues/5) Engage 50 II | RC19 reports confirm HID/input access but show pre-exec `218/CAPABILITIES`. RC20 corrects the bundled user unit and explains the failure. [Screenshot/settings comparison](ENGAGE_SETTINGS.md) records remaining controls. | Confirm RC20 startup on the affected Ubuntu host, then actual native reads/settings and call control with/without the controller. |
| [#9](https://github.com/Watchdog0x/jabridge/issues/9) Desktop applet | Service subscriptions are available. | Applet implementation. |
| [#17](https://github.com/Watchdog0x/jabridge/issues/17) Evolve 75e | Generic discovery and debug paths exist. | Current hardware report. |
| [#25](https://github.com/Watchdog0x/jabridge/issues/25) Evolve2 30 SE | USB-only discovery exists; no dongle requirement. | Current hardware confirmation. |
| [#34](https://github.com/Watchdog0x/jabridge/issues/34) Test reports | RC17 Speak report received; RC18 source/regression work follows it. | Independent headset battery/settings and everyday-use tests. |
| [#35](https://github.com/Watchdog0x/jabridge/issues/35) Service ownership | RC18 routes status, battery, settings, model and diagnose through an existing service, without selecting a different device or restarting it. | Firmware-exclusive service workflow and remaining standalone paths. |
| [#36](https://github.com/Watchdog0x/jabridge/issues/36) Write/recovery tests | Link 380 auto-pairing OFF/ON with read-back and restoration passed on the new transport. | Headset writes, pairing/reset and firmware recovery qualification. |
| [#38](https://github.com/Watchdog0x/jabridge/issues/38) PipeWire IPC | Existing meeting monitor runs in the service. | Move direct sound controls into IPC and add the Sound view. |
| [#39](https://github.com/Watchdog0x/jabridge/issues/39) Release roadmap | This review records the current gaps. | Stable release qualification on explicitly supported models. |
| [#40](https://github.com/Watchdog0x/jabridge/issues/40) Evolve3 | Model data and control definitions exist. | Physical reads/writes and firmware protocols 16/17. |
| [#41](https://github.com/Watchdog0x/jabridge/issues/41) Devuan installer | Closed as a legacy jLink 0.1.1 installer report superseded by the native rewrite; reporter directed to the preview. | Investigate a new report if native setup fails on Devuan. No legacy installer patch was merged. |

The rewrite remains a preview in PR #33. Hardware issues are kept open until
the affected model confirms the implemented behavior.
