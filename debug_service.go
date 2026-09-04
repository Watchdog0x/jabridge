package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/Watchdog0x/jabridge/daemon/ipc"
)

func writeRecentServiceFailures(out *bytes.Buffer) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	data, err := exec.CommandContext(ctx, "journalctl", "--user", "-u", "jabridge.service", "--since=-15min", "-n", "80", "-o", "cat", "--no-pager").Output()
	if err != nil {
		fmt.Fprintln(out, "Recent service failure categories: unavailable")
		return
	}
	fmt.Fprintln(out, "Recent service history (last 80 entries within 15 minutes; raw logs omitted):")
	categories := serviceFailureCategories(string(data))
	if len(categories) == 0 {
		fmt.Fprintln(out, "  No recognized failure categories in the available journal.")
	}
	for _, category := range categories {
		fmt.Fprintln(out, "  "+category)
	}
}

func serviceFailureCategories(data string) []string {
	counts := map[string]int{}
	for _, line := range strings.Split(strings.ToLower(data), "\n") {
		switch {
		case strings.Contains(line, "namespace") && (strings.Contains(line, "fail") || strings.Contains(line, "denied")):
			counts["namespace setup failure"]++
		case strings.Contains(line, "permission denied"):
			counts["permission denied"]++
		case strings.Contains(line, "address already in use"), strings.Contains(line, "already running"):
			counts["duplicate service/socket instance"]++
		case strings.Contains(line, "panic:"), strings.Contains(line, "fatal error:"):
			counts["Go runtime panic/fatal error"]++
		case strings.Contains(line, "timeout"), strings.Contains(line, "timed out"):
			counts["timeout"]++
		case strings.Contains(line, "failed to execute"), strings.Contains(line, "203/exec"):
			counts["service executable failure"]++
		case strings.Contains(line, "symlink"), strings.Contains(line, "unsafe"):
			counts["path safety validation"]++
		}
	}
	var categories []string
	for name, count := range counts {
		categories = append(categories, fmt.Sprintf("%s: %d", name, count))
	}
	sort.Strings(categories)
	return categories
}

func ipcDiagnosticFailure(err error) string {
	var remote *ipc.RemoteError
	if errors.As(err, &remote) {
		switch remote.Code {
		case ipc.ErrCodeMethodNF:
			return "service lacks this diagnostic method; update it and run jabridge service restart"
		case ipc.ErrCodeInvalidP:
			return "service rejected diagnostic parameters; investigate client/service API compatibility"
		default:
			if strings.Contains(strings.ToLower(remote.Message), "already running") {
				return "another diagnostic is running; retry when it finishes"
			}
			if strings.Contains(strings.ToLower(remote.Message), "disconnected") {
				return "device disconnected during the diagnostic; reconnect and repeat"
			}
			return fmt.Sprintf("service returned RPC error %d; %s", remote.Code, protocolDiagnosticError(err))
		}
	}
	return "service communication " + diagnosticError(err) + "; check the service state and socket"
}
