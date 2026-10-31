package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
	"github.com/Watchdog0x/jabridge/internal/btsearch"
	"github.com/Watchdog0x/jabridge/internal/firmware"
	"golang.org/x/sys/unix"
)

var nativeSearch struct {
	sync.Mutex
	parent    context.Context
	session   *btsearch.Session
	target    *jabra_DeviceInfo
	binding   *firmware.ControlBinding
	token     string
	lastError string
}

func searchForNewDevices() (resultErr error) {
	nativeSearch.Lock()
	defer nativeSearch.Unlock()
	defer func() {
		if resultErr != nil {
			nativeSearch.lastError = resultErr.Error()
		}
	}()
	if nativeSearch.parent == nil || nativeSearch.parent.Err() != nil {
		return fmt.Errorf("device search requires the running service: %w", ErrNotSupported)
	}
	if nativeSearch.session != nil {
		select {
		case <-nativeSearch.session.Done():
		default:
			return errors.New("a headset search is already running")
		}
	}
	nativeSearch.session, nativeSearch.target, nativeSearch.token, nativeSearch.lastError = nil, nil, "", ""
	nativeSearch.binding = nil
	dongle, err := selectedDongleDevice()
	if err != nil {
		return err
	}
	if !supportsValidatedPairingReads(dongle.productID) {
		return fmt.Errorf("native search is not implemented for dongle %04x", dongle.productID)
	}
	if !dongle.gnpDestinationKnown || dongle.hidrawPath == "" {
		return errors.New("dongle control is not ready; wait for device detection")
	}
	reader, err := openSearchLink(dongle)
	if err != nil {
		return err
	}
	start := btsearch.Start
	if usesDualModeSearch(dongle) {
		start = btsearch.StartDualMode
	}
	session, err := start(nativeSearch.parent, reader, 25*time.Second)
	if err != nil {
		return fmt.Errorf("start headset search: %w", err)
	}
	nativeSearch.session, nativeSearch.target, nativeSearch.token = session, dongle, newDeviceInstance()
	nativeSearch.binding = reader.binding
	return nil
}

func stopNativeSearch() error {
	nativeSearch.Lock()
	session := nativeSearch.session
	nativeSearch.Unlock()
	if session == nil {
		return nil
	}
	return session.Stop()
}

func searchMatchesCurrent(target *jabra_DeviceInfo) bool {
	if target == nil {
		return false
	}
	current := deviceForID(target.deviceID)
	return current != nil && current.instance == target.instance && current.hidrawPath == target.hidrawPath && current.productID == target.productID
}

func currentNativeSearch() (ipc.SearchState, []btsearch.Device) {
	nativeSearch.Lock()
	defer nativeSearch.Unlock()
	if nativeSearch.session == nil {
		state := ipc.SearchState{State: "idle", Error: nativeSearch.lastError}
		if state.Error != "" {
			state.State = "failed"
		}
		return state, nil
	}
	state := ipc.SearchState{Session: nativeSearch.token, DeviceID: nativeSearch.target.deviceID}
	if !searchMatchesCurrent(nativeSearch.target) {
		state.State = "disconnected"
		return state, nil
	}
	snapshot := nativeSearch.session.Snapshot()
	state.State, state.Count, state.Error = snapshot.State, len(snapshot.Devices), snapshot.Error
	if usesDualModeSearch(nativeSearch.target) {
		for index := range snapshot.Devices {
			snapshot.Devices[index].Name = typedSearchName(snapshot.Devices[index])
		}
	}
	return state, snapshot.Devices
}

func connectSearchDeviceBound(index int, token string) error {
	nativeSearch.Lock()
	session, target, currentToken := nativeSearch.session, nativeSearch.target, nativeSearch.token
	binding, parent := nativeSearch.binding, nativeSearch.parent
	nativeSearch.Unlock()
	if session == nil || len(token) != 32 || token != currentToken {
		return fmt.Errorf("search has changed; search again before connecting: %w", ErrNotSupported)
	}
	selected, err := selectedDongleDevice()
	if err != nil {
		return err
	}
	if selected.instance != target.instance || !searchMatchesCurrent(target) {
		return errors.New("selected dongle changed; search again")
	}
	policy := pairingPolicyFor(selected)
	if !policy.SearchConnect {
		return fmt.Errorf("connecting search results is not enabled for dongle PID 0x%04x: %w", selected.productID, ErrNotSupported)
	}
	state := session.Snapshot()
	if state.State != "searching" && state.State != "complete" {
		return errors.New("search is no longer available; search again")
	}
	if index < 0 || index >= len(state.Devices) {
		return errors.New("search result no longer exists")
	}
	device := state.Devices[index]
	if policy.BoundSearch && policy.Wire == searchWireLegacy {
		if _, err := btsearch.LegacyPairCommand(device); err != nil {
			return err
		}
	}
	if err := session.Stop(); err != nil {
		return fmt.Errorf("stop discovery before connecting: %w", err)
	}
	if !searchMatchesCurrent(target) {
		return errors.New("dongle disconnected before pairing")
	}
	if policy.BoundSearch {
		if binding == nil || parent == nil {
			return errors.New("search has no captured USB attachment; search again")
		}
		link, err := openBoundSearchLink(target, binding)
		if err != nil {
			return err
		}
		defer func() { _ = link.Close() }()
		link.pairing = true
		ctx, cancel := context.WithTimeout(parent, 30*time.Second)
		defer cancel()
		pair := btsearch.PairDualMode
		if policy.Wire == searchWireLegacy {
			pair = btsearch.PairLegacyBound
		}
		if err := pair(ctx, link, device); err != nil {
			return err
		}
		requestUIRedraw()
		return nil
	}
	h := openDeviceHidraw(target)
	if h == nil {
		return errors.New("cannot open dongle to connect headset")
	}
	payload := append([]byte{0x04}, device.Address[:]...)
	err = gnpCommand(h, gnpSrcDongle, nextSeq(), gnpClassPairingDevice, gnpOpBluetoothPair, payload)
	h.close()
	if err != nil {
		return fmt.Errorf("connect selected headset: %w", err)
	}
	return waitForRememberedDevice(device.Address, true, true, 15*time.Second)
}

func getSearchDeviceList(deviceID uint16) *pairingList {
	state, devices := currentNativeSearch()
	result := &pairingList{listType: searchComplete}
	if state.DeviceID != deviceID {
		return result
	}
	if state.State == "searching" || state.State == "starting" {
		result.listType = searchResult
	}
	for _, device := range devices {
		result.pairedDevices = append(result.pairedDevices, pairedDevice{deviceName: device.Name, deviceBTAddr: device.Address, bluetoothType: device.BluetoothType})
	}
	result.count = uint16(len(result.pairedDevices))
	return result
}

type searchLink struct {
	device    *jabra_DeviceInfo
	binding   *firmware.ControlBinding
	pairing   bool
	file      *os.File
	assembler *firmware.ControlPacketAssembler
}

func openSearchLink(device *jabra_DeviceInfo) (*searchLink, error) {
	if pairingPolicyFor(device).BoundSearch {
		binding, err := firmware.BindControlDevice(firmware.USBDevice{SysPath: device.usbDevicePath, VendorID: device.vendorID, ProductID: device.productID})
		if err != nil {
			return nil, err
		}
		return openBoundSearchLink(device, binding)
	}
	layout, err := firmware.InspectControlLayout(device.hidrawPath)
	if err != nil {
		return nil, err
	}
	fd, err := unix.Open(device.hidrawPath, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	return &searchLink{device: device, file: os.NewFile(uintptr(fd), device.hidrawPath), assembler: firmware.NewControlPacketAssembler(layout)}, nil
}

func (s *searchLink) Close() error { return s.file.Close() }
func (s *searchLink) discoveryCommand(start bool) []byte {
	if usesDualModeSearch(s.device) {
		if start {
			return btsearch.DualModeStartCommand()
		}
		return btsearch.DualModeStopCommand()
	}
	if start {
		return btsearch.StartCommand()
	}
	return btsearch.StopCommand()
}

func (s *searchLink) Start(ctx context.Context) error {
	command := s.discoveryCommand(true)
	return s.command(ctx, command[0], command[1:])
}
func (s *searchLink) Stop(ctx context.Context) error {
	command := s.discoveryCommand(false)
	return s.command(ctx, command[0], command[1:])
}

func (s *searchLink) command(ctx context.Context, opcode byte, payload []byte) error {
	if s.binding != nil {
		return s.Command(ctx, opcode, payload)
	}
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for !gnpIOMu.TryLock() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
	defer gnpIOMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if !searchMatchesCurrent(s.device) {
		return errors.New("dongle changed during search")
	}
	h := openDeviceHidraw(s.device)
	if h == nil {
		return errors.New("cannot open dongle management endpoint")
	}
	defer h.close()
	seq := nextSeq()
	packet, err := buildGNPReport(gnpSrcDongle, seq, gnpFlagCmd, gnpClassPairingDevice, opcode, payload)
	if err != nil {
		return err
	}
	if err := h.write(packet); err != nil {
		return err
	}
	deadline := time.Now().Add(2 * time.Second)
	if end, ok := ctx.Deadline(); ok && end.Before(deadline) {
		deadline = end
	}
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return err
		}
		reply, err := h.read(time.Until(deadline))
		if err != nil {
			return err
		}
		if len(reply) > 0 && reply[0] == gnpReportID {
			reply = reply[1:]
		}
		if matched, err := matchGNPWriteReply(reply, gnpSrcDongle, seq); matched {
			return err
		}
	}
	return errors.New("dongle search acknowledgement timed out")
}

func (s *searchLink) Read(ctx context.Context) ([]byte, error) {
	buffer := make([]byte, 256)
	for ctx.Err() == nil {
		if !searchMatchesCurrent(s.device) {
			return nil, errors.New("dongle disconnected during search")
		}
		if s.binding != nil {
			if err := s.Valid(); err != nil {
				return nil, err
			}
		}
		fds := []unix.PollFd{{Fd: int32(s.file.Fd()), Events: unix.POLLIN}}
		pollMS := 100
		if s.binding != nil {
			if end, ok := ctx.Deadline(); ok {
				remaining := time.Until(end)
				if remaining <= 0 {
					return nil, context.DeadlineExceeded
				}
				pollMS = min(pollMS, int((remaining+time.Millisecond-1)/time.Millisecond))
			}
		}
		count, err := unix.Poll(fds, pollMS)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return nil, err
		}
		if count == 0 {
			continue
		}
		if fds[0].Revents&(unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) != 0 {
			return nil, errors.New("dongle search reader disconnected")
		}
		n, err := unix.Read(int(s.file.Fd()), buffer)
		if err == unix.EAGAIN || err == unix.EINTR {
			continue
		}
		if err != nil {
			return nil, err
		}
		if n == 0 {
			return nil, errors.New("dongle search reader closed")
		}
		packet, err := s.assembler.Push(buffer[:n])
		if err != nil {
			return nil, err
		}
		if packet != nil {
			return packet, nil
		}
	}
	return nil, ctx.Err()
}
