// Package mcp implements a Model Context Protocol (MCP) wrapper around a
// Reverb client. It speaks JSON-RPC 2.0 and exposes the cache's core
// operations (lookup, store, invalidate, delete, stats) as MCP tools that an
// LLM agent can invoke.
//
// The Server is transport-agnostic: Handle takes a raw JSON-RPC request byte
// slice and returns the encoded response. A convenience http.Handler is
// provided for wiring MCP over HTTP.
package mcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/nobelk/reverb/pkg/reverb"
	"github.com/nobelk/reverb/pkg/store"
)

// ProtocolVersion is the MCP protocol revision this server advertises.
const ProtocolVersion = "2024-11-05"

// ServerName and ServerVersion identify this implementation to MCP clients.
const (
	ServerName    = "reverb-mcp"
	ServerVersion = "0.1.0"
)

// JSON-RPC 2.0 error codes (https://www.jsonrpc.org/specification).
const (
	errCodeParse          = -32700
	errCodeInvalidRequest = -32600
	errCodeMethodNotFound = -32601
	errCodeInvalidParams  = -32602
)

// maxRequestBodySize caps HTTP request bodies at 1 MiB to match the REST server.
const maxRequestBodySize = 1 << 20

// request is a JSON-RPC 2.0 request envelope. ID is kept as a raw message so
// we can echo it back verbatim — JSON-RPC allows strings, numbers, or null.
type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// response is a JSON-RPC 2.0 response envelope.
type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// toolContent is one piece of a tools/call response. MCP defines several
// content types; Reverb only needs text.
type toolContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// toolCallResult is the payload MCP returns from tools/call.
type toolCallResult struct {
	Content []toolContent `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

// toolDescriptor advertises one tool in the tools/list response.
type toolDescriptor struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// Server wraps a Reverb client and dispatches MCP JSON-RPC requests.
type Server struct {
	client *reverb.Client
	tools  []toolDescriptor
}

// NewServer constructs an MCP server bound to the given Reverb client.
func NewServer(client *reverb.Client) *Server {
	return &Server{
		client: client,
		tools:  buildToolDescriptors(),
	}
}

// Handle processes a single JSON-RPC request body and returns the encoded
// response bytes. If raw is a notification (no id), the returned slice is
// nil — per JSON-RPC 2.0, notifications produce no reply. Transport code
// should treat a nil return as "write nothing."
func (s *Server) Handle(ctx context.Context, raw []byte) []byte {
	var req request
	if err := json.Unmarshal(raw, &req); err != nil {
		return mustEncode(response{
			JSONRPC: "2.0",
			Error: &rpcError{
				Code:    errCodeParse,
				Message: "invalid JSON: " + err.Error(),
			},
		})
	}

	if req.JSONRPC != "2.0" {
		return mustEncode(response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &rpcError{
				Code:    errCodeInvalidRequest,
				Message: `"jsonrpc" must be "2.0"`,
			},
		})
	}

	// Notifications (no id) — process side effects but never reply.
	isNotification := len(req.ID) == 0 || bytes.Equal(req.ID, []byte("null"))

	result, rpcErr := s.dispatch(ctx, req.Method, req.Params)

	if isNotification {
		return nil
	}

	resp := response{JSONRPC: "2.0", ID: req.ID}
	if rpcErr != nil {
		resp.Error = rpcErr
	} else {
		resp.Result = result
	}
	return mustEncode(resp)
}

// ServeHTTP lets Server be mounted as a JSON-RPC-over-HTTP endpoint.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "only POST is supported", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body: "+err.Error(), http.StatusBadRequest)
		return
	}

	resp := s.Handle(r.Context(), body)
	if resp == nil {
		// Notification: acknowledge with 204 No Content.
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(resp)
}

// dispatch routes a JSON-RPC method to the right handler.
func (s *Server) dispatch(ctx context.Context, method string, params json.RawMessage) (any, *rpcError) {
	switch method {
	case "initialize":
		return s.handleInitialize()
	case "initialized", "notifications/initialized":
		// Client is signalling it finished initialization. Nothing to do.
		return map[string]any{}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return s.handleToolsList()
	case "tools/call":
		return s.handleToolsCall(ctx, params)
	default:
		return nil, &rpcError{
			Code:    errCodeMethodNotFound,
			Message: "unknown method: " + method,
		}
	}
}

func (s *Server) handleInitialize() (any, *rpcError) {
	return map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities": map[string]any{
			"tools": map[string]any{"listChanged": false},
		},
		"serverInfo": map[string]any{
			"name":    ServerName,
			"version": ServerVersion,
		},
	}, nil
}

func (s *Server) handleToolsList() (any, *rpcError) {
	return map[string]any{"tools": s.tools}, nil
}

// toolCallParams mirrors the JSON-RPC params for tools/call.
type toolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func (s *Server) handleToolsCall(ctx context.Context, raw json.RawMessage) (any, *rpcError) {
	var p toolCallParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &rpcError{
			Code:    errCodeInvalidParams,
			Message: "invalid params: " + err.Error(),
		}
	}
	if p.Name == "" {
		return nil, &rpcError{Code: errCodeInvalidParams, Message: "tool name is required"}
	}

	args := p.Arguments
	if len(args) == 0 {
		args = []byte("{}")
	}

	switch p.Name {
	case "reverb_lookup":
		return s.callLookup(ctx, args)
	case "reverb_store":
		return s.callStore(ctx, args)
	case "reverb_invalidate":
		return s.callInvalidate(ctx, args)
	case "reverb_delete_entry":
		return s.callDeleteEntry(ctx, args)
	case "reverb_stats":
		return s.callStats(ctx)
	default:
		return nil, &rpcError{
			Code:    errCodeMethodNotFound,
			Message: "unknown tool: " + p.Name,
		}
	}
}

// --- tool argument types --------------------------------------------------

type lookupArgs struct {
	Namespace string `json:"namespace"`
	Prompt    string `json:"prompt"`
	ModelID   string `json:"model_id"`
}

type sourceArg struct {
	SourceID    string `json:"source_id"`
	ContentHash string `json:"content_hash"`
}

type storeArgs struct {
	Namespace    string            `json:"namespace"`
	Prompt       string            `json:"prompt"`
	ModelID      string            `json:"model_id"`
	Response     string            `json:"response"`
	ResponseMeta map[string]string `json:"response_meta"`
	Sources      []sourceArg       `json:"sources"`
	TTLSeconds   int               `json:"ttl_seconds"`
}

type invalidateArgs struct {
	SourceID string `json:"source_id"`
}

type deleteEntryArgs struct {
	EntryID string `json:"entry_id"`
}

// --- tool dispatchers -----------------------------------------------------

func (s *Server) callLookup(ctx context.Context, raw json.RawMessage) (any, *rpcError) {
	var a lookupArgs
	if err := decodeStrict(raw, &a); err != nil {
		return nil, invalidParams(err)
	}
	if a.Namespace == "" {
		return toolError("namespace is required"), nil
	}
	if a.Prompt == "" {
		return toolError("prompt is required"), nil
	}
	result, err := s.client.Lookup(ctx, reverb.LookupRequest{
		Namespace: a.Namespace,
		Prompt:    a.Prompt,
		ModelID:   a.ModelID,
	})
	if err != nil {
		return toolError("lookup failed: " + err.Error()), nil
	}
	return toolSuccess(lookupResultJSON(result)), nil
}

func (s *Server) callStore(ctx context.Context, raw json.RawMessage) (any, *rpcError) {
	var a storeArgs
	if err := decodeStrict(raw, &a); err != nil {
		return nil, invalidParams(err)
	}
	if a.Namespace == "" {
		return toolError("namespace is required"), nil
	}
	if a.Prompt == "" {
		return toolError("prompt is required"), nil
	}
	if a.Response == "" {
		return toolError("response is required"), nil
	}
	sources, err := convertSources(a.Sources)
	if err != nil {
		return toolError(err.Error()), nil
	}
	var ttl time.Duration
	if a.TTLSeconds > 0 {
		ttl = time.Duration(a.TTLSeconds) * time.Second
	}
	entry, err := s.client.Store(ctx, reverb.StoreRequest{
		Namespace:    a.Namespace,
		Prompt:       a.Prompt,
		ModelID:      a.ModelID,
		Response:     a.Response,
		ResponseMeta: a.ResponseMeta,
		Sources:      sources,
		TTL:          ttl,
	})
	if err != nil {
		return toolError("store failed: " + err.Error()), nil
	}
	return toolSuccess(map[string]any{
		"id":         entry.ID,
		"created_at": entry.CreatedAt.Format(time.RFC3339Nano),
	}), nil
}

func (s *Server) callInvalidate(ctx context.Context, raw json.RawMessage) (any, *rpcError) {
	var a invalidateArgs
	if err := decodeStrict(raw, &a); err != nil {
		return nil, invalidParams(err)
	}
	if a.SourceID == "" {
		return toolError("source_id is required"), nil
	}
	count, err := s.client.Invalidate(ctx, a.SourceID)
	if err != nil {
		return toolError("invalidate failed: " + err.Error()), nil
	}
	return toolSuccess(map[string]any{"invalidated_count": count}), nil
}

func (s *Server) callDeleteEntry(ctx context.Context, raw json.RawMessage) (any, *rpcError) {
	var a deleteEntryArgs
	if err := decodeStrict(raw, &a); err != nil {
		return nil, invalidParams(err)
	}
	if a.EntryID == "" {
		return toolError("entry_id is required"), nil
	}
	if err := s.client.InvalidateEntry(ctx, a.EntryID); err != nil {
		return toolError("delete failed: " + err.Error()), nil
	}
	return toolSuccess(map[string]any{"deleted": true, "entry_id": a.EntryID}), nil
}

func (s *Server) callStats(ctx context.Context) (any, *rpcError) {
	stats, err := s.client.Stats(ctx)
	if err != nil {
		return toolError("stats failed: " + err.Error()), nil
	}
	return toolSuccess(map[string]any{
		"total_entries":       stats.TotalEntries,
		"namespaces":          stats.Namespaces,
		"exact_hits_total":    stats.ExactHitsTotal,
		"semantic_hits_total": stats.SemanticHitsTotal,
		"misses_total":        stats.MissesTotal,
		"invalidations_total": stats.InvalidationsTotal,
		"hit_rate":            stats.HitRate,
	}), nil
}

// --- helpers --------------------------------------------------------------

func decodeStrict(raw json.RawMessage, dst any) error {
	return json.Unmarshal(raw, dst)
}

func invalidParams(err error) *rpcError {
	return &rpcError{Code: errCodeInvalidParams, Message: "invalid arguments: " + err.Error()}
}

// toolError builds an MCP tool error payload. Tool-level errors are *not*
// JSON-RPC errors — they ride inside a normal `result` with `isError: true`.
// This matches how MCP clients surface errors to the LLM.
func toolError(msg string) toolCallResult {
	return toolCallResult{
		Content: []toolContent{{Type: "text", Text: msg}},
		IsError: true,
	}
}

// toolSuccess wraps a Go value as an MCP tool result. The value is serialized
// to JSON and embedded in a single text content block.
func toolSuccess(payload any) toolCallResult {
	buf, err := json.Marshal(payload)
	if err != nil {
		return toolError("failed to encode result: " + err.Error())
	}
	return toolCallResult{
		Content: []toolContent{{Type: "text", Text: string(buf)}},
	}
}

// lookupResultJSON converts a *reverb.LookupResponse into a plain map suitable
// for JSON serialization. We flatten the nested CacheEntry fields we expose
// over HTTP/gRPC, hex-encoding the prompt hash and source content hashes.
func lookupResultJSON(r *reverb.LookupResponse) map[string]any {
	out := map[string]any{
		"hit":        r.Hit,
		"tier":       r.Tier,
		"similarity": r.Similarity,
	}
	if r.Entry != nil {
		out["entry"] = entryJSON(r.Entry)
	}
	return out
}

func entryJSON(e *store.CacheEntry) map[string]any {
	sources := make([]map[string]string, len(e.SourceHashes))
	for i, sref := range e.SourceHashes {
		sources[i] = map[string]string{
			"source_id":    sref.SourceID,
			"content_hash": hex.EncodeToString(sref.ContentHash[:]),
		}
	}
	expires := ""
	if !e.ExpiresAt.IsZero() {
		expires = e.ExpiresAt.Format(time.RFC3339Nano)
	}
	return map[string]any{
		"id":            e.ID,
		"created_at":    e.CreatedAt.Format(time.RFC3339Nano),
		"expires_at":    expires,
		"namespace":     e.Namespace,
		"prompt":        e.PromptText,
		"model_id":      e.ModelID,
		"response":      e.ResponseText,
		"response_meta": e.ResponseMeta,
		"sources":       sources,
		"hit_count":     e.HitCount,
	}
}

func convertSources(args []sourceArg) ([]store.SourceRef, error) {
	if len(args) == 0 {
		return nil, nil
	}
	refs := make([]store.SourceRef, len(args))
	for i, a := range args {
		refs[i].SourceID = a.SourceID
		if a.ContentHash == "" {
			continue
		}
		decoded, err := hex.DecodeString(strings.TrimPrefix(a.ContentHash, "0x"))
		if err != nil {
			return nil, fmt.Errorf("invalid content_hash for source %q: %w", a.SourceID, err)
		}
		if len(decoded) != sha256.Size {
			return nil, fmt.Errorf("content_hash for source %q must be %d bytes (got %d)", a.SourceID, sha256.Size, len(decoded))
		}
		copy(refs[i].ContentHash[:], decoded)
	}
	return refs, nil
}

func mustEncode(r response) []byte {
	buf, err := json.Marshal(r)
	if err != nil {
		// Encoding a fixed-shape struct should never fail; if it does, the
		// best we can do is return a minimal parse-error envelope.
		fallback := `{"jsonrpc":"2.0","error":{"code":-32603,"message":"encode failed"}}`
		return []byte(fallback)
	}
	return buf
}

// buildToolDescriptors returns the static tool catalogue. Schemas are written
// as raw JSON so we don't have to encode them at construction time; a bad
// schema would fail the compile-time test, not a runtime request.
func buildToolDescriptors() []toolDescriptor {
	return []toolDescriptor{
		{
			Name:        "reverb_lookup",
			Description: "Check the Reverb semantic cache for a response matching the given prompt. Returns hit=true with the cached response on an exact or semantic match, or hit=false on a miss.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"namespace": {"type": "string", "description": "Logical partition (e.g. \"support-bot\")"},
					"prompt":    {"type": "string", "description": "The prompt text to look up"},
					"model_id":  {"type": "string", "description": "LLM model identifier (e.g. \"gpt-4o\")"}
				},
				"required": ["namespace", "prompt"]
			}`),
		},
		{
			Name:        "reverb_store",
			Description: "Store a prompt/response pair in the Reverb cache, with optional source-document lineage so the entry can be invalidated when its sources change.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"namespace":     {"type": "string"},
					"prompt":        {"type": "string"},
					"model_id":      {"type": "string"},
					"response":      {"type": "string"},
					"response_meta": {"type": "object", "additionalProperties": {"type": "string"}},
					"sources": {
						"type": "array",
						"items": {
							"type": "object",
							"properties": {
								"source_id":    {"type": "string"},
								"content_hash": {"type": "string", "description": "hex-encoded SHA-256 of the source content (64 chars)"}
							},
							"required": ["source_id"]
						}
					},
					"ttl_seconds": {"type": "integer", "minimum": 0}
				},
				"required": ["namespace", "prompt", "response"]
			}`),
		},
		{
			Name:        "reverb_invalidate",
			Description: "Invalidate every cache entry whose lineage includes the given source ID. Typically invoked when a source document has been updated or deleted.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"source_id": {"type": "string"}
				},
				"required": ["source_id"]
			}`),
		},
		{
			Name:        "reverb_delete_entry",
			Description: "Delete a single cache entry by ID.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"entry_id": {"type": "string"}
				},
				"required": ["entry_id"]
			}`),
		},
		{
			Name:        "reverb_stats",
			Description: "Return aggregate cache statistics: total entries, namespaces, hit/miss counts, and hit rate.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {}}`),
		},
	}
}

