package httpx

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWithRequestID(t *testing.T) {
	cases := []struct {
		name, incoming string
		keep           bool
	}{
		{"none sent: generate one", "", false},
		{"valid: keep it", "cycle-0123abcd", true},
		{"newline injection: replace it", "abc\ninjected", false},
		{"too long: replace it", strings.Repeat("a", 65), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var seen string
			h := WithRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen = RequestIDFrom(r.Context())
			}))
			req := httptest.NewRequest("GET", "/", nil)
			if tc.incoming != "" {
				req.Header.Set(RequestIDHeader, tc.incoming)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if seen == "" || rec.Header().Get(RequestIDHeader) != seen {
				t.Fatalf("handler saw %q, response header %q; want the same non-empty ID", seen, rec.Header().Get(RequestIDHeader))
			}
			if (seen == tc.incoming) != tc.keep {
				t.Errorf("ID = %q, incoming %q, keep=%v", seen, tc.incoming, tc.keep)
			}
		})
	}
}

func TestLoggerAddsRequestID(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf)

	logger.InfoContext(ContextWithRequestID(context.Background(), "abc123"), "hello")
	logger.Info("no context")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if !strings.Contains(lines[0], `"request_id":"abc123"`) {
		t.Errorf("line with context = %s, want request_id", lines[0])
	}
	if strings.Contains(lines[1], "request_id") {
		t.Errorf("line without context = %s, want no request_id", lines[1])
	}

	// Loggers derived with .With(...) must keep the behaviour.
	buf.Reset()
	logger.With(slog.String("svc", "api")).InfoContext(ContextWithRequestID(context.Background(), "xyz"), "derived")
	if !strings.Contains(buf.String(), `"request_id":"xyz"`) {
		t.Errorf("derived logger lost request_id: %s", buf.String())
	}
}
