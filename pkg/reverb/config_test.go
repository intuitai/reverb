package reverb_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nobelk/reverb/pkg/reverb"
)

func TestConfig_Validate_Valid(t *testing.T) {
	cfg := reverb.DefaultConfig()
	require.NoError(t, cfg.Validate())
}

func TestConfig_Validate_InvalidThreshold(t *testing.T) {
	tests := []struct {
		name      string
		threshold float32
	}{
		{"negative", -0.1},
		{"above_one", 1.5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := reverb.DefaultConfig()
			cfg.SimilarityThreshold = tt.threshold
			assert.Error(t, cfg.Validate())
		})
	}
}

func TestConfig_Validate_InvalidTopK(t *testing.T) {
	cfg := reverb.DefaultConfig()
	cfg.SemanticTopK = 0
	assert.Error(t, cfg.Validate())
}

func TestConfig_Validate_NegativeTTL(t *testing.T) {
	cfg := reverb.DefaultConfig()
	cfg.DefaultTTL = -1 * time.Second
	assert.Error(t, cfg.Validate())
}

func TestConfig_ApplyDefaults(t *testing.T) {
	cfg := reverb.Config{}
	cfg.ApplyDefaults()
	assert.Equal(t, "default", cfg.DefaultNamespace)
	assert.Equal(t, 24*time.Hour, cfg.DefaultTTL)
	assert.Equal(t, float32(0.95), cfg.SimilarityThreshold)
	assert.Equal(t, 5, cfg.SemanticTopK)
	assert.Equal(t, "memory", cfg.Store.Backend)
	assert.Equal(t, "flat", cfg.Vector.Backend)
	assert.NotNil(t, cfg.Clock)
}

func TestDefaultConfig_Values(t *testing.T) {
	cfg := reverb.DefaultConfig()
	assert.Equal(t, "default", cfg.DefaultNamespace)
	assert.Equal(t, 24*time.Hour, cfg.DefaultTTL)
	assert.Equal(t, float32(0.95), cfg.SimilarityThreshold)
	assert.Equal(t, 5, cfg.SemanticTopK)
	assert.True(t, cfg.ScopeByModel)
}
