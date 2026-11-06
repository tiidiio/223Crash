// mockwallet simule un endpoint opérateur pour tester le flux DEBIT/CREDIT/ROLLBACK
// en local, sans dépendre d'un vrai opérateur. Solde en mémoire, remis à zéro au redémarrage.
package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
)

// callback reste une struct locale, indépendante de wallet.Callback : le mock simule un
// opérateur tiers, il ne doit dépendre d'aucun type interne au Provider Core. GameID et
// TransactionID sont présents ici uniquement pour observer le flux de corrélation en local
// (log) — le mock ne leur applique aucune logique conditionnelle, conformément à la contrainte
// d'opacité du Provider Core.
type callback struct {
	ID             string `json:"id"`
	Type           string `json:"type"`
	GameID         string `json:"gameId"`
	TransactionID  string `json:"transactionId"`
	OperatorID     string `json:"operatorId"`
	PlayerID       string `json:"playerId"`
	RoundID        string `json:"roundId"`
	BetID          string `json:"betId"`
	Amount         int64  `json:"amount"`
	IdempotencyKey string `json:"idempotencyKey"`
}

type response struct {
	Balance int64 `json:"balance"`
}

const startingBalance = 100000 // 1000.00 en unité mineure (centimes)

type store struct {
	mu       sync.Mutex
	balances map[string]int64 // "operatorId:playerId" -> solde
	seen     map[string]int64 // idempotencyKey -> solde déjà appliqué (anti double-traitement)
}

func newStore() *store {
	return &store{
		balances: make(map[string]int64),
		seen:     make(map[string]int64),
	}
}

func (s *store) key(operatorID, playerID string) string {
	return operatorID + ":" + playerID
}

func (s *store) balance(operatorID, playerID string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := s.key(operatorID, playerID)
	if b, ok := s.balances[k]; ok {
		return b
	}
	s.balances[k] = startingBalance
	return startingBalance
}

func (s *store) apply(cb callback) (int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if bal, done := s.seen[cb.IdempotencyKey]; done {
		return bal, true // déjà traité, on renvoie le même résultat (idempotence)
	}

	k := s.key(cb.OperatorID, cb.PlayerID)
	bal, ok := s.balances[k]
	if !ok {
		bal = startingBalance
	}

	switch cb.Type {
	case "DEBIT":
		if bal < cb.Amount {
			return bal, false
		}
		bal -= cb.Amount
	case "CREDIT", "ROLLBACK":
		bal += cb.Amount
	}

	s.balances[k] = bal
	s.seen[cb.IdempotencyKey] = bal
	return bal, true
}

func main() {
	secret := os.Getenv("WALLET_HMAC_SECRET")
	if secret == "" {
		log.Fatal("WALLET_HMAC_SECRET requis")
	}
	port := os.Getenv("MOCKWALLET_PORT")
	if port == "" {
		port = "9000"
	}

	st := newStore()

	mux := http.NewServeMux()

	mux.HandleFunc("/wallet/balance", func(w http.ResponseWriter, r *http.Request) {
		operatorID := r.URL.Query().Get("operatorId")
		playerID := r.URL.Query().Get("playerId")
		json.NewEncoder(w).Encode(response{Balance: st.balance(operatorID, playerID)})
	})

	mux.HandleFunc("/wallet/callback", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<16))
		if err != nil {
			http.Error(w, "body illisible", http.StatusBadRequest)
			return
		}

		sig := r.Header.Get("X-Signature")
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		expected := hex.EncodeToString(mac.Sum(nil))
		if !hmac.Equal([]byte(sig), []byte(expected)) {
			http.Error(w, "signature invalide", http.StatusUnauthorized)
			return
		}

		var cb callback
		if err := json.Unmarshal(body, &cb); err != nil {
			http.Error(w, "JSON invalide", http.StatusBadRequest)
			return
		}

		newBal, ok := st.apply(cb)
		if !ok {
			http.Error(w, "fonds insuffisants", http.StatusPaymentRequired)
			return
		}

		log.Printf("mockwallet: %s %s game=%s txn=%s player=%s amount=%d nouveau_solde=%d", cb.Type, cb.ID, cb.GameID, cb.TransactionID, cb.PlayerID, cb.Amount, newBal)
		json.NewEncoder(w).Encode(response{Balance: newBal})
	})

	log.Printf("mockwallet: écoute sur :%s (solde initial %d/joueur)", port, startingBalance)
	bind := os.Getenv("MOCKWALLET_BIND")
	if bind == "" {
		bind = "127.0.0.1"
	}
	log.Fatal(http.ListenAndServe(bind+":"+port, mux))
}
