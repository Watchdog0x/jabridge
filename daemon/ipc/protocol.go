// ipc/protocol — JSON-RPC 2.0 types for the Jabridge daemon IPC.
//
// Wire format: newline-delimited JSON over Unix socket.
// Each message is one JSON object terminated by '\n'.

package ipc

import "encoding/json"

// Request is a JSON-RPC 2.0 request.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response is a JSON-RPC 2.0 response.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  interface{}     `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError is a JSON-RPC 2.0 error object.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Standard JSON-RPC error codes.
const (
	ErrCodeParse      = -32700
	ErrCodeInvalidReq = -32600
	ErrCodeMethodNF   = -32601
	ErrCodeInvalidP   = -32602
	ErrCodeInternal   = -32603
)

// Notification is a server-initiated push message (no ID).
type Notification struct {
	JSONRPC string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params"`
}

func SuccessResponse(id json.RawMessage, result interface{}) Response {
	return Response{JSONRPC: "2.0", ID: id, Result: result}
}

func ErrorResponse(id json.RawMessage, code int, msg string) Response {
	return Response{JSONRPC: "2.0", ID: id, Error: &RPCError{Code: code, Message: msg}}
}
