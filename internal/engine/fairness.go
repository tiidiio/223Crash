package engine

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math"
	"strconv"
)

const DefaultHouseEdge = 0.01

var (
	ErrInvalidServerSeed = errors.New("server seed is empty")
	ErrInvalidClientSeed = errors.New("client seed is empty")
	ErrInvalidNonce      = errors.New("nonce must be >= 0")
	ErrInvalidCrashPoint = errors.New("invalid crash point")
	ErrInvalidHouseEdge  = errors.New("house edge must be >= 0 and < 1")
)

// CrashPoint génère un multiplicateur de crash déterministe.
//
// Le serveur utilise le serverSeed comme clé HMAC.
// Le clientSeed et le nonce constituent le message.
//
// Formule :
//
//	u = HMAC-SHA256(serverSeed, clientSeed:nonce)
//	r = valeur uniforme dans ]0,1]
//	crash = floor(((1-houseEdge) / r) * 100) / 100
//
// Le résultat minimum est 1.00x.
func CrashPoint(serverSeed, clientSeed string, nonce uint64, houseEdge float64) (float64, error) {
	if serverSeed == "" {
		return 0, ErrInvalidServerSeed
	}

	if clientSeed == "" {
		return 0, ErrInvalidClientSeed
	}

	if houseEdge < 0 || houseEdge >= 1 {
		return 0, ErrInvalidHouseEdge
	}

	message := clientSeed + ":" + strconv.FormatUint(nonce, 10)

	mac := hmac.New(sha256.New, []byte(serverSeed))
	_, _ = mac.Write([]byte(message))

	sum := mac.Sum(nil)

	// Les 8 premiers octets deviennent un entier uint64.
	value := binary.BigEndian.Uint64(sum[:8])

	// Conversion déterministe vers ]0,1].
	//
	// +1 évite zéro.
	r := (float64(value) + 1.0) / (float64(math.MaxUint64) + 1.0)

	crash := (1.0 - houseEdge) / r

	// Arrondi vers le bas au centième.
	crash = math.Floor(crash*100.0) / 100.0

	if crash < 1.0 {
		crash = 1.0
	}

	return crash, nil
}

// Verify recalcule le crash point et vérifie qu'il correspond.
//
// La comparaison utilise une tolérance extrêmement faible afin
// d'éviter les problèmes de représentation floating-point.
func Verify(serverSeed, clientSeed string, nonce uint64, crashPoint float64) bool {
	calculated, err := CrashPoint(
		serverSeed,
		clientSeed,
		nonce,
		DefaultHouseEdge,
	)
	if err != nil {
		return false
	}

	return math.Abs(calculated-crashPoint) < 0.0000001
}

// VerifyWithHouseEdge permet de vérifier avec un house edge configurable.
func VerifyWithHouseEdge(
	serverSeed,
	clientSeed string,
	nonce uint64,
	crashPoint float64,
	houseEdge float64,
) bool {
	calculated, err := CrashPoint(
		serverSeed,
		clientSeed,
		nonce,
		houseEdge,
	)
	if err != nil {
		return false
	}

	return math.Abs(calculated-crashPoint) < 0.0000001
}

// HashServerSeed permet de publier un engagement cryptographique
// du server seed AVANT le round.
//
// Le server seed lui-même doit rester secret jusqu'au reveal.
func HashServerSeed(serverSeed string) (string, error) {
	if serverSeed == "" {
		return "", ErrInvalidServerSeed
	}

	hash := sha256.Sum256([]byte(serverSeed))
	return hex.EncodeToString(hash[:]), nil
}
