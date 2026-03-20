package webhook

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/org/reverb/pkg/cdc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestHandler creates a webhook Listener and returns its handler along with
// the events channel. This avoids starting a full HTTP server for unit tests.
func newTestHandler(authToken string) (http.HandlerFunc, chan cdc.ChangeEvent) {
	events := make(chan cdc.ChangeEvent, 10)
	l := New(Config{
		Addr:      ":0",
		Path:      "/hooks/source-changed",
		AuthToken: authToken,
	})
	return l.handler(events), events
}

func TestWebhook_ValidEvent(t *testing.T) {
	handler, events := newTestHandler("")

	body := `{"source_id":"doc:reset","content_hash":"abcdef1234abcdef1234abcdef1234abcdef1234abcdef1234abcdef1234abcd","timestamp":"2026-01-01T00:00:00Z"}`
	req := httptest.NewRequest(http.MethodPost, "/hooks/source-changed", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	handler(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	select {
	case event := <-events:
		assert.Equal(t, "doc:reset", event.SourceID)
		assert.Equal(t, "abcdef1234abcdef1234abcdef1234abcdef1234abcdef1234abcdef1234abcd", event.ContentHashHex)
		expectedTime, _ := time.Parse(time.RFC3339, "2026-01-01T00:00:00Z")
		assert.Equal(t, expectedTime, event.Timestamp)
		// Verify the binary hash was populated (first byte should be 0xab).
		assert.Equal(t, byte(0xab), event.ContentHash[0])
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}
}

func TestWebhook_MalformedJSON(t *testing.T) {
	handler, _ := newTestHandler("")

	req := httptest.NewRequest(http.MethodPost, "/hooks/source-changed", strings.NewReader(`{not valid json`))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	handler(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestWebhook_MissingFields(t *testing.T) {
	handler, _ := newTestHandler("")

	// Valid JSON but missing source_id.
	body := `{"content_hash":"abcdef"}`
	req := httptest.NewRequest(http.MethodPost, "/hooks/source-changed", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	handler(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "source_id")
}

func TestWebhook_MethodNotAllowed(t *testing.T) {
	handler, _ := newTestHandler("")

	req := httptest.NewRequest(http.MethodGet, "/hooks/source-changed", nil)

	w := httptest.NewRecorder()
	handler(w, req)

	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

func TestWebhook_Shutdown(t *testing.T) {
	events := make(chan cdc.ChangeEvent, 10)
	l := New(Config{
		Addr: "127.0.0.1:0",
		Path: "/hooks/source-changed",
	})

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)

	go func() {
		errCh <- l.Start(ctx, events)
	}()

	// Give the server a moment to start listening.
	time.Sleep(50 * time.Millisecond)

	// Cancel the context to trigger graceful shutdown.
	cancel()

	select {
	case err := <-errCh:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for shutdown")
	}
}

func TestWebhook_AuthToken(t *testing.T) {
	handler, events := newTestHandler("secret-token")

	body := `{"source_id":"doc:auth-test"}`

	t.Run("missing token", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/hooks/source-changed", strings.NewReader(body))
		w := httptest.NewRecorder()
		handler(w, req)
		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})

	t.Run("wrong token", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/hooks/source-changed", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer wrong-token")
		w := httptest.NewRecorder()
		handler(w, req)
		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})

	t.Run("valid token", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/hooks/source-changed", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer secret-token")
		w := httptest.NewRecorder()
		handler(w, req)
		assert.Equal(t, http.StatusOK, w.Code)

		select {
		case event := <-events:
			assert.Equal(t, "doc:auth-test", event.SourceID)
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for event")
		}
	})
}
