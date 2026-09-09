package main

import "github.com/Watchdog0x/jabridge/daemon/ipc"

// UI-thread state, distinct from the service's private discovery records.
var searchViewState ipc.SearchState
