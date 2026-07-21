package store

import (
	"context"
	"testing"
)

// The memStore arm always runs — it is hermetic and needs no external services.
func init() {
	storeFactories = append(storeFactories, storeFactory{
		name: "mem",
		new: func(t *testing.T) Store {
			st, err := NewMemStore()
			if err != nil {
				t.Fatalf("NewMemStore: %v", err)
			}
			if err := st.Migrate(context.Background()); err != nil {
				t.Fatalf("Migrate: %v", err)
			}
			t.Cleanup(func() { _ = st.Close() })
			return st
		},
	})
}
