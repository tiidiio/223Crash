package ws

import (
	"encoding/json"
	"testing"
	"time"
)

type TestPayload struct {
	RoundID    string  `json:"round_id"`
	Multiplier float64 `json:"multiplier"`
}

func TestHubBroadcastAndClientLifecycle(t *testing.T) {
	hub := NewHub()
	go hub.Run()

	client := &Client{
		ID:   "client_test_1",
		Send: make(chan []byte, 10),
		Hub:  hub,
	}

	hub.register <- client
	time.Sleep(10 * time.Millisecond)

	if hub.ClientCount() != 1 {
		t.Fatalf("attendu 1 client connecté, trouvé %d", hub.ClientCount())
	}

	payload := TestPayload{
		RoundID:    "round_crash_99",
		Multiplier: 1.45,
	}

	err := hub.Broadcast("CRASH_TICK", payload)
	if err != nil {
		t.Fatalf("échec du broadcast: %v", err)
	}

	select {
	case data := <-client.Send:
		var msg Message
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Fatalf("échec unmarshal message ws: %v", err)
		}
		if msg.Type != "CRASH_TICK" {
			t.Errorf("type de message attendu CRASH_TICK, reçu %s", msg.Type)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timeout en attente du message WebSocket")
	}

	hub.unregister <- client
	time.Sleep(10 * time.Millisecond)

	if hub.ClientCount() != 0 {
		t.Errorf("attendu 0 client après déconnexion, trouvé %d", hub.ClientCount())
	}
}
