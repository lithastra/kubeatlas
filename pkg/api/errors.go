package api

import (
	"encoding/json"
	"net/http"
)

// ErrorResponse is the wire shape every API error returns.
//
// Fields:
//   - Error: a single human-readable sentence; safe to surface in a UI.
//   - Code:  a stable machine-readable token (e.g. "not_found",
//     "invalid_argument", "internal"). Clients branch on this, not on
//     the human message.
//   - RequestID: optional correlation id; unset until the request-id
//     middleware lands in P1-T6.
type ErrorResponse struct {
	Error     string `json:"error"`
	Code      string `json:"code"`
	RequestID string `json:"request_id,omitempty"`
}

// Canonical error codes the API uses. Stable across versions; clients
// may rely on these strings.
const (
	CodeNotFound        = "not_found"
	CodeInvalidArgument = "invalid_argument"
	CodeInternal        = "internal"
)

// writeError serialises an ErrorResponse with the given HTTP status.
// The Content-Type is always application/json.
func writeError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(ErrorResponse{Error: msg, Code: code})
}
