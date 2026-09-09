package buttons

import (
	"bufio"
	"context"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

type testPlayer struct {
	calls  atomic.Int32
	pauses atomic.Int32
	plays  atomic.Int32
}

func (p *testPlayer) PlayPause() *dbus.Error { p.calls.Add(1); return nil }
func (p *testPlayer) Pause() *dbus.Error     { p.pauses.Add(1); return nil }
func (p *testPlayer) Play() *dbus.Error      { p.plays.Add(1); return nil }

type testProperties struct{ status string }

func (p testProperties) GetAll(iface string) (map[string]dbus.Variant, *dbus.Error) {
	return map[string]dbus.Variant{"PlaybackStatus": dbus.MakeVariant(p.status), "CanControl": dbus.MakeVariant(true), "CanPause": dbus.MakeVariant(true)}, nil
}

func TestPlayPauseUsesIsolatedNativeDBusAndSelectsPlayingPlayer(t *testing.T) {
	binary, err := exec.LookPath("dbus-daemon")
	if err != nil {
		t.Skip("dbus-daemon unavailable for isolated protocol test")
	}
	command := exec.Command(binary, "--session", "--nofork", "--print-address=1", "--nopidfile")
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = command.Process.Kill(); _ = command.Wait() })
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() {
		t.Fatal("no isolated bus address")
	}
	address := scanner.Text()
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", address)
	players := []*testPlayer{{}, {}}
	for i, status := range []string{"Paused", "Playing"} {
		conn, err := dbus.Connect(address)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		if _, err := conn.RequestName("org.mpris.MediaPlayer2.test"+status, dbus.NameFlagDoNotQueue); err != nil {
			t.Fatal(err)
		}
		if err := conn.Export(players[i], "/org/mpris/MediaPlayer2", "org.mpris.MediaPlayer2.Player"); err != nil {
			t.Fatal(err)
		}
		if err := conn.Export(testProperties{status}, "/org/mpris/MediaPlayer2", "org.freedesktop.DBus.Properties"); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := PlayPause(ctx); err != nil {
		t.Fatal(err)
	}
	if players[0].calls.Load() != 0 || players[1].calls.Load() != 1 {
		t.Fatal("wrong media player selected")
	}
	if err := ControlMedia(ctx, "Pause"); err != nil {
		t.Fatal(err)
	}
	if players[0].calls.Load() != 0 || players[0].plays.Load() != 0 || players[1].pauses.Load() != 1 {
		t.Fatal("Pause resumed a player or used toggle")
	}
	if err := ControlMedia(ctx, "Play"); err != nil {
		t.Fatal(err)
	}
	if players[0].plays.Load() != 1 || players[1].plays.Load() != 0 {
		t.Fatal("Play selected a non-paused player")
	}
}

func TestPlayerRankAndLocalBusGuard(t *testing.T) {
	if playerRank("Stopped", true, true) != 0 || playerRank("Playing", false, true) != 0 || playerRank("Playing", true, false) != 0 || playerRank("Paused", true, true) != 1 {
		t.Fatal("player capabilities ignored")
	}
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "tcp:host=example.invalid,port=42")
	if _, err := sessionBusAddress(); err == nil {
		t.Fatal("network bus allowed")
	}
}
