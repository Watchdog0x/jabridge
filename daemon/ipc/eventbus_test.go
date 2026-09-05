package ipc

import "testing"

func TestEventBusSubscribePublishCancel(t *testing.T) {
	bus := NewEventBus()
	events, cancel := bus.Subscribe()
	if bus.SubscriberCount() != 1 {
		t.Fatalf("subscriber count = %d", bus.SubscriberCount())
	}
	bus.Publish("device.attached", map[string]int{"pid": 1})
	event := <-events
	if event.Method != "device.attached" {
		t.Fatalf("event = %#v", event)
	}
	cancel()
	if bus.SubscriberCount() != 0 {
		t.Fatalf("subscriber remains after cancel")
	}
	if _, open := <-events; open {
		t.Fatal("subscriber channel remains open")
	}
}

func TestEventBusSlowSubscriberDoesNotBlock(t *testing.T) {
	bus := NewEventBus()
	events, cancel := bus.Subscribe()
	defer cancel()
	for index := 0; index < 100; index++ {
		bus.Publish("device.battery.update", index)
	}
	select {
	case event := <-events:
		if event.Method != "device.battery.update" {
			t.Fatalf("event = %#v", event)
		}
	default:
		t.Fatal("slow subscriber received no recent event")
	}
}
