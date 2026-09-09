package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Watchdog0x/jabridge/daemon/buttons"
	"github.com/Watchdog0x/jabridge/daemon/ipc"
)

func runButtonServiceCommand(command string) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	dialCtx, stop := context.WithTimeout(ctx, 3*time.Second)
	client, err := ipc.Dial(dialCtx, ipcSocketPath())
	stop()
	if err != nil {
		return errors.New("service is not ready; run jabridge service start")
	}
	defer func() { _ = client.Close() }()
	return buttonServiceCommand(ctx, client, command, os.Stdout)
}

func buttonServiceCommand(ctx context.Context, client *ipc.Client, command string, out io.Writer) error {
	if command == "watch" {
		// Subscribe first so events emitted while fetching the initial state
		// are already queued for this connection.
		sub, stop := context.WithTimeout(ctx, 3*time.Second)
		err := client.Subscribe(sub)
		stop()
		if err != nil {
			return err
		}
		probe, stopProbe := context.WithTimeout(ctx, 3*time.Second)
		var state buttons.Status
		err = client.Call(probe, "buttons.status", nil, &state)
		stopProbe()
		if err != nil {
			return err
		}
		if state.Error != "" {
			return errors.New(state.Error)
		}
		keepalive := time.NewTicker(15 * time.Second)
		defer keepalive.Stop()
		encoder := json.NewEncoder(out)
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-client.Done():
				return errors.New("service disconnected; start the button watch again")
			case event := <-client.Notifications():
				if event.Method == "device.button" || event.Method == "device.signal" || event.Method == "media.action" || event.Method == "buttons.changed" {
					if err := encoder.Encode(event); err != nil {
						return err
					}
				}
			case <-keepalive.C:
				ping, stop := context.WithTimeout(ctx, 3*time.Second)
				err := client.Ping(ping)
				stop()
				if err != nil {
					return err
				}
			}
		}
	}
	method := "buttons.status"
	var params any
	if command == "on" || command == "off" {
		mode := "off"
		if command == "on" {
			mode = "play-pause"
		}
		method = "buttons.configure"
		params = map[string]string{"mode": mode}
	}
	callCtx, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	var state buttons.Status
	if err := client.Call(callCtx, method, params, &state); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "Music button control: %s\n", state.Mode); err != nil {
		return err
	}
	if state.Error != "" {
		return errors.New(state.Error)
	}
	for _, source := range state.Sources {
		state := "ready"
		if !source.Ready {
			state = source.Error
		}
		if _, err := fmt.Fprintf(out, "0b0e:%04x (%s): %s; %d mapped fields\n", source.PID, source.Connection, state, len(source.Controls)); err != nil {
			return err
		}
	}
	if len(state.Sources) == 0 {
		if _, err := fmt.Fprintln(out, "No button sources found yet."); err != nil {
			return err
		}
	}
	if command == "on" {
		_, err := fmt.Fprintln(out, "Only matching Link 380 play/pause fields are handled. Other keys stay with the desktop.\nMusic control pauses while a Jabra microphone is active. Service restart turns it off.")
		return err
	}
	return nil
}
