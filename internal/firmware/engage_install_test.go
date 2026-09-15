package firmware

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestEngageFullUpdateAndControllerActivation(t *testing.T) {
	for _, controller := range []bool{false, true} {
		t.Run(fmt.Sprint(controller), func(t *testing.T) {
			w := makeEngageWorld(controller)
			state := firmwareRecoveryState{ArchiveSHA256: "synthetic"}
			saved := false
			w.checkpoint = func() bool { return saved }
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err := runSitelInstall(ctx, w, w.device(), w.images, "4.1.3", &state, func() error { saved = true; return nil }, nil)
			if err != nil || w.version != "4.1.3" || w.writes == 0 || w.bootMode {
				t.Fatal(err, w.writes)
			}
			if controller && (w.controllerVersion != "4.1.3" || w.activations != 1) {
				t.Fatal("controller did not activate")
			}
			if controller && w.unsubscribes != 1 {
				t.Fatal("controller event subscription not cleaned up")
			}
			if !controller && w.activations != 0 {
				t.Fatal("absent controller activated")
			}
		})
	}
}

func TestEngageInterruptedBootloaderRecovery(t *testing.T) {
	w := makeEngageWorld(true)
	w.failWrite = 3
	state := firmwareRecoveryState{ArchiveSHA256: "synthetic"}
	save := func() error { return nil }
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := runSitelInstall(ctx, w, w.device(), w.images, "4.1.3", &state, save, nil); err == nil || !w.bootMode || state.Phase != "flashing" {
		t.Fatal("expected interrupted transfer", err, state.Phase)
	}
	w.failWrite = 0
	if err := runSitelInstall(ctx, w, w.device(), w.images, "4.1.3", &state, save, nil); err != nil {
		t.Fatal("recovery failed", err)
	}
	if w.version != "4.1.3" || w.controllerVersion != "4.1.3" {
		t.Fatal("recovery not verified")
	}
}

func TestEngageUpdateFailuresStayRecoverable(t *testing.T) {
	for _, name := range []string{"checkpoint", "geometry", "image-id", "verify", "controller", "port", "cancel"} {
		t.Run(name, func(t *testing.T) {
			w := makeEngageWorld(true)
			state := firmwareRecoveryState{ArchiveSHA256: "synthetic"}
			save := func() error { return nil }
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			switch name {
			case "checkpoint":
				save = func() error { return errors.New("disk full") }
			case "geometry":
				w.badGeometry = true
			case "image-id":
				w.wrongImageID = true
			case "verify":
				w.badVerify = true
			case "controller":
				w.badController = true
			case "port":
				w.wrongPort = true
			case "cancel":
				cancel()
			}
			if err := runSitelInstall(ctx, w, w.device(), w.images, "4.1.3", &state, save, nil); err == nil {
				t.Fatal("failure accepted")
			}
			if (name == "checkpoint" || name == "geometry" || name == "image-id" || name == "port" || name == "cancel") && w.writes != 0 {
				t.Fatal("wrote after preflight failure")
			}
			if name == "controller" {
				before := w.writes
				w.badController = false
				if err := runSitelInstall(ctx, w, w.device(), w.images, "4.1.3", &state, save, nil); err != nil || w.writes != before {
					t.Fatal("controller recovery repeated headset flash", err)
				}
			}
		})
	}
}
