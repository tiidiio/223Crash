package main

import (
	"log"
	"net/url"
	"os"
	"strings"
)

// checkWalletURL empêche un démarrage production pointant vers le mock wallet ou un wallet en clair.
func checkWalletURL(raw string) {
	if os.Getenv("APP_ENV") != "production" {
		return
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		log.Fatal("WALLET_URL invalide")
	}
	switch strings.ToLower(u.Hostname()) {
	case "localhost", "127.0.0.1", "::1", "0.0.0.0":
		log.Fatal("WALLET_URL pointe vers une adresse locale (mock wallet ?) en production")
	}
	if u.Scheme != "https" {
		log.Fatal("WALLET_URL doit être en https en production")
	}
}
