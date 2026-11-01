# Link 390 configuration ownership

The owner is one physical USB port. Both Link 390 IDs, `2e56` and `2e57`,
participate in coordination. Write eligibility is a separate, stricter model
and firmware check. Other devices retain their idle open behavior.

Lock order: API mutation mutex, device-access shared lease, settings-write
exclusive lease, port permit, then the GNP packet mutex. Never wait for a port
while holding the packet mutex. The port gate is process-local; the existing
device/settings leases provide their own cross-process coordination.

| Path | Coordination |
| --- | --- |
| `jabraApi.go` management opens | Parent resolved before/after open. Scoped handles retain a shared permit until close. |
| `gnpQueryWithPayloadTimeout`, `gnpCommand` | Borrow a valid handle permit or acquire a permit before the packet mutex. Unresolved exchanges are counted and drained too. |
| `refreshDongleChildDevice` | Acquire a parent-port permit before the packet mutex and direct firmware transport; busy does not count as a child miss. |
| `enrichUSBDevice`, battery polling | Defer while owned. Typed busy errors preserve cached data rather than clearing it. |
| USB registry removal | Retain the owned USB slot across the expected disappearance; removal resumes after ownership ends. |
| `control_parts.go` | Management probes use the coordinated opener and packet functions. |
| `paired_devices.go` | Remembered-device actions use the coordinated query/command callbacks; no independent native transport. |
| `voice_language.go`, ordinary setting reads | Use `settingTransport`, which preserves typed busy errors. |
| Bound headset search | Acquire a search permit before capturing USB binding; retain it until once-only Close. Every bound exchange checks validity before the packet mutex. |
| Captured pairing after search | Acquire a new ordinary permit after discovery stops and retain it through pairing/confirmation. |
| Legacy search | Non-scoped devices retain their existing path. |
| `debug_hid_activity.go` observation | Read-only file descriptors, no GNP commands. Exempt from draining so a long observation cannot block configuration indefinitely. |
| `daemon/buttons/linux.go` | Read-only HID input handles, no GNP writes. Exempt for the same reason. |

An unresolved handle must resolve its parent again at an exchange. If it
cannot, its exchange uses the unbound-reader count while idle and is refused
when an owner/waiter exists. This closes the race between checking idle state
and acquiring the packet mutex. Non-scoped devices with known parents remain
independent of another port's owner.

Third-party control applications are outside Jabridge's advisory coordination.
No new configuration writes are enabled merely by installing this gate.
