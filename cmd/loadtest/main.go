package main

import (
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
)

type stats struct {
	connOK   atomic.Int64
	connFail atomic.Int64
	betOK    atomic.Int64
	betFail  atomic.Int64
	cashOK   atomic.Int64
	msgRecv  atomic.Int64
	latSumMs atomic.Int64
}

func genToken(secret, operatorID, playerID string) string {
	claims := jwt.MapClaims{
		"operatorId": operatorID,
		"sub":        playerID,
		"exp":        time.Now().Add(10 * time.Minute).Unix(),
		"iat":        time.Now().Unix(),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS512, claims)
	s, _ := tok.SignedString([]byte(secret))
	return s
}

func main() {
	conns := flag.Int("c", 100, "connexions WS simultanées")
	wsURL := flag.String("url", "ws://127.0.0.1:3000", "ws gateway url")
	op := flag.String("op", "demo", "operator code")
	duration := flag.Duration("d", 30*time.Second, "durée test")
	flag.Parse()

	secret := os.Getenv("JWT_SECRET")
	if secret == "" {
		// fallback .env
		b, _ := os.ReadFile(".env")
		for _, line := range stringToLines(string(b)) {
			if len(line) > 11 && line[:11] == "JWT_SECRET=" {
				secret = trimQuotes(line[11:])
				break
			}
		}
	}
	if secret == "" {
		log.Fatal("JWT_SECRET manquant")
	}

	st := &stats{}
	var wg sync.WaitGroup
	start := time.Now()
	endAt := start.Add(*duration)
	log.Printf("loadtest: %d conns vers %s op=%s pendant %s", *conns, *wsURL, *op, *duration)

	for i := 0; i < *conns; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			player := fmt.Sprintf("load_%d_%d", idx, rand.Intn(100000))
			token := genToken(secret, *op, player)
			u, _ := url.Parse(*wsURL)
			q := u.Query()
			q.Set("token", token)
			u.RawQuery = q.Encode()

			dialStart := time.Now()
			conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
			if err != nil {
				st.connFail.Add(1)
				return
			}
			st.connOK.Add(1)
			st.latSumMs.Add(time.Since(dialStart).Milliseconds())
			defer conn.Close()

			go func() {
				for {
					_, _, err := conn.ReadMessage()
					if err != nil {
						return
					}
					st.msgRecv.Add(1)
				}
			}()

			ticker := time.NewTicker(time.Duration(300+rand.Intn(700)) * time.Millisecond)
			defer ticker.Stop()
			betSeq := 0
			for time.Now().Before(endAt) {
				<-ticker.C
				betSeq++
				betID := fmt.Sprintf("loadbet_%d_%d_%d", idx, time.Now().UnixMilli(), betSeq)
				amt := float64(10 + rand.Intn(90))
				msg := map[string]any{"action": "bet", "betId": betID, "slot": []string{"A", "B"}[rand.Intn(2)], "amount": amt}
				if rand.Float64() < 0.7 {
					ac := 1.5 + rand.Float64()*5
					msg["autoCashout"] = ac
				}
				if err := conn.WriteJSON(msg); err != nil {
					st.betFail.Add(1)
					return
				}
				st.betOK.Add(1)
				if rand.Float64() < 0.4 {
					time.Sleep(time.Duration(500+rand.Intn(1500)) * time.Millisecond)
					co := map[string]any{"action": "cashout", "slot": msg["slot"]}
					_ = conn.WriteJSON(co)
					st.cashOK.Add(1)
				}
			}
		}(i)
		if i%100 == 0 {
			time.Sleep(50 * time.Millisecond)
		}
	}

	go func() {
		for time.Now().Before(endAt) {
			time.Sleep(2 * time.Second)
			elapsed := time.Since(start).Seconds()
			fmt.Printf("[%.0fs] conn ok=%d fail=%d bet ok=%d fail=%d cash=%d recv=%d avgConnMs=%d\n",
				elapsed, st.connOK.Load(), st.connFail.Load(), st.betOK.Load(), st.betFail.Load(), st.cashOK.Load(), st.msgRecv.Load(),
				func() int64 {
					ok := st.connOK.Load()
					if ok == 0 {
						return 0
					}
					return st.latSumMs.Load() / ok
				}(),
			)
		}
	}()

	wg.Wait()
	fmt.Printf("\n=== FINAL %d conns en %.0fs ===\n", *conns, time.Since(start).Seconds())
	fmt.Printf("conn ok=%d fail=%d | bet ok=%d fail=%d | cash=%d | msg recv=%d\n", st.connOK.Load(), st.connFail.Load(), st.betOK.Load(), st.betFail.Load(), st.cashOK.Load(), st.msgRecv.Load())
	failRate := float64(st.connFail.Load()) / float64(*conns) * 100
	if failRate > 0.5 {
		fmt.Printf("FAIL taux echec %.2f%% >0.5%%\n", failRate)
		os.Exit(1)
	}
	fmt.Printf("PASS %.2f%% echec <0.5%%\n", failRate)
}

func stringToLines(s string) []string {
	var out []string
	cur := ""
	for _, ch := range s {
		if ch == '\n' {
			out = append(out, cur)
			cur = ""
		} else {
			cur += string(ch)
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}
func trimQuotes(s string) string {
	if len(s) >= 2 && ((s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'')) {
		return s[1 : len(s)-1]
	}
	return s
}
