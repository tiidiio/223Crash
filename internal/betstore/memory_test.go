package betstore

import "testing"

func TestMemoryStoreContract(t *testing.T) {
	runContract(t, func(t *testing.T) Store { return NewMemory() })
}
