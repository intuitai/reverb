package memory_test

import (
	"testing"

	"github.com/nobelk/reverb/pkg/store"
	"github.com/nobelk/reverb/pkg/store/conformance"
	"github.com/nobelk/reverb/pkg/store/memory"
)

func TestMemoryStoreConformance(t *testing.T) {
	conformance.RunStoreConformance(t, func(t *testing.T) store.Store {
		return memory.New()
	})
}
