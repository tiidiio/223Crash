// gentoken génère un JWT HS512 valide pour tester la gateway en local — mêmes claims
// que wsClaims dans cmd/server/gateway.go (sub=playerId, operatorId).
package main

import (
	"fmt"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Println("usage: gentoken <playerId> <operatorId>")
		os.Exit(1)
	}
	secret := os.Getenv("JWT_SECRET")
	if secret == "" {
		fmt.Println("JWT_SECRET manquant dans l'env")
		os.Exit(1)
	}
	claims := jwt.MapClaims{
		"sub":        os.Args[1],
		"operatorId": os.Args[2],
		"exp":        time.Now().Add(1 * time.Hour).Unix(),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS512, claims)
	signed, err := tok.SignedString([]byte(secret))
	if err != nil {
		fmt.Println("erreur signature:", err)
		os.Exit(1)
	}
	fmt.Println(signed)
}
