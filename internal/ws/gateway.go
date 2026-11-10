package ws

import (
	"encoding/json"
	"sync"
	"time"
)

// Message représente une trame WebSocket échangée avec le client ou le studio.
type Message struct {
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
	Timestamp int64           `json:"timestamp"`
}

// Client représente une connexion WebSocket active avec isolation B2B par OperatorID.
type Client struct {
	ID         string
	OperatorID string
	Send       chan []byte
	Hub        *Hub
}

// Hub gère l'ensemble des connexions, l'isolation par opérateur et la diffusion des messages en temps réel.
type Hub struct {
	mu         sync.RWMutex
	clients    map[*Client]bool
	byOperator map[string]map[*Client]bool // Indexation par opérateur pour l'isolation B2B
	broadcast  chan []byte
	register   chan *Client
	unregister chan *Client
}

// NewHub instancie un nouveau Hub WebSocket.
func NewHub() *Hub {
	return &Hub{
		clients:    make(map[*Client]bool),
		byOperator: make(map[string]map[*Client]bool),
		broadcast:  make(chan []byte, 256),
		register:   make(chan *Client),
		unregister: make(chan *Client),
	}
}

// Run démarre la boucle principale de gestion des connexions du Hub.
func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			h.clients[client] = true
			if client.OperatorID != "" {
				if h.byOperator[client.OperatorID] == nil {
					h.byOperator[client.OperatorID] = make(map[*Client]bool)
				}
				h.byOperator[client.OperatorID][client] = true
			}
			h.mu.Unlock()

		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				if client.OperatorID != "" {
					if opMap, exists := h.byOperator[client.OperatorID]; exists {
						delete(opMap, client)
						if len(opMap) == 0 {
							delete(h.byOperator, client.OperatorID)
						}
					}
				}
				close(client.Send)
			}
			h.mu.Unlock()

		case message := <-h.broadcast:
			h.mu.RLock()
			for client := range h.clients {
				select {
				case client.Send <- message:
				default:
					go func(c *Client) {
						h.unregister <- c
					}(client)
				}
			}
			h.mu.RUnlock()
		}
	}
}

// Broadcast diffuse un message à tous les clients connectés.
func (h *Hub) Broadcast(msgType string, payload any) error {
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	msg := Message{
		Type:      msgType,
		Payload:   rawPayload,
		Timestamp: time.Now().UnixMilli(),
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	h.broadcast <- data
	return nil
}

// BroadcastToOperator diffuse un message uniquement aux clients d'un opérateur B2B spécifique.
func (h *Hub) BroadcastToOperator(operatorID string, msgType string, payload any) error {
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	msg := Message{
		Type:      msgType,
		Payload:   rawPayload,
		Timestamp: time.Now().UnixMilli(),
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	if opClients, ok := h.byOperator[operatorID]; ok {
		for client := range opClients {
			select {
			case client.Send <- data:
			default:
				go func(c *Client) {
					h.unregister <- c
				}(client)
			}
		}
	}
	return nil
}

// ClientCount retourne le nombre de connexions actives.
func (h *Hub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// RegisterClient enregistre un client de manière sécurisée via le channel.
func (h *Hub) RegisterClient(c *Client) {
	h.register <- c
}

// UnregisterClient désenregistre un client de manière sécurisée via le channel.
func (h *Hub) UnregisterClient(c *Client) {
	h.unregister <- c
}
