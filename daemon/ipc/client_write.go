package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"
)

// writeRequest honors cancellation while waiting for another writer and while
// the peer is not reading. Only the writer holding the gate changes deadlines.
func (client *Client) writeRequest(ctx context.Context, request Request) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	packet, err := json.Marshal(request)
	if err != nil {
		return err
	}
	packet = append(packet, '\n')
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-client.done:
		return errors.New("IPC connection closed")
	case client.writeGate <- struct{}{}:
	}
	defer func() { <-client.writeGate }()
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-client.done:
		return errors.New("IPC connection closed")
	default:
	}
	deadline, hasDeadline := ctx.Deadline()
	if err := client.conn.SetWriteDeadline(deadline); err != nil {
		_ = client.Close()
		return err
	}
	cancellationFinished := make(chan struct{})
	stopCancellation := context.AfterFunc(ctx, func() {
		_ = client.conn.SetWriteDeadline(time.Now())
		close(cancellationFinished)
	})
	n, writeErr := client.conn.Write(packet)
	if !stopCancellation() {
		<-cancellationFinished
	}
	resetErr := client.conn.SetWriteDeadline(time.Time{})
	if writeErr == nil && n != len(packet) {
		writeErr = io.ErrShortWrite
	}
	if writeErr != nil || resetErr != nil {
		// A failed write may leave a partial JSON line. Never reuse that
		// stream or let a later call complete the interrupted request.
		_ = client.Close()
	}
	if writeErr != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
		if hasDeadline && !time.Now().Before(deadline) {
			return context.DeadlineExceeded
		}
		return writeErr
	}
	// The complete request was written. A peer may have already replied and
	// closed, making deadline cleanup fail. Let Call consume a queued reply
	// before treating that orderly close as a failed operation.
	return nil
}
