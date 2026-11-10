package outbox

import (
	"context"
	"errors"
	"time"
)

type EventStatus string

const (
	StatusPending    EventStatus = "PENDING"
	StatusProcessing EventStatus = "PROCESSING"
	StatusPublished  EventStatus = "PUBLISHED"
	StatusFailed     EventStatus = "FAILED"
)

type OutboxEvent struct {
	ID        string      `json:"id"`
	Aggregate string      `json:"aggregate"`
	EventType string      `json:"event_type"`
	Payload   string      `json:"payload"`
	Status    EventStatus `json:"status"`
	Attempts  int         `json:"attempts"`
	LockedAt  time.Time   `json:"locked_at,omitempty"`
	LockedBy  string      `json:"locked_by,omitempty"`
	CreatedAt time.Time   `json:"created_at"`
	UpdatedAt time.Time   `json:"updated_at"`
}

type MemoryOutboxStore struct {
	events map[string]OutboxEvent
}

func NewMemoryOutboxStore() *MemoryOutboxStore {
	return &MemoryOutboxStore{
		events: make(map[string]OutboxEvent),
	}
}

func (s *MemoryOutboxStore) Save(ctx context.Context, event OutboxEvent) error {
	if event.ID == "" {
		return errors.New("lID de lévénement outbox est obligatoire")
	}
	event.Status = StatusPending
	event.CreatedAt = time.Now()
	event.UpdatedAt = time.Now()
	s.events[event.ID] = event
	return nil
}

func (s *MemoryOutboxStore) AcquireLease(ctx context.Context, workerID string, limit int, leaseDuration time.Duration) ([]OutboxEvent, error) {
	now := time.Now()
	var acquired []OutboxEvent

	for id, ev := range s.events {
		if len(acquired) >= limit {
			break
		}
		if ev.Status == StatusPending || (ev.Status == StatusProcessing && now.Sub(ev.LockedAt) > leaseDuration) {
			ev.Status = StatusProcessing
			ev.LockedAt = now
			ev.LockedBy = workerID
			ev.Attempts++
			ev.UpdatedAt = now
			s.events[id] = ev
			acquired = append(acquired, ev)
		}
	}
	return acquired, nil
}

func (s *MemoryOutboxStore) MarkPublished(ctx context.Context, eventID string) error {
	ev, found := s.events[eventID]
	if !found {
		return errors.New("événement outbox introuvable")
	}
	ev.Status = StatusPublished
	ev.UpdatedAt = time.Now()
	s.events[eventID] = ev
	return nil
}
