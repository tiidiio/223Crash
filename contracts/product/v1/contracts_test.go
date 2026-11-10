package v1

import (
	"encoding/json"
	"testing"
	"time"
)

func TestSessionHandshakeSerialization(t *testing.T) {
	req := SessionHandshakeRequest{
		Token:      "test-token-123",
		ProductID:  "223crash",
		ClientIP:   "127.0.0.1",
		ClientTime: time.Now(),
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("échec du marshaling SessionHandshakeRequest: %v", err)
	}

	var decoded SessionHandshakeRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("échec du unmarshaling SessionHandshakeRequest: %v", err)
	}

	if decoded.Token != req.Token || decoded.ProductID != req.ProductID {
		t.Errorf("les valeurs décodées ne correspondent pas à la requête d'origine")
	}
}

func TestFinancialPayloadSerialization(t *testing.T) {
	betReq := BetRequest{
		CorrelationID:  "corr-001",
		IdempotencyKey: "idem-001",
		SessionID:      "sess-001",
		PlayerID:       "player-001",
		OperatorID:     "op-001",
		RoundID:        "round-001",
		Amount:         5000,
		Currency:       "XOF",
	}

	data, err := json.Marshal(betReq)
	if err != nil {
		t.Fatalf("échec du marshaling BetRequest: %v", err)
	}

	var decoded BetRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("échec du unmarshaling BetRequest: %v", err)
	}

	if decoded.Amount != 5000 || decoded.Currency != "XOF" {
		t.Errorf("montant ou devise financière incorrects : obtenu %d %s", decoded.Amount, decoded.Currency)
	}
}

func TestGameEventEnvelope(t *testing.T) {
	tick := MultiplierTickPayload{
		RoundID:    "round-001",
		Multiplier: 2.45,
		ElapsedMs:  1250,
	}

	payloadBytes, _ := json.Marshal(tick)

	event := GameEvent{
		EventID:       "evt-001",
		CorrelationID: "corr-001",
		Type:          EventMultiplierTick,
		Timestamp:     time.Now().UnixMilli(),
		ProductID:     "223crash",
		RoundID:       "round-001",
		Payload:       payloadBytes,
	}

	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("échec du marshaling GameEvent: %v", err)
	}

	var decoded GameEvent
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("échec du unmarshaling GameEvent: %v", err)
	}

	if decoded.Type != EventMultiplierTick {
		t.Errorf("type d'événement attendu %s, obtenu %s", EventMultiplierTick, decoded.Type)
	}
}
