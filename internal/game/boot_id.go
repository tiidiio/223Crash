package game

import (
	"strconv"
	"time"
)

// bootID identifie de façon unique ce démarrage du processus (ms epoch en base 36).
// Il rend les IDs moteur (round, bet) uniques entre redémarrages, ce qui garantit
// l'unicité des clés d'idempotence ledger (credit:<op>:<player>:<betID>).
var bootID = strconv.FormatInt(time.Now().UnixMilli(), 36)

// uniqueID construit "<prefix>-<bootID>-<n>".
func uniqueID(prefix string, n uint64) string {
	return prefix + "-" + bootID + "-" + strconv.FormatUint(n, 10)
}
