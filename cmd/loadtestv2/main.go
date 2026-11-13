package main

import (
 "crypto/tls"
 "flag"
 "fmt"
 "log"
 "math"
 "math/rand"
 "net/url"
 "os"
 "sort"
 "strings"
 "sync"
 "sync/atomic"
 "time"
 "github.com/golang-jwt/jwt/v5"
 "github.com/gorilla/websocket"
)

type stats struct {
 connOK, connFail, betOK, betFail, cashOK, msgRecv atomic.Int64
 mu sync.Mutex
 betLats []int64
 cashLats []int64
}

func percentile(a []int64, p float64) int64 {
 if len(a) == 0 { return 0 }
 sort.Slice(a, func(i, j int) bool { return a[i] < a[j] })
 idx := int(math.Ceil(p/100*float64(len(a)))) - 1
 if idx < 0 { idx = 0 }
 if idx >= len(a) { idx = len(a) - 1 }
 return a[idx]
}

func genToken(secret, op, player string) string {
 claims := jwt.MapClaims{"operatorId": op, "sub": player, "exp": time.Now().Add(10 * time.Minute).Unix(), "iat": time.Now().Unix()}
 tok := jwt.NewWithClaims(jwt.SigningMethodHS512, claims)
 s, _ := tok.SignedString([]byte(secret))
 return s
}
func trimQuotes(s string) string {
 if len(s) >= 2 && ((s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'')) {
  return s[1 : len(s)-1]
 }
 return s
}

func main() {
 conns := flag.Int("c", 100, "conns")
 wsURL := flag.String("url", "ws://127.0.0.1:3000", "ws url")
 op := flag.String("op", "demo", "operator")
 dur := flag.Duration("d", 60*time.Second, "duree")
 seed := flag.Int64("seed", 42, "seed")
 flag.Parse()
 rand.Seed(*seed)
 secret := os.Getenv("JWT_SECRET")
 if secret == "" {
  b, _ := os.ReadFile(".env")
  for _, l := range strings.Split(string(b), "\n") {
   if len(strings.TrimSpace(l)) > 11 && strings.TrimSpace(l)[:11] == "JWT_SECRET=" {
    secret = trimQuotes(strings.TrimSpace(l)[11:])
    break
   }
  }
 }
 if secret == "" { log.Fatal("JWT_SECRET manquant") }
 st := &stats{}
 var wg sync.WaitGroup
 start := time.Now()
 endAt := start.Add(*dur)
 log.Printf("loadtestV2: %d conns vers %s op=%s dur=%s seed=%d", *conns, *wsURL, *op, *dur, *seed)
 for i := 0; i < *conns; i++ {
  wg.Add(1)
  go func(idx int) {
   defer wg.Done()
   player := fmt.Sprintf("loadV2_%d_%d", idx, rand.Intn(100000))
   token := genToken(secret, *op, player)
   u, _ := url.Parse(*wsURL)
   q := u.Query()
   q.Set("token", token)
   u.RawQuery = q.Encode()
   dialer := websocket.Dialer{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, HandshakeTimeout: 5 * time.Second}
   conn, _, err := dialer.Dial(u.String(), nil)
   if err != nil { st.connFail.Add(1); return }
   st.connOK.Add(1)
   defer conn.Close()
   betCh := make(chan time.Time, 100)
   cashCh := make(chan time.Time, 100)
   go func() {
    for {
     var m map[string]any
     if err := conn.ReadJSON(&m); err != nil { return }
     st.msgRecv.Add(1)
     if t, ok := m["type"]; ok {
      if t == "BET_ACCEPTED" {
       select { case ts := <-betCh: st.mu.Lock(); st.betLats = append(st.betLats, time.Since(ts).Milliseconds()); st.mu.Unlock(); default: }
      } else if t == "CASHOUT_ACCEPTED" {
       select { case ts := <-cashCh: st.mu.Lock(); st.cashLats = append(st.cashLats, time.Since(ts).Milliseconds()); st.mu.Unlock(); default: }
      }
     }
    }
   }()
   ticker := time.NewTicker(time.Duration(300+rand.Intn(700)) * time.Millisecond)
   defer ticker.Stop()
   betSeq := 0
   for time.Now().Before(endAt) {
    <-ticker.C
    betSeq++
    betID := fmt.Sprintf("V2bet_%d_%d_%d", idx, time.Now().UnixMilli(), betSeq)
    amt := float64(10 + rand.Intn(90))
    msg := map[string]any{"action": "bet", "betId": betID, "slot": []string{"A", "B"}[rand.Intn(2)], "amount": amt}
    if rand.Float64() < 0.7 { ac := 1.5 + rand.Float64()*5; msg["autoCashout"] = ac }
    ts := time.Now()
    select { case betCh <- ts: default: }
    if err := conn.WriteJSON(msg); err != nil { st.betFail.Add(1); return }
    st.betOK.Add(1)
    if rand.Float64() < 0.4 {
     time.Sleep(time.Duration(500+rand.Intn(1500)) * time.Millisecond)
     co := map[string]any{"action": "cashout", "slot": msg["slot"]}
     ts2 := time.Now()
     select { case cashCh <- ts2: default: }
     _ = conn.WriteJSON(co)
     st.cashOK.Add(1)
    }
   }
  }(i)
  if i%100 == 0 { time.Sleep(50 * time.Millisecond) }
 }
 go func() {
  for time.Now().Before(endAt) {
   time.Sleep(2 * time.Second)
   elapsed := time.Since(start).Seconds()
   st.mu.Lock()
   bl := append([]int64{}, st.betLats...)
   cl := append([]int64{}, st.cashLats...)
   st.mu.Unlock()
   fmt.Printf("[%.0fs] conn ok=%d fail=%d bet ok=%d fail=%d cash=%d recv=%d bet P50=%d P95=%d P99=%d | cash P50=%d P95=%d P99=%d\n",
    elapsed, st.connOK.Load(), st.connFail.Load(), st.betOK.Load(), st.betFail.Load(), st.cashOK.Load(), st.msgRecv.Load(),
    percentile(bl, 50), percentile(bl, 95), percentile(bl, 99),
    percentile(cl, 50), percentile(cl, 95), percentile(cl, 99))
  }
 }()
 wg.Wait()
 st.mu.Lock()
 bl := st.betLats; cl := st.cashLats
 st.mu.Unlock()
 fmt.Printf("\n=== FINAL V2 %d conns en %.0fs ===\n", *conns, time.Since(start).Seconds())
 fmt.Printf("conn ok=%d fail=%d | bet ok=%d fail=%d | cash=%d | recv=%d\n", st.connOK.Load(), st.connFail.Load(), st.betOK.Load(), st.betFail.Load(), st.cashOK.Load(), st.msgRecv.Load())
 fmt.Printf("BET lat ms: count=%d P50=%d P95=%d P99=%d P99.9=%d\n", len(bl), percentile(bl, 50), percentile(bl, 95), percentile(bl, 99), percentile(bl, 99.9))
 fmt.Printf("CASHOUT lat ms: count=%d P50=%d P95=%d P99=%d P99.9=%d\n", len(cl), percentile(cl, 50), percentile(cl, 95), percentile(cl, 99), percentile(cl, 99.9))
 failRate := float64(st.connFail.Load()) / float64(*conns) * 100
 if failRate > 0.5 { fmt.Printf("FAIL %.2f%% >0.5%%\n", failRate); os.Exit(1) }
 fmt.Printf("PASS %.2f%% <0.5%%\n", failRate)
}
