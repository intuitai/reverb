package mcp_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nobelk/reverb/pkg/embedding/fake"
	"github.com/nobelk/reverb/pkg/reverb"
	"github.com/nobelk/reverb/pkg/server/mcp"
	"github.com/nobelk/reverb/pkg/store/memory"
	"github.com/nobelk/reverb/pkg/vector/flat"
)

func newTestServer(t *testing.T) (*mcp.Server, *reverb.Client) {
	t.Helper()
	client, err := reverb.New(
		reverb.Config{
			DefaultTTL:          24 * time.Hour,
			SimilarityThreshold: 0.95,
			SemanticTopK:        5,
		},
		fake.New(64),
		memory.New(),
		flat.New(0),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	return mcp.NewServer(client), client
}

// call sends a JSON-RPC request and decodes the envelope. id must be a simple
// integer; pass 0 for a notification.
func call(t *testing.T, srv *mcp.Server, id int, method string, params any) (raw []byte, env envelope) {
	t.Helper()
	req := map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
	}
	if id != 0 {
		req["id"] = id
	}
	if params != nil {
		req["params"] = params
	}
	body, err := json.Marshal(req)
	require.NoError(t, err)

	raw = srv.Handle(context.Background(), body)
	if id == 0 {
		// Notification: caller asserts nil separately if desired.
		return raw, envelope{}
	}
	require.NotNil(t, raw, "expected a response for request id=%d", id)
	require.NoError(t, json.Unmarshal(raw, &env))
	return raw, env
}

// callTool issues a tools/call for the given tool name and unpacks the single
// text content block into outer (the envelope) and payload (the parsed JSON
// from the content[0].text field).
func callTool(t *testing.T, srv *mcp.Server, id int, name string, args any) (env envelope, result toolResult, payload map[string]any) {
	t.Helper()
	_, env = call(t, srv, id, "tools/call", map[string]any{
		"name":      name,
		"arguments": args,
	})
	require.Nil(t, env.Error, "unexpected rpc error: %+v", env.Error)
	require.NotNil(t, env.Result)
	require.NoError(t, json.Unmarshal(env.Result, &result))
	if len(result.Content) > 0 {
		_ = json.Unmarshal([]byte(result.Content[0].Text), &payload)
	}
	return env, result, payload
}

type envelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type toolResult struct {
	Content []toolContent `json:"content"`
	IsError bool          `json:"isError"`
}

type toolContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// -- initialize / ping / tools/list -----------------------------------------

func TestInitialize_ReturnsServerInfo(t *testing.T) {
	srv, _ := newTestServer(t)
	_, env := call(t, srv, 1, "initialize", map[string]any{
		"protocolVersion": mcp.ProtocolVersion,
		"capabilities":    map[string]any{},
	})
	require.Nil(t, env.Error)

	var result struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
		Capabilities map[string]any `json:"capabilities"`
	}
	require.NoError(t, json.Unmarshal(env.Result, &result))
	assert.Equal(t, mcp.ProtocolVersion, result.ProtocolVersion)
	assert.Equal(t, mcp.ServerName, result.ServerInfo.Name)
	assert.Equal(t, mcp.ServerVersion, result.ServerInfo.Version)
	assert.Contains(t, result.Capabilities, "tools")
}

func TestPing_Responds(t *testing.T) {
	srv, _ := newTestServer(t)
	_, env := call(t, srv, 2, "ping", nil)
	assert.Nil(t, env.Error)
	assert.NotEmpty(t, env.Result)
}

func TestToolsList_AdvertisesAllTools(t *testing.T) {
	srv, _ := newTestServer(t)
	_, env := call(t, srv, 3, "tools/list", nil)
	require.Nil(t, env.Error)

	var result struct {
		Tools []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"inputSchema"`
		} `json:"tools"`
	}
	require.NoError(t, json.Unmarshal(env.Result, &result))

	want := []string{
		"reverb_lookup",
		"reverb_store",
		"reverb_invalidate",
		"reverb_delete_entry",
		"reverb_stats",
	}
	got := map[string]bool{}
	for _, tool := range result.Tools {
		got[tool.Name] = true
		assert.NotEmpty(t, tool.Description, "tool %q has no description", tool.Name)
		// InputSchema must be a valid JSON object.
		var schema map[string]any
		require.NoError(t, json.Unmarshal(tool.InputSchema, &schema),
			"tool %q has invalid inputSchema", tool.Name)
		assert.Equal(t, "object", schema["type"])
	}
	for _, n := range want {
		assert.True(t, got[n], "tool %q missing from tools/list", n)
	}
}

// -- tool: store + lookup ---------------------------------------------------

func TestToolsCall_Store_Then_Lookup_ExactHit(t *testing.T) {
	srv, _ := newTestServer(t)

	_, stored, storePayload := callTool(t, srv, 10, "reverb_store", map[string]any{
		"namespace": "support-bot",
		"prompt":    "How do I reset my password?",
		"model_id":  "gpt-4o",
		"response":  "Go to Settings > Security.",
	})
	require.False(t, stored.IsError, "store returned tool error: %s", stored.Content[0].Text)
	assert.NotEmpty(t, storePayload["id"], "store result missing id")

	_, lookup, lookupPayload := callTool(t, srv, 11, "reverb_lookup", map[string]any{
		"namespace": "support-bot",
		"prompt":    "How do I reset my password?",
		"model_id":  "gpt-4o",
	})
	require.False(t, lookup.IsError)
	assert.Equal(t, true, lookupPayload["hit"])
	assert.Equal(t, "exact", lookupPayload["tier"])

	entry, ok := lookupPayload["entry"].(map[string]any)
	require.True(t, ok, "lookup result missing entry object")
	assert.Equal(t, "Go to Settings > Security.", entry["response"])
	assert.Equal(t, "support-bot", entry["namespace"])
}

func TestToolsCall_Lookup_Miss(t *testing.T) {
	srv, _ := newTestServer(t)
	_, res, payload := callTool(t, srv, 20, "reverb_lookup", map[string]any{
		"namespace": "empty-ns",
		"prompt":    "nothing cached",
		"model_id":  "gpt-4o",
	})
	require.False(t, res.IsError)
	assert.Equal(t, false, payload["hit"])
	assert.NotContains(t, payload, "entry")
}

func TestToolsCall_Store_WithSourceLineage(t *testing.T) {
	srv, _ := newTestServer(t)

	hash := strings.Repeat("ab", 32) // 64 hex chars = 32 bytes
	_, stored, _ := callTool(t, srv, 30, "reverb_store", map[string]any{
		"namespace": "docs-bot",
		"prompt":    "What is the return policy?",
		"model_id":  "gpt-4o",
		"response":  "30 days, no questions asked.",
		"sources": []map[string]any{
			{"source_id": "doc:returns", "content_hash": hash},
		},
		"ttl_seconds": 3600,
	})
	require.False(t, stored.IsError, "store with sources failed: %s", stored.Content[0].Text)

	_, invalidated, invPayload := callTool(t, srv, 31, "reverb_invalidate", map[string]any{
		"source_id": "doc:returns",
	})
	require.False(t, invalidated.IsError)
	// float64 is how Go decodes JSON numbers into any.
	assert.EqualValues(t, 1, invPayload["invalidated_count"])

	// After invalidation, the same lookup must miss.
	_, _, lookupPayload := callTool(t, srv, 32, "reverb_lookup", map[string]any{
		"namespace": "docs-bot",
		"prompt":    "What is the return policy?",
		"model_id":  "gpt-4o",
	})
	assert.Equal(t, false, lookupPayload["hit"])
}

func TestToolsCall_Store_InvalidContentHash_ReturnsToolError(t *testing.T) {
	srv, _ := newTestServer(t)
	_, res, _ := callTool(t, srv, 40, "reverb_store", map[string]any{
		"namespace": "x",
		"prompt":    "y",
		"response":  "z",
		"sources": []map[string]any{
			{"source_id": "doc:bad", "content_hash": "not-a-hex-string"},
		},
	})
	require.True(t, res.IsError)
	assert.Contains(t, res.Content[0].Text, "invalid content_hash")
}

func TestToolsCall_Store_ShortContentHash_ReturnsToolError(t *testing.T) {
	srv, _ := newTestServer(t)
	_, res, _ := callTool(t, srv, 41, "reverb_store", map[string]any{
		"namespace": "x",
		"prompt":    "y",
		"response":  "z",
		"sources": []map[string]any{
			{"source_id": "doc:bad", "content_hash": hex.EncodeToString([]byte{0x01, 0x02})},
		},
	})
	require.True(t, res.IsError)
	assert.Contains(t, res.Content[0].Text, "must be 32 bytes")
}

// -- tool: delete entry ----------------------------------------------------

func TestToolsCall_DeleteEntry_RemovesEntry(t *testing.T) {
	srv, _ := newTestServer(t)

	_, _, storePayload := callTool(t, srv, 50, "reverb_store", map[string]any{
		"namespace": "ns",
		"prompt":    "keep or delete",
		"model_id":  "m",
		"response":  "temporary",
	})
	entryID, _ := storePayload["id"].(string)
	require.NotEmpty(t, entryID)

	_, delRes, delPayload := callTool(t, srv, 51, "reverb_delete_entry", map[string]any{
		"entry_id": entryID,
	})
	require.False(t, delRes.IsError)
	assert.Equal(t, true, delPayload["deleted"])

	// Subsequent lookup must miss.
	_, _, lookupPayload := callTool(t, srv, 52, "reverb_lookup", map[string]any{
		"namespace": "ns",
		"prompt":    "keep or delete",
		"model_id":  "m",
	})
	assert.Equal(t, false, lookupPayload["hit"])
}

// -- tool: stats -----------------------------------------------------------

func TestToolsCall_Stats_ReflectsActivity(t *testing.T) {
	srv, _ := newTestServer(t)

	// Store one entry and perform a hit + a miss so counters move.
	callTool(t, srv, 60, "reverb_store", map[string]any{
		"namespace": "stats-ns",
		"prompt":    "counted",
		"model_id":  "m",
		"response":  "ok",
	})
	callTool(t, srv, 61, "reverb_lookup", map[string]any{
		"namespace": "stats-ns",
		"prompt":    "counted",
		"model_id":  "m",
	})
	callTool(t, srv, 62, "reverb_lookup", map[string]any{
		"namespace": "stats-ns",
		"prompt":    "unseen",
		"model_id":  "m",
	})

	_, res, payload := callTool(t, srv, 63, "reverb_stats", map[string]any{})
	require.False(t, res.IsError)
	assert.EqualValues(t, 1, payload["total_entries"])
	assert.EqualValues(t, 1, payload["exact_hits_total"])
	assert.EqualValues(t, 1, payload["misses_total"])
	nss, ok := payload["namespaces"].([]any)
	require.True(t, ok)
	assert.Contains(t, nss, "stats-ns")
}

// -- validation errors -----------------------------------------------------

func TestToolsCall_MissingRequiredArgs_ReturnsToolError(t *testing.T) {
	srv, _ := newTestServer(t)

	tests := []struct {
		name string
		tool string
		args map[string]any
		want string
	}{
		{"lookup no namespace", "reverb_lookup", map[string]any{"prompt": "p"}, "namespace is required"},
		{"lookup no prompt", "reverb_lookup", map[string]any{"namespace": "n"}, "prompt is required"},
		{"store no namespace", "reverb_store", map[string]any{"prompt": "p", "response": "r"}, "namespace is required"},
		{"store no response", "reverb_store", map[string]any{"namespace": "n", "prompt": "p"}, "response is required"},
		{"invalidate no source_id", "reverb_invalidate", map[string]any{}, "source_id is required"},
		{"delete no entry_id", "reverb_delete_entry", map[string]any{}, "entry_id is required"},
	}

	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, res, _ := callTool(t, srv, 100+i, tc.tool, tc.args)
			require.True(t, res.IsError, "expected tool error for %s", tc.name)
			assert.Contains(t, res.Content[0].Text, tc.want)
		})
	}
}

// -- JSON-RPC envelope errors ----------------------------------------------

func TestHandle_InvalidJSON_ReturnsParseError(t *testing.T) {
	srv, _ := newTestServer(t)
	raw := srv.Handle(context.Background(), []byte("{not valid json"))
	require.NotNil(t, raw)

	var env envelope
	require.NoError(t, json.Unmarshal(raw, &env))
	require.NotNil(t, env.Error)
	assert.Equal(t, -32700, env.Error.Code)
}

func TestHandle_WrongJSONRPCVersion_ReturnsInvalidRequest(t *testing.T) {
	srv, _ := newTestServer(t)
	raw := srv.Handle(context.Background(), []byte(`{"jsonrpc":"1.0","id":1,"method":"ping"}`))
	require.NotNil(t, raw)

	var env envelope
	require.NoError(t, json.Unmarshal(raw, &env))
	require.NotNil(t, env.Error)
	assert.Equal(t, -32600, env.Error.Code)
}

func TestHandle_UnknownMethod_ReturnsMethodNotFound(t *testing.T) {
	srv, _ := newTestServer(t)
	_, env := call(t, srv, 7, "does/not/exist", nil)
	require.NotNil(t, env.Error)
	assert.Equal(t, -32601, env.Error.Code)
}

func TestToolsCall_UnknownTool_ReturnsMethodNotFound(t *testing.T) {
	srv, _ := newTestServer(t)
	_, env := call(t, srv, 8, "tools/call", map[string]any{
		"name":      "not_a_real_tool",
		"arguments": map[string]any{},
	})
	require.NotNil(t, env.Error)
	assert.Equal(t, -32601, env.Error.Code)
	assert.Contains(t, env.Error.Message, "not_a_real_tool")
}

func TestHandle_Notification_ReturnsNoResponse(t *testing.T) {
	srv, _ := newTestServer(t)
	// A request with no "id" is a notification. Handle must return nil so
	// the transport writes nothing (or 204).
	raw := srv.Handle(context.Background(),
		[]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	assert.Nil(t, raw)
}

func TestHandle_PreservesRequestID(t *testing.T) {
	srv, _ := newTestServer(t)

	// JSON-RPC allows ids of any type — verify we echo strings, not just ints.
	raw := srv.Handle(context.Background(),
		[]byte(`{"jsonrpc":"2.0","id":"req-abc","method":"ping"}`))
	require.NotNil(t, raw)

	var env envelope
	require.NoError(t, json.Unmarshal(raw, &env))
	assert.JSONEq(t, `"req-abc"`, string(env.ID))
}

// -- HTTP transport --------------------------------------------------------

func TestServeHTTP_EndToEnd(t *testing.T) {
	srv, _ := newTestServer(t)

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	var env envelope
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	assert.Nil(t, env.Error)
	assert.NotEmpty(t, env.Result)
}

func TestServeHTTP_Notification_Returns204(t *testing.T) {
	srv, _ := newTestServer(t)

	body := `{"jsonrpc":"2.0","method":"notifications/initialized"}`
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Empty(t, rec.Body.Bytes())
}

func TestServeHTTP_RejectsNonPost(t *testing.T) {
	srv, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}
