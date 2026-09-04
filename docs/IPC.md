# IPC in five minutes

Jabridge has a local API for small desktop tools, scripts, and a future tray
applet. It uses one private Unix socket and JSON-RPC 2.0.

The easy commands below need no extra program and no hand-written JSON.

## 1. Set up and start the service

Run setup once. It installs the app, device access, Bash completion, and the
user service. The service is enabled automatically for future sign-ins.

```bash
./jabridge setup
./jabridge service start
```

Check it at any time:

```bash
./jabridge service status
```

The TUI uses this same service, so it is safe to open `jabridge` while the
service is running.

## 2. Check the connection

In a second terminal:

```bash
./jabridge ipc ping
```

The answer should be:

```text
Service is ready.
```

## 3. Read device data

```bash
./jabridge ipc devices
./jabridge ipc battery
./jabridge ipc settings dongle
./jabridge ipc settings headset
```

The result is formatted JSON, so it is also easy for another program to read.
`devices` includes the small device ID used by `select`:

```bash
./jabridge ipc select 1
```

For a direct-USB headset, selection prefers matching headset PipeWire nodes.
For a headset routed through a Link dongle, selection prefers that Link's
PipeWire output and microphone. The simpler CLI wrapper is `jabridge use
usb|dongle`.

Each object returned by `devices.list` includes `selected`. This lets a client
show the same active dongle and headset connection as the service.
The selected headset connection is remembered across service restarts.

## 4. Watch live events

```bash
./jabridge ipc watch
```

Press Ctrl+C to stop. The command keeps its connection alive automatically.
It can print these events:

- `device.attached`
- `device.detached`
- `device.battery.update`
- `device.pairing.update`

## 5. Change a setting

First list the settings and copy a setting key and one of its choices:

```bash
./jabridge ipc settings dongle
./jabridge ipc set dongle auto-pairing on
```

For a headset:

```bash
./jabridge ipc settings headset
./jabridge ipc set headset three-dot-button push-to-talk
```

`ipc set` changes hardware. Jabridge reads the value back before returning
success. Use only a setting listed as editable for the connected model.

## Run it as a user service

Setup already enables the service. These standard system commands are optional:

```bash
systemctl --user enable --now jabridge.service
systemctl --user status jabridge.service
```

Stop it with:

```bash
systemctl --user stop jabridge.service
```

## Socket details

The default socket is:

```text
$XDG_RUNTIME_DIR/jabridge.sock
```

It normally resolves to `/run/user/YOUR_USER_ID/jabridge.sock`. Its permissions
are `0600`, so only the current user can connect. Set `JABRIDGE_SOCKET` to use a
different path with the built-in IPC commands.

Each request and response is one JSON object followed by a newline. A minimal
request looks like this:

```json
{"jsonrpc":"2.0","id":1,"method":"service.ping"}
```

The response is:

```json
{"jsonrpc":"2.0","id":1,"result":{"ok":true}}
```

## Methods

| Method | Purpose | Changes hardware? |
| --- | --- | --- |
| `service.ping` | Health check | No |
| `service.shutdown` | Stop a portable non-systemd service | Stops the service |
| `version` | Service version | No |
| `devices.list` | Connected devices | No |
| `diagnostics.device` | Read-only native checks for numeric device `id` | No |
| `device.select` | Choose a control connection and matching PipeWire audio | No |
| `device.battery` | Battery state | No |
| `device.firmware` | Installed firmware version | No |
| `device.features` | Basic feature flags | No |
| `settings.list` | Supported settings and choices | No |
| `settings.set` | Change one setting | Yes |
| `bt.list` | Remembered headsets | No |
| `bt.search` | Search for a headset | Yes |
| `bt.search.results` | Read current search results | No |
| `bt.search.connect` | Connect one search result | Yes |
| `bt.connect` | Connect a remembered headset | Yes |
| `bt.disconnect` | Disconnect a remembered headset | Yes |
| `bt.forget` | Remove a remembered headset | Yes, destructive |
| `bt.pair` | Start or stop pairing | Yes |
| `bt.autopair` | Read or change auto pairing | Only when changing it |
| `device.busylight` | Read or change busylight mode | Only when changing it |
| `device.reset` | Factory reset | Yes, destructive |
| `subscribe` | Start event notifications | No |

## Small Go example

```go
package main

import (
    "context"
    "fmt"
    "time"

    "github.com/Watchdog0x/jabridge/daemon"
    "github.com/Watchdog0x/jabridge/daemon/ipc"
)

func main() {
    ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
    defer cancel()

    client, err := ipc.Dial(ctx, daemon.DefaultConfig().SocketPath)
    if err != nil {
        panic(err)
    }
    defer client.Close()

    var devices []ipc.DeviceInfo
    if err := client.Call(ctx, "devices.list", nil, &devices); err != nil {
        panic(err)
    }
    fmt.Println(devices)
}
```

## Current preview boundary

The service, event bus, client, settings methods, keepalive, automatic start,
and TUI reconnect flow are implemented. The TUI uses IPC and never receives a
`hidraw` path. A few legacy CLI hardware commands still run directly; Jabridge
automatically stops the service for that one command and starts it again when
the command finishes. Moving those last commands onto IPC is follow-up work.
