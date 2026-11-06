package main

import (
	"context"
	"log"
	"time"
)

// refreshBalance relit le solde réel auprès du wallet après chaque délai et le pousse au client.
// Sert aux mouvements asynchrones (CREDIT, ROLLBACK via la queue) : le dernier relevé fait foi.
func (gw *gateway) refreshBalance(c *client, delays ...time.Duration) {
	go func() {
		for _, d := range delays {
			if d > 0 {
				time.Sleep(d)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			balance, err := gw.walletClient.GetBalance(ctx, c.operatorID, c.playerID)
			cancel()
			if err != nil {
				log.Printf("wallet: GetBalance (refresh) player=%s: %v", c.playerID, err)
				continue
			}
			c.sendJSON(map[string]any{"type": "BALANCE", "balance": centimesToUnits(balance)})
		}
	}()
}
