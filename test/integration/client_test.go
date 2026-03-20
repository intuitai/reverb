//go:build integration

package integration

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClientLifecycle(t *testing.T) {
	addr := serverAddr(t)

	// 1. Store a cache entry
	contentHash := sha256.Sum256([]byte("guide-content-v1"))
	storeBody := map[string]any{
		"namespace": "integration-test",
		"prompt":    "How do I reset my password?",
		"model_id":  "gpt-4o",
		"response":  "Go to Settings > Security > Reset Password.",
		"sources": []map[string]string{
			{"source_id": "doc:password-reset", "content_hash": hex.EncodeToString(contentHash[:])},
		},
		"ttl_seconds": 3600,
	}

	resp := doPost(t, addr+"/v1/store", storeBody)
	require.Equal(t, 201, resp.StatusCode)
	var storeResult map[string]any
	readJSON(t, resp, &storeResult)
	entryID, ok := storeResult["id"].(string)
	require.True(t, ok, "store response should contain id")
	require.NotEmpty(t, entryID)
	t.Logf("stored entry: %s", entryID)

	// 2. Lookup exact same prompt → exact hit
	lookupBody := map[string]any{
		"namespace": "integration-test",
		"prompt":    "How do I reset my password?",
		"model_id":  "gpt-4o",
	}
	resp = doPost(t, addr+"/v1/lookup", lookupBody)
	require.Equal(t, 200, resp.StatusCode)
	var lookupResult map[string]any
	readJSON(t, resp, &lookupResult)
	assert.Equal(t, true, lookupResult["hit"])
	assert.Equal(t, "exact", lookupResult["tier"])
	entry := lookupResult["entry"].(map[string]any)
	assert.Equal(t, "Go to Settings > Security > Reset Password.", entry["response"])

	// 3. Check stats
	resp = doGet(t, addr+"/v1/stats")
	require.Equal(t, 200, resp.StatusCode)
	var stats map[string]any
	readJSON(t, resp, &stats)
	assert.GreaterOrEqual(t, stats["total_entries"].(float64), float64(1))

	// 4. Invalidate by source
	invalidateBody := map[string]any{
		"source_id": "doc:password-reset",
	}
	resp = doPost(t, addr+"/v1/invalidate", invalidateBody)
	require.Equal(t, 200, resp.StatusCode)
	var invResult map[string]any
	readJSON(t, resp, &invResult)
	assert.GreaterOrEqual(t, invResult["invalidated_count"].(float64), float64(1))

	// 5. Lookup again → miss (entry was invalidated)
	resp = doPost(t, addr+"/v1/lookup", lookupBody)
	require.Equal(t, 200, resp.StatusCode)
	readJSON(t, resp, &lookupResult)
	assert.Equal(t, false, lookupResult["hit"])
}
