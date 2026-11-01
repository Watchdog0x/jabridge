package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestControlOwnershipDrainsReadersAndDefersNewExchanges(t *testing.T) {
	var gate controlGate
	reader, err := gate.shared("/usb/1-1", false)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	owned := make(chan *controlPermit, 1)
	failure := make(chan error, 1)
	go func() {
		permit, err := gate.exclusive(ctx, "/usb/1-1", "/sys/bus/usb/devices/1-1")
		if err != nil {
			failure <- err
			return
		}
		owned <- permit
	}()
	for !gate.busy("/usb/1-1") {
		if ctx.Err() != nil {
			t.Fatal(ctx.Err())
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := gate.shared("/usb/1-1", false); !errors.Is(err, errControlBusy) {
		t.Fatalf("new read did not defer: %v", err)
	}
	other, err := gate.shared("/usb/1-2", false)
	if err != nil {
		t.Fatal("unrelated dongle blocked", err)
	}
	other.release()
	select {
	case <-owned:
		t.Fatal("exclusive owner started before the old exchange finished")
	default:
	}
	reader.release()
	var owner *controlPermit
	select {
	case owner = <-owned:
	case err := <-failure:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if !owner.valid() || !gate.busy("/sys/bus/usb/devices/1-1") {
		t.Fatal("owner/alias was lost during the transaction")
	}
	owner.release()
	owner.release()
	if owner.valid() || gate.busy("/usb/1-1") {
		t.Fatal("closed owner still active")
	}
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if len(gate.ports) != 0 || len(gate.aliases) != 0 {
		t.Fatal("idle ports retained")
	}
}

func TestControlSearchLifetimePreventsConfiguration(t *testing.T) {
	var gate controlGate
	search, err := gate.shared("/usb/1-1", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gate.exclusive(context.Background(), "/usb/1-1", ""); !errors.Is(err, errSearchActive) {
		t.Fatalf("configuration was allowed during search: %v", err)
	}
	search.release()
	owner, err := gate.exclusive(context.Background(), "/usb/1-1", "")
	if err != nil {
		t.Fatal(err)
	}
	owner.release()
}

func TestControlCancelledWaiterReleasesOwnership(t *testing.T) {
	var gate controlGate
	reader, err := gate.shared("/usb/1-1", false)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := gate.exclusive(ctx, "/usb/1-1", "/alias"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if gate.busy("/alias") || gate.busy("/usb/1-1") {
		t.Fatal("cancelled waiter left the dongle busy")
	}
	reader.release()
}

func TestControlExchangesAndOwnersDoNotRace(t *testing.T) {
	var gate controlGate
	var workers sync.WaitGroup
	for index := 0; index < 8; index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for attempt := 0; attempt < 30; attempt++ {
				permit, err := gate.shared("/usb/1-1", false)
				if err == nil {
					_ = permit.valid()
					permit.release()
				}
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				owner, err := gate.exclusive(ctx, "/usb/1-1", "")
				if err == nil {
					if !owner.valid() {
						t.Error("new owner is already invalid")
					}
					owner.release()
				}
				cancel()
			}
		}()
	}
	workers.Wait()
}
