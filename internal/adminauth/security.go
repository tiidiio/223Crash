package adminauth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	argonMemory      uint32 = 64 * 1024
	argonIterations  uint32 = 3
	argonParallelism uint8  = 2
	argonKeyLength   uint32 = 32
	argonSaltLength         = 16

	minPasswordLength = 12
	maxPasswordLength = 128
)

var (
	ErrInvalidPassword = errors.New("password must contain between 12 and 128 bytes")
	ErrRandomFailure   = errors.New("cryptographic random generation failed")
)

func HashPassword(password string) (string, error) {
	if len(password) < minPasswordLength ||
		len(password) > maxPasswordLength {
		return "", ErrInvalidPassword
	}

	salt := make([]byte, argonSaltLength)

	if _, err := rand.Read(salt); err != nil {
		return "", ErrRandomFailure
	}

	hash := argon2.IDKey(
		[]byte(password),
		salt,
		argonIterations,
		argonMemory,
		argonParallelism,
		argonKeyLength,
	)

	encoded := fmt.Sprintf(
		"$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		argonMemory,
		argonIterations,
		argonParallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	)

	return encoded, nil
}

func VerifyPassword(password, encodedHash string) (bool, error) {
	if len(password) < minPasswordLength ||
		len(password) > maxPasswordLength {
		return false, ErrInvalidPassword
	}

	parts := strings.Split(encodedHash, "$")
	if len(parts) != 6 ||
		parts[0] != "" ||
		parts[1] != "argon2id" ||
		parts[2] != "v=19" {
		return false, errors.New("invalid password hash format")
	}

	var memory, iterations uint64
	var parallelism uint64

	for _, parameter := range strings.Split(parts[3], ",") {
		key, value, ok := strings.Cut(parameter, "=")
		if !ok {
			return false, errors.New("invalid password hash parameters")
		}

		n, err := strconv.ParseUint(value, 10, 32)
		if err != nil {
			return false, errors.New("invalid password hash parameters")
		}

		switch key {
		case "m":
			memory = n
		case "t":
			iterations = n
		case "p":
			parallelism = n
		default:
			return false, errors.New("unsupported password hash parameter")
		}
	}

	// Bound the parameters before invoking Argon2id.
	if memory < 8*1024 || memory > 128*1024 ||
		iterations < 1 || iterations > 10 ||
		parallelism < 1 || parallelism > 8 {
		return false, errors.New("password hash parameters outside safe bounds")
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) < 16 || len(salt) > 64 {
		return false, errors.New("invalid password hash salt")
	}

	expected, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(expected) != int(argonKeyLength) {
		return false, errors.New("invalid password hash digest")
	}

	actual := argon2.IDKey(
		[]byte(password),
		salt,
		uint32(iterations),
		uint32(memory),
		uint8(parallelism),
		uint32(len(expected)),
	)

	return subtle.ConstantTimeCompare(actual, expected) == 1, nil
}

func GenerateToken() (string, error) {
	return generateOpaqueToken(32)
}

func GenerateCSRFToken() (string, error) {
	return generateOpaqueToken(32)
}

func generateOpaqueToken(size int) (string, error) {
	raw := make([]byte, size)

	if _, err := rand.Read(raw); err != nil {
		return "", ErrRandomFailure
	}

	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func VerifyCSRFToken(expected, supplied string) bool {
	if expected == "" || supplied == "" {
		return false
	}

	return subtle.ConstantTimeCompare(
		[]byte(expected),
		[]byte(supplied),
	) == 1
}
