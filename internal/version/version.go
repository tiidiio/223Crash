package version

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Version représente une version sémantique conforme à SemVer 2.0.0.
type Version struct {
	Major      uint64   `json:"major"`
	Minor      uint64   `json:"minor"`
	Patch      uint64   `json:"patch"`
	PreRelease []string `json:"pre_release,omitempty"`
	Build      []string `json:"build,omitempty"`
}

// New crée et valide une nouvelle structure Version.
func New(major, minor, patch uint64, preRelease, build []string) (Version, error) {
	v := Version{
		Major:      major,
		Minor:      minor,
		Patch:      patch,
		PreRelease: preRelease,
		Build:      build,
	}
	if err := v.Validate(); err != nil {
		return Version{}, err
	}
	return v, nil
}

// Validate vérifie la conformité interne de la version.
func (v Version) Validate() error {
	for _, pr := range v.PreRelease {
		if err := validateIdentifier(pr, true); err != nil {
			return fmt.Errorf("préversion invalide %q: %w", pr, err)
		}
	}
	for _, b := range v.Build {
		if err := validateIdentifier(b, false); err != nil {
			return fmt.Errorf("métadonnée de build invalide %q: %w", b, err)
		}
	}
	return nil
}

// String restitue la version au format textuel SemVer 2.0.0.
func (v Version) String() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%d.%d.%d", v.Major, v.Minor, v.Patch)
	if len(v.PreRelease) > 0 {
		sb.WriteString("-")
		sb.WriteString(strings.Join(v.PreRelease, "."))
	}
	if len(v.Build) > 0 {
		sb.WriteString("+")
		sb.WriteString(strings.Join(v.Build, "."))
	}
	return sb.String()
}

// Compare compare deux versions selon les règles de priorité SemVer 2.0.0.
// Renvoie -1 si v < other, 0 si v == other, 1 si v > other.
func (v Version) Compare(other Version) int {
	if v.Major != other.Major {
		if v.Major > other.Major {
			return 1
		}
		return -1
	}
	if v.Minor != other.Minor {
		if v.Minor > other.Minor {
			return 1
		}
		return -1
	}
	if v.Patch != other.Patch {
		if v.Patch > other.Patch {
			return 1
		}
		return -1
	}

	// Gestion des préversions
	if len(v.PreRelease) == 0 && len(other.PreRelease) > 0 {
		return 1
	}
	if len(v.PreRelease) > 0 && len(other.PreRelease) == 0 {
		return -1
	}
	if len(v.PreRelease) == 0 && len(other.PreRelease) == 0 {
		return 0
	}

	maxLen := len(v.PreRelease)
	if len(other.PreRelease) < maxLen {
		maxLen = len(other.PreRelease)
	}

	for i := 0; i < maxLen; i++ {
		p1 := v.PreRelease[i]
		p2 := other.PreRelease[i]

		num1, isNum1 := parseNumeric(p1)
		num2, isNum2 := parseNumeric(p2)

		if isNum1 && isNum2 {
			if num1 != num2 {
				if num1 > num2 {
					return 1
				}
				return -1
			}
		} else if isNum1 && !isNum2 {
			return -1
		} else if !isNum1 && isNum2 {
			return 1
		} else {
			if p1 != p2 {
				if p1 > p2 {
					return 1
				}
				return -1
			}
		}
	}

	if len(v.PreRelease) < len(other.PreRelease) {
		return -1
	}
	if len(v.PreRelease) > len(other.PreRelease) {
		return 1
	}

	return 0
}

// Equal vérifie l'égalité sémantique (ignore Build Metadata).
func (v Version) Equal(other Version) bool {
	return v.Compare(other) == 0
}

// LessThan vérifie si v est strictement inférieure à other.
func (v Version) LessThan(other Version) bool {
	return v.Compare(other) < 0
}

// GreaterThan vérifie si v est strictement supérieure à other.
func (v Version) GreaterThan(other Version) bool {
	return v.Compare(other) > 0
}

// MarshalJSON implémente l'interface json.Marshaler.
func (v Version) MarshalJSON() ([]byte, error) {
	return json.Marshal(v.String())
}

// UnmarshalJSON implémente l'interface json.Unmarshaler.
func (v *Version) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	parsed, err := Parse(s)
	if err != nil {
		return err
	}
	*v = parsed
	return nil
}

func validateIdentifier(id string, isPreRelease bool) error {
	if id == "" {
		return fmt.Errorf("identifiant vide interdit")
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		valid := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '-'
		if !valid {
			return fmt.Errorf("caractère non autorisé %q", c)
		}
	}
	if isPreRelease && isNumeric(id) && len(id) > 1 && id[0] == '0' {
		return fmt.Errorf("zéro initial interdit pour un identifiant numérique de préversion")
	}
	return nil
}

func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func parseNumeric(s string) (uint64, bool) {
	if !isNumeric(s) {
		return 0, false
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}
