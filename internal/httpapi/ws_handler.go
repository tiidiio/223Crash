package httpapi

import (
	"net/http"

	"github.com/gorilla/websocket"
	"github.com/tiidiio/223Gaming/internal/ws"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		// En mode B2B / standalone, autoriser les connexions du client studio/frontend
		return true
	},
}

// NewWebSocketHandler crée un handler HTTP pour les connexions WebSocket du jeu Crash.
func NewWebSocketHandler(hub *ws.Hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}

		client := &ws.Client{
			ID:   r.RemoteAddr,
			Send: make(chan []byte, 256),
			Hub:  hub,
		}

		client.Hub.RegisterClient(client) // Note: ajusté si besoin selon les méthodes du Hub

		// Goroutines de lecture/écriture pour maintenir la connexion active
		go func() {
			defer func() {
				client.Hub.UnregisterClient(client)
				conn.Close()
			}()
			for {
				_, _, err := conn.ReadMessage()
				if err != nil {
					break
				}
			}
		}()

		go func() {
			defer conn.Close()
			for message := range client.Send {
				err := conn.WriteMessage(websocket.TextMessage, message)
				if err != nil {
					break
				}
			}
		}()
	}
}
