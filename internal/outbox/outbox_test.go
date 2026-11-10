package outbox

import (
	"context"
	"testing"
	"time"
)

func TestOutboxLeaseAndPublishFlow(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryOutboxStore()

	event := OutboxEvent{
		ID:        "evt_001",
		Aggregate: "crash_round",
		EventType: "RoundStarted",
		Payload:   `{"round_id":"r1","multiplier":1.0}`,
	}

	if err := store.Save(ctx, event); err != nil {
		t.Fatalf("échec sauvegarde outbox: %v", err)
	}

	events, err := store.AcquireLease(ctx, "worker_alpha", 10, 5*time.Second)
	if err != nil || len(events) != 1 {
		t.Fatalf("échec acquisition lease: err=%v, count=%d", err, len(events))
	}

	if events[0].Status != StatusProcessing || events[0].LockedBy != "worker_alpha" {
		t.Errorf("état ou propriétaire du lease invalide: status=%s, locked_by=%s", events[0].Status, events[0].LockedBy)
	}

	if err := store.MarkPublished(ctx, "evt_001"); err != nil {
		t.Fatalf("échec marquage publié: %v", err)
	}

	pending, _ := store.AcquireLease(ctx, "worker_alpha", 10, 5*time.Second)
	if len(pending) != 0 {
		t.Errorf("attendu 0 événement en attente après publication, trouvé %d", len(pending))
	}
}
