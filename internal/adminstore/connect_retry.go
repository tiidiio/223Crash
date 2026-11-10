package adminstore

import (
	"context"
	"errors"
	"log"
	"time"
)

// ConnectWithRetry réessaie Connect avec un délai croissant. Un réseau mobile
// instable ne doit pas faire tomber le Provider Core au démarrage.
func ConnectWithRetry(ctx context.Context, uri, database string, attempts int) (*Store, error) {
	if attempts < 1 {
		attempts = 1
	}

	var lastErr error
	for i := 1; i <= attempts; i++ {
		store, err := Connect(ctx, uri, database)
		if err == nil {
			return store, nil
		}
		lastErr = err

		if i == attempts || ctx.Err() != nil {
			break
		}

		delay := time.Duration(i*2) * time.Second
		log.Printf("Provider database: tentative %d/%d échouée (%v), nouvel essai dans %s", i, attempts, err, delay)

		select {
		case <-ctx.Done():
			return nil, errors.New("MongoDB connectivity check failed: context ended")
		case <-time.After(delay):
		}
	}

	return nil, lastErr
}
