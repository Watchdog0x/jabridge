// Package history stores a bounded, local event timeline. It accepts selected
// fields only: never messages, command arguments, payloads, or device names.
package history

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"
)

const MaxFileBytes = 128 << 10
const RetentionDays = 7

type Event struct {
	Time          time.Time `json:"time"`
	Version       string    `json:"version,omitempty"`
	Session       string    `json:"session,omitempty"`
	Operation     uint64    `json:"operation,omitempty"`
	Component     string    `json:"component"`
	Command       string    `json:"command,omitempty"`
	Subcommand    string    `json:"subcommand,omitempty"`
	Input         string    `json:"input,omitempty"`
	Action        string    `json:"action"`
	Phase         string    `json:"phase"`
	Screen        string    `json:"screen,omitempty"`
	Selection     int       `json:"selection,omitempty"`
	USBProduct    uint16    `json:"usbProduct,omitempty"`
	Connection    string    `json:"connection,omitempty"`
	Setting       string    `json:"setting,omitempty"`
	Method        string    `json:"method,omitempty"`
	Error         string    `json:"error,omitempty"`
	DurationMS    int64     `json:"durationMs,omitempty"`
	RPCCode       int       `json:"rpcCode,omitempty"`
	DroppedBefore uint64    `json:"droppedBefore,omitempty"`
}

type Recorder struct {
	Dir   string
	mu    sync.Mutex
	clock func() time.Time
	limit int
}

var active atomic.Pointer[Recorder]
var operation atomic.Uint64
var session string
var version string
var statusMu sync.Mutex
var lastFailure string
var missed atomic.Uint64
var pendingMissed atomic.Uint64
var settings sync.Map
var ErrPanic = errors.New("panic")
var logFileName = regexp.MustCompile(`^events-(\d{4}-\d{2}-\d{2})\.jsonl(?:\.1)?$`)
var versionPattern = regexp.MustCompile(`^\d+\.\d+\.\d+(?:-[a-zA-Z0-9.-]+)?$`)
var sessionPattern = regexp.MustCompile(`^[0-9a-f]{16}$`)

// Configure is called once by the executable. Unit tests do not enable the
// process logger, so library tests cannot write into the user's history.
func Configure(buildVersion string) {
	if os.Getenv("JABRIDGE_HISTORY") == "off" {
		return
	}
	var random [8]byte
	if _, err := rand.Read(random[:]); err == nil {
		session = hex.EncodeToString(random[:])
	}
	version = buildVersion
	directory, err := Directory()
	if err != nil {
		noteFailure(err)
		return
	}
	active.Store(&Recorder{Dir: directory})
}

func RegisterSettings(keys ...string) {
	for _, key := range keys {
		settings.Store(key, true)
	}
}

func NextOperation() uint64 { return operation.Add(1) }

func TraceMethod(method string) bool {
	switch method {
	case "settings.set", "settings.list", "device.select", "device.reset", "device.busylight", "bt.connect", "bt.disconnect", "bt.forget", "bt.pair", "bt.autopair", "bt.search", "service.shutdown":
		return true
	}
	return false
}

func CapturePanic(event Event) {
	if value := recover(); value != nil {
		event.Action = "panic"
		event.Phase = "panic"
		event.Error = "panic"
		Record(event)
		panic(value)
	}
}

func Directory() (string, error) {
	if root := os.Getenv("STATE_DIRECTORY"); root != "" && filepath.IsAbs(root) && !strings.Contains(root, ":") {
		return filepath.Join(root, "history"), nil
	}
	root := os.Getenv("XDG_STATE_HOME")
	if !filepath.IsAbs(root) {
		homeDirectory, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		root = filepath.Join(homeDirectory, ".local", "state")
	}
	return filepath.Join(root, "jabridge", "history"), nil
}

func Record(event Event) {
	recorder := active.Load()
	if recorder == nil {
		return
	}
	event.Version = version
	event.Session = session
	event.DroppedBefore = pendingMissed.Swap(0)
	if err := recorder.Append(event); err != nil {
		noteFailure(err)
		pendingMissed.Add(event.DroppedBefore + 1)
	}
}

func noteFailure(err error) {
	missed.Add(1)
	statusMu.Lock()
	lastFailure = Classify(err)
	statusMu.Unlock()
}

func Status() (uint64, string) {
	statusMu.Lock()
	defer statusMu.Unlock()
	return missed.Load(), lastFailure
}

type RecordingStatus struct {
	Enabled bool   `json:"enabled"`
	Missed  uint64 `json:"missed"`
	Error   string `json:"error,omitempty"`
}

func LiveStatus() RecordingStatus {
	count, why := Status()
	return RecordingStatus{Enabled: active.Load() != nil, Missed: count, Error: why}
}

// Begin persists the start immediately, so interrupted operations have no
// matching finish. A missing finish is evidence of interruption, not its cause.
func Begin(event Event) func(error) {
	event.Operation = operation.Add(1)
	event.Phase = "start"
	Record(event)
	start := time.Now()
	return func(err error) {
		event.DurationMS = time.Since(start).Milliseconds()
		event.Phase = "ok"
		event.Error = Classify(err)
		if err != nil {
			event.Phase = "error"
		}
		Record(event)
	}
}

// EndDeferred must be deferred directly. It records a panic as failure and
// rethrows it, preserving the application's original failure behavior.
func EndDeferred(finish func(error), result *error) {
	if value := recover(); value != nil {
		finish(ErrPanic)
		panic(value)
	}
	finish(*result)
}

func Classify(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, ErrPanic):
		return "panic"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, os.ErrPermission), errors.Is(err, unix.EPERM):
		return "permission"
	case errors.Is(err, os.ErrNotExist):
		return "missing"
	case errors.Is(err, unix.EROFS):
		return "read-only-filesystem"
	case errors.Is(err, unix.ENOSPC):
		return "disk-full"
	case errors.Is(err, unix.EWOULDBLOCK):
		return "history-busy"
	}
	text := strings.ToLower(err.Error())
	switch {
	case strings.Contains(text, "timeout"), strings.Contains(text, "timed out"):
		return "timeout"
	case strings.Contains(text, "permission denied"):
		return "permission"
	case strings.Contains(text, "disconnect"), strings.Contains(text, "connection closed"), strings.Contains(text, "not connected"):
		return "disconnected"
	case strings.Contains(text, "nak"):
		return "device-rejected"
	case strings.Contains(text, "not supported"), strings.Contains(text, "unsupported"), strings.Contains(text, "not implemented"):
		return "unsupported"
	case strings.Contains(text, "invalid"), strings.Contains(text, "mismatch"):
		return "invalid-data"
	default:
		return "failed"
	}
}

func allowed(value, choices string) string {
	if value == "" {
		return ""
	}
	for _, choice := range strings.Fields(choices) {
		if value == choice {
			return value
		}
	}
	return "other"
}

func sanitize(event Event) Event {
	event.Component = allowed(event.Component, "app cli tui service ipc-client ipc-server device")
	event.Command = allowed(event.Command, "tui status battery diagnose debug buttons daemon --daemon -d update firmware fw settings model models sound audio use setup ipc service completion history --version -v version --help -h help")
	event.Subcommand = allowed(event.Subcommand, "start status stop restart install download verify set list output volume mute usb dongle ping watch devices battery settings select bash clear")
	event.Input = allowed(event.Input, "up down enter back action-1 action-2 action-3 action-4")
	event.Action = allowed(event.Action, "run key navigation screen action load-settings message connect reconnect request malformed close attach detach battery pairing select settings start stop panic debug history")
	event.Phase = allowed(event.Phase, "start ok error cancelled observed panic")
	event.Screen = allowed(event.Screen, "home search remembered dongle-settings headset-settings devices firmware")
	event.Connection = allowed(event.Connection, "usb dongle")
	event.Method = allowed(event.Method, "service.ping service.shutdown history.status version devices.list device.select settings.list settings.set device.battery device.firmware device.features device.reset device.busylight bt.list bt.search bt.search.list bt.search.connect bt.connect bt.disconnect bt.forget bt.pair bt.autopair subscribe diagnostics.device")
	event.Error = allowed(event.Error, "cancelled timeout permission missing read-only-filesystem disk-full history-busy disconnected device-rejected unsupported invalid-data failed panic transport-closed truncated malformed")
	if _, ok := settings.Load(event.Setting); !ok {
		event.Setting = ""
	}
	if !versionPattern.MatchString(event.Version) {
		event.Version = ""
	}
	if !sessionPattern.MatchString(event.Session) {
		event.Session = ""
	}
	return event
}

func (r *Recorder) now() time.Time {
	if r.clock != nil {
		return r.clock().UTC()
	}
	return time.Now().UTC()
}

func secureFile(dirFD int, name string, flags int) (*os.File, error) {
	fd, err := unix.Openat(dirFD, name, flags|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0o600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	var info unix.Stat_t
	if err := unix.Fstat(fd, &info); err != nil {
		_ = file.Close()
		return nil, err
	}
	if info.Mode&unix.S_IFMT != unix.S_IFREG || info.Uid != uint32(os.Geteuid()) || info.Nlink != 1 || info.Mode&0o777 != 0o600 {
		_ = file.Close()
		return nil, errors.New("unsafe history file")
	}
	return file, nil
}

func (r *Recorder) openDirectory(create bool) (int, error) {
	if create {
		if err := os.MkdirAll(r.Dir, 0o700); err != nil {
			return -1, err
		}
	}
	fd, err := unix.Open(r.Dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	if stat.Uid != uint32(os.Geteuid()) || stat.Mode&0o777 != 0o700 {
		_ = unix.Close(fd)
		return -1, errors.New("unsafe history directory")
	}
	return fd, nil
}

func directoryNames(fd int) ([]string, error) {
	copyFD, err := unix.Dup(fd)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(copyFD), "history")
	defer func() { _ = file.Close() }()
	return file.Readdirnames(-1)
}

func (r *Recorder) lock(dirFD int) (func(), error) {
	file, err := secureFile(dirFD, ".lock", unix.O_RDWR|unix.O_CREAT)
	if err != nil {
		return nil, err
	}
	// A stalled peer must not hang the TUI. Contention is counted by Status.
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, err
	}
	return func() { _ = unix.Flock(int(file.Fd()), unix.LOCK_UN); _ = file.Close() }, nil
}

func retainedName(name string, now time.Time) bool {
	match := logFileName.FindStringSubmatch(name)
	if match == nil {
		return false
	}
	day, err := time.Parse("2006-01-02", match[1])
	if err != nil {
		return false
	}
	today := now.Truncate(24 * time.Hour)
	return !day.Before(today.AddDate(0, 0, -RetentionDays+1)) && !day.After(today)
}

func removeLog(dirFD int, name string) error {
	file, err := secureFile(dirFD, name, unix.O_RDONLY)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	_ = file.Close()
	return unix.Unlinkat(dirFD, name, 0)
}

func (r *Recorder) Append(event Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	fd, err := r.openDirectory(true)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	unlock, err := r.lock(fd)
	if err != nil {
		return err
	}
	defer unlock()
	now := r.now()
	names, err := directoryNames(fd)
	if err != nil {
		return err
	}
	for _, name := range names {
		if logFileName.MatchString(name) && !retainedName(name, now) {
			if err := removeLog(fd, name); err != nil {
				return err
			}
		}
	}
	event = sanitize(event)
	event.Time = now
	encoded, err := json.Marshal(event)
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	limit := MaxFileBytes
	if r.limit > 0 {
		limit = r.limit
	}
	if len(encoded) > limit {
		return errors.New("history event exceeds limit")
	}
	name := "events-" + now.Format("2006-01-02") + ".jsonl"
	file, err := secureFile(fd, name, unix.O_RDWR|unix.O_APPEND|unix.O_CREAT)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return err
	}
	if info.Size() > MaxFileBytes {
		_ = file.Close()
		return errors.New("oversized history file")
	}
	if info.Size() > 0 {
		var tail [1]byte
		if _, err := file.ReadAt(tail[:], info.Size()-1); err != nil {
			_ = file.Close()
			return err
		}
		if tail[0] != '\n' {
			encoded = append([]byte{'\n'}, encoded...)
		}
	}
	if info.Size()+int64(len(encoded)) > int64(limit) {
		_ = file.Close()
		if err := removeLog(fd, name+".1"); err != nil {
			return err
		}
		if err := unix.Renameat(fd, name, fd, name+".1"); err != nil {
			return err
		}
		file, err = secureFile(fd, name, unix.O_RDWR|unix.O_APPEND|unix.O_CREAT|unix.O_EXCL)
		if err != nil {
			return err
		}
	}
	defer func() { _ = file.Close() }()
	n, err := file.Write(encoded)
	if err == nil && n != len(encoded) {
		err = io.ErrShortWrite
	}
	return err
}

// Read works after a restart and never enables recording or starts the daemon.
func (r *Recorder) Read(limit int) ([]Event, int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	fd, err := r.openDirectory(false)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = unix.Close(fd) }()
	lockFile, lockErr := secureFile(fd, ".lock", unix.O_RDONLY)
	if lockErr != nil {
		return nil, 0, lockErr
	}
	defer func() { _ = lockFile.Close() }()
	if err := unix.Flock(int(lockFile.Fd()), unix.LOCK_SH|unix.LOCK_NB); err != nil {
		return nil, 0, err
	}
	defer func() { _ = unix.Flock(int(lockFile.Fd()), unix.LOCK_UN) }()
	names, err := directoryNames(fd)
	if err != nil {
		return nil, 0, err
	}
	sort.Slice(names, func(i, j int) bool {
		left, right := strings.TrimSuffix(names[i], ".1"), strings.TrimSuffix(names[j], ".1")
		if left == right {
			return strings.HasSuffix(names[i], ".1") && !strings.HasSuffix(names[j], ".1")
		}
		return names[i] < names[j]
	})
	var events []Event
	skipped := 0
	for _, name := range names {
		if !retainedName(name, r.now()) {
			continue
		}
		file, err := secureFile(fd, name, unix.O_RDONLY)
		if err != nil {
			skipped++
			continue
		}
		reader := bufio.NewScanner(io.LimitReader(file, MaxFileBytes))
		reader.Buffer(make([]byte, 4096), 4096)
		for reader.Scan() {
			var event Event
			if json.Unmarshal(reader.Bytes(), &event) != nil || event.Time.IsZero() {
				skipped++
				continue
			}
			if event.Time.Before(r.now().Truncate(24*time.Hour).AddDate(0, 0, -RetentionDays+1)) || event.Time.After(r.now().Add(time.Minute)) {
				continue
			}
			events = append(events, sanitize(event))
		}
		if reader.Err() != nil {
			skipped++
		}
		_ = file.Close()
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].Time.Before(events[j].Time) })
	if limit > 0 && len(events) > limit {
		events = events[len(events)-limit:]
	}
	return events, skipped, nil
}

func Describe(event Event) string {
	text := fmt.Sprintf("%s %s/%s %s session=%s op=%d", event.Time.UTC().Format(time.RFC3339Nano), event.Component, event.Action, event.Phase, event.Session, event.Operation)
	if event.Version != "" {
		text += " version=" + event.Version
	}
	if event.Screen != "" {
		text += fmt.Sprintf(" screen=%s row=%d", event.Screen, event.Selection)
	}
	if event.Command != "" {
		text += " command=" + event.Command
	}
	if event.Subcommand != "" {
		text += " subcommand=" + event.Subcommand
	}
	if event.Input != "" {
		text += " input=" + event.Input
	}
	if event.USBProduct != 0 {
		text += fmt.Sprintf(" usb=0b0e:%04x connection=%s", event.USBProduct, event.Connection)
	}
	if event.Setting != "" {
		text += " setting=" + event.Setting
	}
	if event.Method != "" {
		text += " method=" + event.Method
	}
	if event.Error != "" {
		text += " error=" + event.Error
	}
	if event.DurationMS > 0 {
		text += fmt.Sprintf(" duration=%dms", event.DurationMS)
	}
	if event.RPCCode != 0 {
		text += fmt.Sprintf(" rpc=%d", event.RPCCode)
	}
	if event.DroppedBefore > 0 {
		text += fmt.Sprintf(" missed-before=%d", event.DroppedBefore)
	}
	return text
}

func (r *Recorder) Clear() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	fd, err := r.openDirectory(false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	unlock, err := r.lock(fd)
	if err != nil {
		return err
	}
	defer unlock()
	names, err := directoryNames(fd)
	if err != nil {
		return err
	}
	for _, name := range names {
		if logFileName.MatchString(name) {
			if err := removeLog(fd, name); err != nil {
				return err
			}
		}
	}
	return nil
}
