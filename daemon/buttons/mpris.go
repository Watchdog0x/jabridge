package buttons

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/godbus/dbus/v5"
)

var ErrNoChange = errors.New("media already in requested state")

// PlayPause uses the user's native D-Bus session. No playerctl, subprocess,
// keyboard injection, track metadata, or network service is involved.
func PlayPause(ctx context.Context) error {
	return ControlMedia(ctx, "PlayPause")
}

func ControlMedia(ctx context.Context, action string) error {
	if action != "PlayPause" && action != "Pause" && action != "Play" {
		return errors.New("unsupported media action")
	}
	address, err := sessionBusAddress()
	if err != nil {
		return err
	}
	conn, err := dbus.Connect(address, dbus.WithContext(ctx))
	if err != nil {
		return errors.New("session bus unavailable")
	}
	defer func() { _ = conn.Close() }()
	var names []string
	if err := conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.ListNames", 0).Store(&names); err != nil {
		return errors.New("cannot list media players")
	}
	sort.Strings(names)
	best, rank := "", 0
	already := false
	for _, name := range names {
		if !strings.HasPrefix(name, "org.mpris.MediaPlayer2.") {
			continue
		}
		var owner string
		if err := conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.GetNameOwner", 0, name).Store(&owner); err != nil {
			continue
		}
		object := conn.Object(owner, dbus.ObjectPath("/org/mpris/MediaPlayer2"))
		var properties map[string]dbus.Variant
		if err := object.CallWithContext(ctx, "org.freedesktop.DBus.Properties.GetAll", 0, "org.mpris.MediaPlayer2.Player").Store(&properties); err != nil {
			continue
		}
		canControl, _ := properties["CanControl"].Value().(bool)
		canPause, _ := properties["CanPause"].Value().(bool)
		status, _ := properties["PlaybackStatus"].Value().(string)
		candidate := playerRank(status, canControl, canPause)
		if candidate > 0 && (action == "Pause" && status == "Paused" || action == "Play" && status == "Playing") {
			already = true
		}
		if action == "Pause" && status != "Playing" {
			candidate = 0
		}
		if action == "Play" && status != "Paused" {
			candidate = 0
		}
		if candidate > rank {
			best, rank = owner, candidate
		}
	}
	if best == "" {
		if already {
			return ErrNoChange
		}
		return errors.New("no controllable playing or paused media player")
	}
	// Address the unique owner, not a well-known name that can be replaced.
	return conn.Object(best, dbus.ObjectPath("/org/mpris/MediaPlayer2")).CallWithContext(ctx, "org.mpris.MediaPlayer2.Player."+action, 0).Err
}

func sessionBusAddress() (string, error) {
	address := os.Getenv("DBUS_SESSION_BUS_ADDRESS")
	if address == "" {
		address = fmt.Sprintf("unix:path=/run/user/%d/bus", os.Getuid())
	}
	for _, part := range strings.Split(address, ";") {
		if !strings.HasPrefix(part, "unix:") {
			return "", errors.New("only a local Unix session bus is supported")
		}
	}
	return address, nil
}

func playerRank(status string, canControl, canPause bool) int {
	if !canControl || !canPause {
		return 0
	}
	if status == "Playing" {
		return 2
	}
	if status == "Paused" {
		return 1
	}
	return 0
}
