package contract_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/bszymi/spine/internal/secrets"
	"github.com/bszymi/spine/internal/secrets/contract"
)

// memClient is a minimal in-memory SecretClient used to validate that
// the contract harness wires correctly. Providers (AWS, file) bring
// their own implementations and call RunContract from their own
// _test.go files.
type memClient struct {
	mu    sync.Mutex
	store map[secrets.SecretRef]string
}

func (m *memClient) Get(_ context.Context, ref secrets.SecretRef) (secrets.SecretValue, secrets.VersionID, error) {
	if _, _, err := secrets.ParseRef(ref); err != nil {
		return secrets.SecretValue{}, "", err
	}
	m.mu.Lock()
	v, ok := m.store[ref]
	m.mu.Unlock()
	if !ok {
		return secrets.SecretValue{}, "", errors.Join(secrets.ErrSecretNotFound)
	}
	return secrets.NewSecretValue([]byte(v)), secrets.VersionID("v1"), nil
}

func (m *memClient) Invalidate(_ context.Context, ref secrets.SecretRef) error {
	if _, _, err := secrets.ParseRef(ref); err != nil {
		return err
	}
	return nil
}

func TestRunContract_AgainstInMemoryFake(t *testing.T) {
	contract.RunContract(t, func() secrets.SecretClient {
		return &memClient{
			store: map[secrets.SecretRef]string{
				contract.RefRuntimeDB: contract.FixtureRuntimeDBValue,
				contract.RefGit:       contract.FixtureGitValue,
			},
		}
	})
}
