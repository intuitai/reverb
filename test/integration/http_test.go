//go:build integration

package integration

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTTP_Healthz(t *testing.T) {
	addr := serverAddr(t)
	resp := doGet(t, addr+"/healthz")
	require.Equal(t, 200, resp.StatusCode)
	var result map[string]any
	readJSON(t, resp, &result)
	assert.Equal(t, "ok", result["status"])
}

func TestHTTP_Stats(t *testing.T) {
	addr := serverAddr(t)
	resp := doGet(t, addr+"/v1/stats")
	require.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
}

func TestHTTP_Lookup_MissingFields(t *testing.T) {
	addr := serverAddr(t)
	// Missing namespace
	resp := doPost(t, addr+"/v1/lookup", map[string]string{"prompt": "hello"})
	require.Equal(t, 400, resp.StatusCode)
}

func TestHTTP_Lookup_BadJSON(t *testing.T) {
	addr := serverAddr(t)
	resp, err := http.Post(addr+"/v1/lookup", "application/json", bytes.NewReader([]byte("not json")))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, 400, resp.StatusCode)
}

func TestHTTP_Store_BadJSON(t *testing.T) {
	addr := serverAddr(t)
	resp, err := http.Post(addr+"/v1/store", "application/json", bytes.NewReader([]byte("{invalid")))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, 400, resp.StatusCode)
}

func TestHTTP_Lookup_Miss(t *testing.T) {
	addr := serverAddr(t)
	body := map[string]string{
		"namespace": "http-test-miss",
		"prompt":    "something completely unique nobody would cache xyz123",
	}
	resp := doPost(t, addr+"/v1/lookup", body)
	require.Equal(t, 200, resp.StatusCode)
	var result map[string]any
	readJSON(t, resp, &result)
	assert.Equal(t, false, result["hit"])
}

func TestHTTP_StoreAndLookup(t *testing.T) {
	addr := serverAddr(t)

	// Store
	storeBody := map[string]any{
		"namespace": "http-test-sl",
		"prompt":    "What is the meaning of life?",
		"model_id":  "gpt-4o",
		"response":  "42",
	}
	resp := doPost(t, addr+"/v1/store", storeBody)
	require.Equal(t, 201, resp.StatusCode)
	var storeResult map[string]any
	readJSON(t, resp, &storeResult)
	require.NotEmpty(t, storeResult["id"])

	// Lookup → hit
	lookupBody := map[string]string{
		"namespace": "http-test-sl",
		"prompt":    "What is the meaning of life?",
		"model_id":  "gpt-4o",
	}
	resp = doPost(t, addr+"/v1/lookup", lookupBody)
	require.Equal(t, 200, resp.StatusCode)
	var lookupResult map[string]any
	readJSON(t, resp, &lookupResult)
	assert.Equal(t, true, lookupResult["hit"])
	assert.Equal(t, "exact", lookupResult["tier"])
}

func TestHTTP_DeleteEntry(t *testing.T) {
	addr := serverAddr(t)

	// Store an entry
	storeBody := map[string]any{
		"namespace": "http-test-del",
		"prompt":    "deleteme",
		"model_id":  "model",
		"response":  "to be deleted",
	}
	resp := doPost(t, addr+"/v1/store", storeBody)
	require.Equal(t, 201, resp.StatusCode)
	var storeResult map[string]any
	readJSON(t, resp, &storeResult)
	id := storeResult["id"].(string)

	// Delete
	resp = doDelete(t, addr+"/v1/entries/"+id)
	require.Equal(t, 204, resp.StatusCode)
	resp.Body.Close()

	// Lookup → miss
	lookupBody := map[string]string{
		"namespace": "http-test-del",
		"prompt":    "deleteme",
		"model_id":  "model",
	}
	resp = doPost(t, addr+"/v1/lookup", lookupBody)
	require.Equal(t, 200, resp.StatusCode)
	var lookupResult map[string]any
	readJSON(t, resp, &lookupResult)
	assert.Equal(t, false, lookupResult["hit"])
}

func TestHTTP_ContentType(t *testing.T) {
	addr := serverAddr(t)
	resp := doGet(t, addr+"/healthz")
	require.Equal(t, 200, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Content-Type"), "application/json")
	resp.Body.Close()
}

func TestHTTP_NotFound(t *testing.T) {
	addr := serverAddr(t)
	resp := doGet(t, addr+"/nonexistent")
	assert.Equal(t, 404, resp.StatusCode)
	resp.Body.Close()
}
