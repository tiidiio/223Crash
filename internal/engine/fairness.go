package engine

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
)

// HouseEdgePercent définit le RTP cible (97%) utilisé dans le calcul du crash point.
const HouseEdgePercent = 97.0

// verifyTolerance absorbe les erreurs d'arrondi flottant lors de la ré-vérification
// publique d'un crash point (valeur ayant transité par JSON/Mongo côté client).
const verifyTolerance = 1e-9

// GenerateServerSeed génère un server seed cryptographiquement sûr (32 octets, hex).
func GenerateServerSeed() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("engine: generate server seed: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// HashServerSeed retourne le digest SHA-256 hex du server seed, publié avant le round (commit).
func HashServerSeed(serverSeed string) string {
	sum := sha256.Sum256([]byte(serverSeed))
	return hex.EncodeToString(sum[:])
}

// NextClientSeed dérive le client seed du round N à partir du server seed révélé du round N-1.
// Chaîne provably fair : clientSeed(N) = SHA-256(serverSeed révélé du round N-1).
func NextClientSeed(prevRevealedServerSeed string) string {
	sum := sha256.Sum256([]byte(prevRevealedServerSeed))
	return hex.EncodeToString(sum[:])
}

// CrashPoint calcule le multiplicateur de crash déterministe d'un round.
//
// h = HMAC-SHA256(serverSeed, "clientSeed:nonce"), tronqué aux 52 premiers bits
// (13 caractères hex) en un entier e ∈ [0, 2^52 - 1].
// X = e / 2^52 ∈ [0, 1[ — X ne peut JAMAIS atteindre 1 puisque e est strictement
// borné à 2^52 - 1, donc (1 - X) ne peut jamais être nul : aucune protection
// division-par-zéro n'est nécessaire ici (contrairement à une version antérieure
// qui gardait à tort contre X == 0, un cas qui produit simplement le crash point
// minimal 1.00x, déjà couvert par le clamp final).
//
// crash = floor(HouseEdgePercent / (1 - X)) / 100
func CrashPoint(serverSeed, clientSeed string, nonce int64) (float64, error) {
	mac := hmac.New(sha256.New, []byte(serverSeed))
	if _, err := mac.Write([]byte(fmt.Sprintf("%s:%d", clientSeed, nonce))); err != nil {
		return 0, fmt.Errorf("engine: hmac write: %w", err)
	}
	digest := mac.Sum(nil)
	hexDigest := hex.EncodeToString(digest)

	e, err := strconv.ParseUint(hexDigest[:13], 16, 64)
	if err != nil {
		return 0, fmt.Errorf("engine: parse hmac prefix %q: %w", hexDigest[:13], err)
	}

	const maxE = float64(uint64(1) << 52)
	x := float64(e) / maxE

	crash := math.Floor(HouseEdgePercent/(1-x)) / 100.0
	if crash < 1.00 {
		crash = 1.00
	}

	return crash, nil
}

// VerifyCrashPoint recalcule le crash point à partir du server seed révélé, pour l'audit
// public (endpoint GET /verify/{roundId}). Compare avec tolérance flottante — une égalité
// stricte produit des faux négatifs dès que expectedCrash a transité par JSON/Mongo.
func VerifyCrashPoint(serverSeed, clientSeed string, nonce int64, expectedCrash float64) (bool, error) {
	calculated, err := CrashPoint(serverSeed, clientSeed, nonce)
	if err != nil {
		return false, err
	}
	return math.Abs(calculated-expectedCrash) < verifyTolerance, nil
}
