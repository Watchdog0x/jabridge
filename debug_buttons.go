package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
	"golang.org/x/sys/unix"
)

type buttonStep struct {
	Label, Prompt string
	Duration      time.Duration
}

func selectGuidedControls(input io.Reader, prompts io.Writer) ([]buttonStep, error) {
	_, err := fmt.Fprintln(prompts, `Which controls or movements does the device you want to test support?
1 Volume up   2 Volume down   3 Mute   4 Call   5 Play/pause
6 Noise control   7 Wheel up   8 Wheel down   9 Another control
10 Raise mic arm   11 Lower mic arm   12 Slide mic out   13 Slide mic in
Enter numbers, for example 1 2 3. Repeat 9 for each extra control.
Press Enter for no physical controls (or to skip). A dongle may advertise controls that are on the headset.`)
	if err != nil {
		return nil, err
	}
	line, err := bufio.NewReader(io.LimitReader(input, 256)).ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("read control selection: %w", err)
	}
	return guidedControlSteps(line)
}

func guidedControlSteps(selection string) ([]buttonStep, error) {
	controls := []buttonStep{
		{Label: "volume-up", Prompt: "Press and release volume UP once"},
		{Label: "volume-down", Prompt: "Press and release volume DOWN once"},
		{Label: "mute", Prompt: "Press and release MUTE once"},
		{Label: "call", Prompt: "Press and release the CALL button once"},
		{Label: "play-pause", Prompt: "Press and release PLAY/PAUSE once"},
		{Label: "noise-control", Prompt: "Press and release NOISE CONTROL once"},
		{Label: "wheel-up", Prompt: "Turn the wheel a little UP"},
		{Label: "wheel-down", Prompt: "Turn the wheel a little DOWN"},
		{Label: "boom-up", Prompt: "Raise or fold the microphone arm UP"},
		{Label: "boom-down", Prompt: "Lower or unfold the microphone arm DOWN"},
		{Label: "microphone-out", Prompt: "Slide or pull the microphone OUT"},
		{Label: "microphone-in", Prompt: "Slide or retract the microphone IN"},
	}
	fields := strings.Fields(strings.ReplaceAll(selection, ",", " "))
	if len(fields) == 0 || len(fields) == 1 && fields[0] == "0" {
		return nil, nil
	}
	if len(fields) > 16 {
		return nil, fmt.Errorf("choose at most 16 controls in one report")
	}
	steps := []buttonStep{{"idle", "Leave all controls alone", 5 * time.Second}}
	extra := 0
	seen := map[int]bool{}
	for _, field := range fields {
		number, err := strconv.Atoi(field)
		if err != nil || number < 1 || number > 13 {
			return nil, fmt.Errorf("choose numbers 1 to 13, or press Enter to skip")
		}
		var step buttonStep
		if number == 9 {
			extra++
			step = buttonStep{Label: fmt.Sprintf("extra-%d", extra), Prompt: fmt.Sprintf("Use extra control or movement %d once (tell us which in your issue)", extra)}
		} else {
			if seen[number] {
				continue
			}
			seen[number] = true
			index := number - 1
			if number >= 10 {
				index--
			}
			step = controls[index]
		}
		step.Duration = 6 * time.Second
		steps = append(steps, step)
	}
	return steps, nil
}

func debugMediaEvent(kind, code uint16, value int32) string {
	if label := mediaInputEvent(kind, code, value); label != "" {
		return label
	}
	// Only button ranges, never ordinary keyboard keys or raw MSC_SCAN data.
	if kind == unix.EV_KEY && (code >= 0x100 && code <= 0x13f || code >= 0x2c0 && code <= 0x2ff) {
		if state := map[int32]string{0: "released", 1: "pressed", 2: "repeat"}[value]; state != "" {
			return "Unmapped button: " + state
		}
	}
	return ""
}

func writeButtonObservation(ctx context.Context, out *bytes.Buffer, prompts io.Writer, steps []buttonStep) error {
	if len(steps) == 0 {
		fmt.Fprintln(out, "\nPhysical control check: skipped by tester (no physical controls or test skipped). Advertised HID capabilities do not establish physical buttons.")
		return nil
	}
	if _, err := fmt.Fprintln(prompts, "Normal device controls still act. Use one Jabra device at a time. Ctrl+C stops."); err != nil {
		return err
	}
	fmt.Fprintln(out, "\nButton observation: times are relative to each step; step labels are requested actions, not confirmed button meanings.")
	for _, step := range steps {
		if ctx.Err() != nil {
			fmt.Fprintln(out, "Observation cancelled; remaining steps NOT TESTED.")
			break
		}
		if _, err := fmt.Fprintf(prompts, "%s (%ds)...\n", step.Prompt, int(step.Duration/time.Second)); err != nil {
			return err
		}
		fmt.Fprintf(out, "\nStep %s (%ds):\n", step.Label, int(step.Duration/time.Second))
		if err := writeButtonWindow(ctx, out, prompts, step.Duration); err != nil {
			return err
		}
	}
	return nil
}

func writeButtonWindow(ctx context.Context, out *bytes.Buffer, prompts io.Writer, duration time.Duration) error {
	window, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	started := time.Now()
	hidResult := make(chan string, 1)
	go func() { hidResult <- observeHIDActivity(window) }()
	ipcResult := make(chan string, 1)
	go func() { ipcResult <- observeControlIPC(window) }()
	count, omitted := 0, 0
	var promptErr error
	labels := map[string]string{}
	err := observeButtonEvents(window, jabraInputPaths(), func(path string, kind, code uint16, value int32) {
		label := debugMediaEvent(kind, code, value)
		if label == "" {
			return
		}
		count++
		if count > 128 {
			omitted++
			return
		}
		if labels[path] == "" {
			labels[path] = diagnosticInputLabel(path)
		}
		fmt.Fprintf(out, "  at=%dms %s ev-type=%04x code=%04x %s\n", time.Since(started).Milliseconds(), labels[path], kind, code, label)
		if _, err := fmt.Fprintln(prompts, label); err != nil {
			promptErr = err
			cancel()
		}
	}, func(message string) { fmt.Fprintln(out, "  Input access:", message) })
	fmt.Fprint(out, <-hidResult)
	fmt.Fprint(out, <-ipcResult)
	if err != nil {
		fmt.Fprintln(out, "  BLOCKED Linux event observation:", err)
	}
	fmt.Fprintf(out, "  Linux button events=%d omitted=%d. Access errors are not counted as button events.\n", count, omitted)
	return promptErr
}

func observeControlIPC(ctx context.Context) string {
	var out strings.Builder
	started := time.Now()
	connect, cancel := context.WithTimeout(ctx, time.Second)
	client, err := ipc.Dial(connect, ipcSocketPath())
	if err == nil {
		err = client.Subscribe(connect)
	}
	cancel()
	if client != nil {
		defer func() { _ = client.Close() }()
	}
	if err != nil {
		fmt.Fprintln(&out, "  IPC observation unavailable:", ipcDiagnosticFailure(err))
		return out.String()
	}
	count, omitted := 0, 0
	for {
		select {
		case <-ctx.Done():
			fmt.Fprintf(&out, "  IPC events=%d omitted=%d; no event does not prove an absent sensor.\n", count, omitted)
			return out.String()
		case event, ok := <-client.Notifications():
			if !ok {
				fmt.Fprintln(&out, "  IPC disconnected during control observation")
				return out.String()
			}
			switch event.Method {
			case "device.attached", "device.detached", "device.battery.update", "device.pairing.update":
				count++
				if count > 64 {
					omitted++
					continue
				}
				fmt.Fprintf(&out, "  at=%dms IPC event=%s%s\n", time.Since(started).Milliseconds(), event.Method, controlEventIdentity(event.Params))
			}
		}
	}
}

func controlEventIdentity(params any) string {
	fields, ok := params.(map[string]any)
	if !ok {
		return " (payload omitted)"
	}
	pid, pok := fields["pid"].(float64)
	id, iok := fields["id"].(float64)
	if !pok || !iok || pid < 0 || pid > 65535 || pid != float64(uint16(pid)) || id < 0 || id > 65535 || id != float64(uint16(id)) {
		return " (payload omitted)"
	}
	connection, _ := fields["connection"].(string)
	if connection != "usb" && connection != "dongle" {
		connection = "unknown"
	}
	return fmt.Sprintf(" device=%d model=0b0e:%04x connection=%s", uint16(id), uint16(pid), connection)
}
