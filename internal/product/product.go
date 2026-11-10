package product

import (
	"errors"
	"fmt"

	"github.com/tiidiio/223Gaming/internal/version"
)

// ProductID représente un identifiant opaque de produit.
type ProductID string

const (
	CrashGameID ProductID = "223crash"
)

// Definition encapsule les métadonnées d'un produit du catalogue B2B.
type Definition struct {
	ID          ProductID       `json:"id"`
	Name        string          `json:"name"`
	Version     version.Version `json:"version"`
	Description string          `json:"description,omitempty"`
	Enabled     bool            `json:"enabled"`
}

// NewCrashGameDefinition retourne la définition canonique de 223CRASH.
func NewCrashGameDefinition(productVersionStr string) (Definition, error) {
	v, err := version.Parse(productVersionStr)
	if err != nil {
		return Definition{}, fmt.Errorf("version de produit invalide pour 223CRASH: %w", err)
	}

	return Definition{
		ID:          CrashGameID,
		Name:        "223CRASH",
		Version:     v,
		Description: "B2B Provably Fair Crash Game Engine",
		Enabled:     true,
	}, nil
}

// Validate s'assure que le produit respecte le périmètre strict.
func (d Definition) Validate() error {
	if d.ID != CrashGameID {
		return errors.New("périmètre strict violé : seul 223CRASH est autorisé dans ce module")
	}
	return d.Version.Validate()
}
