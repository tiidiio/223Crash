package main

import (
  "encoding/json"
  "net/http"
  "os"
  "strings"
  "time"
  "github.com/tiidiio/223Crash/internal/engine"
)

type runtimeLiveResponse struct {
  Runtime string `json:"runtime"`
  Round struct {
    ID string `json:"id"`
    State string `json:"state"`
    Phase string `json:"phase"`
    BettingOpenedAt *time.Time `json:"bettingOpenedAt,omitempty"`
    StartedAt *time.Time `json:"startedAt,omitempty"`
    ElapsedMs int64 `json:"elapsedMs"`
  } `json:"round"`
  Multiplier float64 `json:"multiplier"`
  Fairness struct {
    ServerSeedHash string `json:"serverSeedHash"`
    ClientSeed string `json:"clientSeed"`
    Nonce int64 `json:"nonce"`
  } `json:"fairness"`
  Stats struct {
    ActiveConnections int `json:"activeConnections"`
    ActiveBets int `json:"activeBets"`
  } `json:"stats"`
  Health struct {
    Status string `json:"status"`
    Mongo string `json:"mongo"`
    Engine string `json:"engine"`
    UptimeSec int64 `json:"uptimeSec"`
    Version string `json:"version"`
  } `json:"health"`
  Timestamp time.Time `json:"timestamp"`
}

var crashStartTime = time.Now()

func handleRuntimeLive(eng *engine.CrashEngine, gw *gateway) http.HandlerFunc {
  return func(w http.ResponseWriter, r *http.Request) {
    if os.Getenv("APP_ENV") == "production" {
      secret := os.Getenv("CRASH_INTERNAL_SECRET")
      if secret != "" {
        hdr := r.Header.Get("X-Internal-Secret")
        remote := r.RemoteAddr
        fwd := r.Header.Get("X-Forwarded-For")
        isLoop := strings.HasPrefix(remote, "127.0.0.1") || strings.HasPrefix(remote, "[::1]") || strings.HasPrefix(fwd, "127.0.0.1")
        if hdr != secret && !isLoop {
          http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
          return
        }
      }
    }
    var resp runtimeLiveResponse
    resp.Runtime = "223crash"
    if eng != nil {
      if round, err := eng.CurrentRound(); err == nil && round != nil {
        resp.Round.ID = round.ID
        resp.Round.State = string(round.State)
        resp.Round.Phase = string(round.State)
        if !round.BettingOpenedAt.IsZero() {
          t := round.BettingOpenedAt
          resp.Round.BettingOpenedAt = &t
        }
        if !round.StartedAt.IsZero() {
          t := round.StartedAt
          resp.Round.StartedAt = &t
          resp.Round.ElapsedMs = time.Since(t).Milliseconds()
        }
      }
      resp.Multiplier = eng.CurrentMultiplier()
      sHash, cSeed, nonce := eng.CurrentFairness()
      resp.Fairness.ServerSeedHash = sHash
      resp.Fairness.ClientSeed = cSeed
      resp.Fairness.Nonce = nonce
    }
    if gw != nil {
      conns, bets := gw.liveStats()
      resp.Stats.ActiveConnections = conns
      resp.Stats.ActiveBets = bets
    }
    resp.Health.Status = "ok"
    resp.Health.Mongo = "connected"
    resp.Health.Engine = "running"
    resp.Health.UptimeSec = int64(time.Since(crashStartTime).Seconds())
    resp.Health.Version = "1.0.0"
    resp.Timestamp = time.Now().UTC()
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(resp)
  }
}
