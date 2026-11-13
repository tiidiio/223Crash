package product

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ProductID est l'identifiant opaque d'un produit : jamais normalisé ni interprété.
type ProductID string

// CrashGameID est l'unique identifiant admis dans ce module.
const CrashGameID = "223crash"

var (
	ErrProductIDMissing      = errors.New("identifiant de produit manquant")
	ErrProductScopeViolation = errors.New("périmètre strict violé : seul 223CRASH est autorisé dans ce module")
	ErrProductNameMissing    = errors.New("nom de produit manquant")
	ErrProductVersionInvalid = errors.New("version de produit invalide")
)

type Definition struct {
	ID          ProductID `json:"id"`
	Name        string    `json:"name"`
	Version     Version   `json:"version"`
	Description string    `json:"description,omitempty"`
	Enabled     bool      `json:"enabled"`
}

type Version struct {
	Major, Minor, Patch int
}

func (v Version) String() string {
	return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
}

// Validate refuse les composantes négatives.
func (v Version) Validate() error {
	if v.Major < 0 || v.Minor < 0 || v.Patch < 0 {
		return fmt.Errorf("%w: composantes négatives (%s)", ErrProductVersionInvalid, v)
	}
	return nil
}

// ParseVersion exige strictement MAJOR.MINOR.PATCH (chiffres uniquement).
func ParseVersion(s string) (Version, error) {
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return Version{}, fmt.Errorf("%w: format MAJOR.MINOR.PATCH attendu, obtenu %q", ErrProductVersionInvalid, s)
	}
	var n [3]int
	for i, p := range parts {
		if p == "" || strings.Trim(p, "0123456789") != "" {
			return Version{}, fmt.Errorf("%w: composante %q non numérique dans %q", ErrProductVersionInvalid, p, s)
		}
		v, err := strconv.Atoi(p)
		if err != nil {
			return Version{}, fmt.Errorf("%w: composante %q hors limites dans %q", ErrProductVersionInvalid, p, s)
		}
		n[i] = v
	}
	return Version{Major: n[0], Minor: n[1], Patch: n[2]}, nil
}

// Validate vérifie le périmètre strict du module et les champs obligatoires.
func (d Definition) Validate() error {
	if strings.TrimSpace(string(d.ID)) == "" {
		return ErrProductIDMissing
	}
	if d.ID != CrashGameID {
		return fmt.Errorf("%w (id=%q)", ErrProductScopeViolation, string(d.ID))
	}
	if strings.TrimSpace(d.Name) == "" {
		return ErrProductNameMissing
	}
	return d.Version.Validate()
}

func NewCrashGameDefinition(versionStr string) (Definition, error) {
	v, err := ParseVersion(versionStr)
	if err != nil {
		return Definition{}, fmt.Errorf("223CRASH: %w", err)
	}
	def := Definition{ID: ProductID(CrashGameID), Name: "223CRASH", Version: v, Description: "B2B Provably Fair Crash", Enabled: true}
	if err := def.Validate(); err != nil {
		return Definition{}, err
	}
	return def, nil
}
