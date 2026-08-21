package memory_test

import (
	"testing"

	"github.com/yeboahd24/ussd-lab/internal/session"
	"github.com/yeboahd24/ussd-lab/internal/storage/memory"
	"github.com/yeboahd24/ussd-lab/internal/storage/storagetest"
)

// The memory store is validated by the shared conformance suite, not by
// bespoke tests. Every future store -- SQLite, and later Redis -- runs this
// same suite (ADR-003).
func TestMemoryStore_Conformance(t *testing.T) {
	t.Parallel()

	storagetest.Run(t, "memory", func(t *testing.T) session.SessionStore {
		return memory.New()
	})
}
