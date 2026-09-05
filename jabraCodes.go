package main

import "errors"

// ErrNotSupported is returned when a hardware operation has not completed its
// model-specific validation gate.
var ErrNotSupported = errors.New("device operation is not supported")
