package httpx

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
)

// WriteJSON sets the content type, writes the status code, then encodes body
// as JSON. Every JSON response in the project goes through this, so they're
// all built the same way.
func WriteJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// If encoding fails here the client's connection is already broken and
	// there's no way to report an error while writing the error response,
	// so the result is deliberately ignored with `_`.
	_ = json.NewEncoder(w).Encode(body)
}

// WriteError writes a JSON error body: {"error": "<msg>"}.
func WriteError(w http.ResponseWriter, status int, msg string) {
	WriteJSON(w, status, map[string]string{"error": msg})
}

// QueryInt64 reads an integer query parameter such as ?limit=50. It returns
// def if the parameter is absent, and an error (meant for a 400 response) if
// it isn't a number or falls outside [min, max].
func QueryInt64(r *http.Request, key string, def, min, max int64) (int64, error) {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", key)
	}
	if n < min || n > max {
		return 0, fmt.Errorf("%s must be between %d and %d", key, min, max)
	}
	return n, nil
}

// PathID reads a positive integer path wildcard such as the {id} in
// "GET /internal/v1/runs/{id}". r.PathValue (Go 1.22+) returns the text the
// router matched for that wildcard.
func PathID(r *http.Request, name string) (int64, error) {
	n, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return n, nil
}

// QueryInt is QueryInt64 for values that fit comfortably in an int (limits, offsets).
func QueryInt(r *http.Request, key string, def, min, max int) (int, error) {
	n, err := QueryInt64(r, key, int64(def), int64(min), int64(max))
	return int(n), err
}
