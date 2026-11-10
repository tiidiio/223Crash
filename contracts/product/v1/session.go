package v1

import (
	"encoding/json"
	"time"
)

const (
	ModeDemo        = "demo"
	ModeIntegration = "integration"
)

type SessionHandshakeRequest struct {
	Token      string    `json:"token"`
	ProductID  string    `json:"product_id"`
	Mode       string    `json:"mode,omitempty"`
	ClientIP   string    `json:"client_ip,omitempty"`
	ClientTime time.Time `json:"client_time"`
}

func (r *SessionHandshakeRequest) UnmarshalJSON(data []byte) error {
	type Alias SessionHandshakeRequest
	aux := struct {
		ClientTime string `json:"client_time"`
		*Alias
	}{
		Alias: (*Alias)(r),
	}

	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	if aux.ClientTime != "" {
		parsed, err := time.Parse(time.RFC3339, aux.ClientTime)
		if err != nil {
			parsed, err = time.Parse("2006-01-02T15:04:05Z07:00", aux.ClientTime)
			if err != nil {
				parsed, err = time.Parse("2006-01-02T15:04:05.999Z07:00", aux.ClientTime)
				if err != nil {
					r.ClientTime = time.Now().UTC()
					return nil
				}
			}
		}
		r.ClientTime = parsed
	} else {
		r.ClientTime = time.Now().UTC()
	}

	return nil
}

type SessionHandshakeResponse struct {
	SessionID  string    `json:"session_id,omitempty"`
	PlayerID   string    `json:"player_id,omitempty"`
	OperatorID string    `json:"operator_id,omitempty"`
	Currency   string    `json:"currency,omitempty"`
	Balance    int64     `json:"balance,omitempty"`
	Mode       string    `json:"mode,omitempty"`
	ExpiresAt  time.Time `json:"expires_at,omitempty"`
	IsValid    bool      `json:"is_valid"`
	ErrorMsg   string    `json:"error_msg,omitempty"`
}
