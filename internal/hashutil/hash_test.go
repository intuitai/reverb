package hashutil

import (
	"bytes"
	"testing"
)

func TestSHA256_Deterministic(t *testing.T) {
	input := []byte("hello, reverb")
	hash1 := SHA256(input)
	hash2 := SHA256(input)
	if hash1 != hash2 {
		t.Fatalf("expected identical hashes for identical input, got %x and %x", hash1, hash2)
	}
}

func TestSHA256_DifferentInputs(t *testing.T) {
	hash1 := SHA256([]byte("input-a"))
	hash2 := SHA256([]byte("input-b"))
	if hash1 == hash2 {
		t.Fatalf("expected different hashes for different inputs, both were %x", hash1)
	}
}

func TestSHA256_EmptyString(t *testing.T) {
	hash := SHA256([]byte(""))
	var zero [32]byte
	if hash == zero {
		t.Fatal("expected non-zero hash for empty string")
	}
	if len(hash) != 32 {
		t.Fatalf("expected 32-byte hash, got %d bytes", len(hash))
	}
}

func TestContentHash_LargeInput(t *testing.T) {
	// 10 MB of data
	large := bytes.Repeat([]byte("A"), 10*1024*1024)
	hash := ContentHash(large)
	var zero [32]byte
	if hash == zero {
		t.Fatal("expected non-zero hash for large input")
	}
}

func TestPromptHash_Deterministic(t *testing.T) {
	hash1 := PromptHash("ns", "tell me a joke", "gpt-4")
	hash2 := PromptHash("ns", "tell me a joke", "gpt-4")
	if hash1 != hash2 {
		t.Fatalf("expected identical hashes for identical inputs, got %x and %x", hash1, hash2)
	}
}

func TestPromptHash_DifferentNamespaces(t *testing.T) {
	hash1 := PromptHash("namespace-a", "same prompt", "same-model")
	hash2 := PromptHash("namespace-b", "same prompt", "same-model")
	if hash1 == hash2 {
		t.Fatalf("expected different hashes for different namespaces, both were %x", hash1)
	}
}
