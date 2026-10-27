package httpx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
)

// RequestIDHeader carries one request's ID from service to service, so the
// log lines a single dashboard click produces in api, analyzer and ingest can
// all be found with one search - a lightweight stand-in for a distributed
// tracing system such as Jaeger.
const RequestIDHeader = "X-Request-ID"

// ctxKey is an unexported type for context keys. Using a private type (not a
// plain string) means no other package can accidentally read or overwrite
// our value, even if it picks the same name.
type ctxKey struct{}

// WithRequestID gives every request an ID. If the caller already sent a
// well-formed X-Request-ID (e.g. api calling ingest), it's kept so the ID
// stays the same across services; otherwise a new one is generated. The ID is
// stored in the request's context and echoed back in the response header.
func WithRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(RequestIDHeader)
		if !validRequestID(id) {
			id = NewRequestID()
		}
		w.Header().Set(RequestIDHeader, id)
		// r.WithContext returns a shallow copy of the request carrying the
		// new context; handlers further down see the ID via r.Context().
		next.ServeHTTP(w, r.WithContext(ContextWithRequestID(r.Context(), id)))
	})
}

// NewRequestID returns a random 16-hex-character ID.
func NewRequestID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b) // crypto/rand.Read never fails on supported platforms
	return hex.EncodeToString(b)
}

// ContextWithRequestID returns a copy of ctx carrying id. Background work
// (the analyzer's cycle) uses it to tag its own outgoing calls.
func ContextWithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// RequestIDFrom returns the request ID stored in ctx, or "" if there is none.
// The `.(string)` is a type assertion: context values are stored as `any`,
// and the two-value form (id, ok) returns ok=false instead of panicking if
// the value is missing or of another type.
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(ctxKey{}).(string)
	return id
}

// validRequestID accepts only short IDs made of letters, digits, '-', '_'
// and '.'. Anything else is replaced, so a client can't inject newlines or
// junk into our logs through this header.
func validRequestID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, c := range id {
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.'
		if !ok {
			return false
		}
	}
	return true
}

// logHandler wraps another slog.Handler and adds a request_id attribute to
// every record logged with a context that carries one. That's what makes
// slog.ErrorContext(r.Context(), ...) inside any handler automatically
// tagged, without each call site passing the ID by hand.
type logHandler struct {
	slog.Handler // embedded: Enabled, WithAttrs, WithGroup pass straight through
}

// NewLogHandler wraps h so request IDs from contexts appear in every log line.
func NewLogHandler(h slog.Handler) slog.Handler {
	return logHandler{h}
}

func (h logHandler) Handle(ctx context.Context, rec slog.Record) error {
	if id := RequestIDFrom(ctx); id != "" {
		rec.AddAttrs(slog.String("request_id", id))
	}
	return h.Handler.Handle(ctx, rec)
}

// WithAttrs and WithGroup must re-wrap, or loggers derived with
// slog.With(...) would silently lose the request_id behaviour.
func (h logHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return logHandler{h.Handler.WithAttrs(attrs)}
}

func (h logHandler) WithGroup(name string) slog.Handler {
	return logHandler{h.Handler.WithGroup(name)}
}

// NewLogger is the logger every service installs at startup: JSON lines on
// stdout, with request IDs attached.
func NewLogger(w interface{ Write([]byte) (int, error) }) *slog.Logger {
	return slog.New(NewLogHandler(slog.NewJSONHandler(w, nil)))
}

// Chain applies the standard middleware every service uses, outermost first:
// assign a request ID, then log the request, then recover from panics.
func Chain(h http.Handler) http.Handler {
	return WithRequestID(WithLogging(WithRecover(h)))
}
