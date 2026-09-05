package ipc

import "sync"

type EventBus struct {
	mu          sync.RWMutex
	nextID      uint64
	subscribers map[uint64]chan Notification
}

func NewEventBus() *EventBus {
	return &EventBus{subscribers: make(map[uint64]chan Notification)}
}

func (bus *EventBus) Subscribe() (<-chan Notification, func()) {
	if bus == nil {
		channel := make(chan Notification)
		close(channel)
		return channel, func() {}
	}
	bus.mu.Lock()
	bus.nextID++
	id := bus.nextID
	channel := make(chan Notification, 32)
	bus.subscribers[id] = channel
	bus.mu.Unlock()
	var once sync.Once
	return channel, func() {
		once.Do(func() {
			bus.mu.Lock()
			if current, exists := bus.subscribers[id]; exists {
				delete(bus.subscribers, id)
				close(current)
			}
			bus.mu.Unlock()
		})
	}
}

func (bus *EventBus) Publish(method string, params interface{}) {
	if bus == nil {
		return
	}
	notification := Notification{JSONRPC: "2.0", Method: method, Params: params}
	bus.mu.RLock()
	defer bus.mu.RUnlock()
	for _, subscriber := range bus.subscribers {
		select {
		case subscriber <- notification:
		default:
			// Keep recent state useful for slow clients. Dropping one stale event
			// is preferable to blocking the hardware owner.
			select {
			case <-subscriber:
			default:
			}
			select {
			case subscriber <- notification:
			default:
			}
		}
	}
}

func (bus *EventBus) SubscriberCount() int {
	if bus == nil {
		return 0
	}
	bus.mu.RLock()
	defer bus.mu.RUnlock()
	return len(bus.subscribers)
}
