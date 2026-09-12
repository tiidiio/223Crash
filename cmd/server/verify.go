package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/tiidiio/223Gaming/223Crash/internal/engine"
	"github.com/tiidiio/223Gaming/223Crash/internal/mongobridge"
)

// handleVerify implémente GET /verify/{roundId} : recalcule le crash point à partir du
// serverSeed révélé et vérifie le commit-reveal contre le serverSeedHash publié avant le round.
func handleVerify(store *mongobridge.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		roundID := strings.TrimPrefix(r.URL.Path, "/verify/")
		if roundID == "" || roundID == "ping" {
			http.NotFound(w, r)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		var crashDoc mongobridge.BroadcastMessage
		crashFilter := bson.D{
			{Key: "type", Value: "CRASH"},
			{Key: "data.roundId", Value: roundID},
		}
		if err := store.Broadcast.FindOne(ctx, crashFilter).Decode(&crashDoc); err != nil {
			http.Error(w, `{"error":"round introuvable ou pas encore crashé"}`, http.StatusNotFound)
			return
		}

		serverSeed, _ := crashDoc.Data["serverSeed"].(string)
		clientSeed, _ := crashDoc.Data["clientSeed"].(string)
		crashPoint, _ := crashDoc.Data["crashPoint"].(float64)
		nonce := toInt64(crashDoc.Data["nonce"])

		var startDoc mongobridge.BroadcastMessage
		startFilter := bson.D{
			{Key: "type", Value: "ROUND_START"},
			{Key: "data.roundId", Value: roundID},
		}
		_ = store.Broadcast.FindOne(ctx, startFilter).Decode(&startDoc)
		publishedHash, _ := startDoc.Data["serverSeedHash"].(string)

		hashValid := publishedHash != "" && engine.HashServerSeed(serverSeed) == publishedHash
		crashPointValid, err := engine.VerifyCrashPoint(serverSeed, clientSeed, nonce, crashPoint)

		resp := map[string]any{
			"roundId":         roundID,
			"serverSeed":      serverSeed,
			"clientSeed":      clientSeed,
			"nonce":           nonce,
			"crashPoint":      crashPoint,
			"serverSeedHash":  publishedHash,
			"hashValid":       hashValid,
			"crashPointValid": crashPointValid,
		}
		if err != nil {
			resp["error"] = err.Error()
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}

func toInt64(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int32:
		return int64(n)
	case float64:
		return int64(n)
	default:
		return 0
	}
}
