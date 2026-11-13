package product

import (
	"errors"
	"math"
	"testing"
)

func mustCanonicalDefinition(t *testing.T) Definition {
	t.Helper()
	def, err := NewCrashGameDefinition("1.0.0")
	if err != nil {
		t.Fatalf("définition canonique invalide: %v", err)
	}
	return def
}

func TestDefinitionValidateAcceptsCanonical(t *testing.T) {
	def := mustCanonicalDefinition(t)
	if err := def.Validate(); err != nil {
		t.Fatalf("définition valide rejetée: %v", err)
	}
}

func TestDefinitionValidateIgnoresEnabledFlag(t *testing.T) {
	def := mustCanonicalDefinition(t)
	def.Enabled = false
	if err := def.Validate(); err != nil {
		t.Fatalf("un produit désactivé reste une définition valide: %v", err)
	}
}

func TestDefinitionValidateMissingID(t *testing.T) {
	for _, id := range []ProductID{"", " ", "\t\n"} {
		def := mustCanonicalDefinition(t)
		def.ID = id
		if err := def.Validate(); !errors.Is(err, ErrProductIDMissing) {
			t.Errorf("ID %q: attendu ErrProductIDMissing, obtenu %v", id, err)
		}
	}
}

func TestDefinitionValidateZeroValue(t *testing.T) {
	if err := (Definition{}).Validate(); !errors.Is(err, ErrProductIDMissing) {
		t.Fatalf("définition vide: attendu ErrProductIDMissing, obtenu %v", err)
	}
}

func TestDefinitionValidateRejectsForeignGameID(t *testing.T) {
	// Identifiants opaques : aucune normalisation (casse, espaces, homoglyphes).
	for _, id := range []ProductID{"autre-jeu", "223CRASH", " 223crash", "223crash ", "223\u0441rash", "223crash2"} {
		def := mustCanonicalDefinition(t)
		def.ID = id
		if err := def.Validate(); !errors.Is(err, ErrProductScopeViolation) {
			t.Errorf("ID %q: attendu ErrProductScopeViolation, obtenu %v", id, err)
		}
	}
}

func TestDefinitionValidateMissingName(t *testing.T) {
	for _, name := range []string{"", "  ", "\t"} {
		def := mustCanonicalDefinition(t)
		def.Name = name
		if err := def.Validate(); !errors.Is(err, ErrProductNameMissing) {
			t.Errorf("nom %q: attendu ErrProductNameMissing, obtenu %v", name, err)
		}
	}
}

func TestDefinitionValidateRejectsNegativeVersion(t *testing.T) {
	for _, v := range []Version{{-1, 0, 0}, {0, -1, 0}, {0, 0, -1}} {
		def := mustCanonicalDefinition(t)
		def.Version = v
		if err := def.Validate(); !errors.Is(err, ErrProductVersionInvalid) {
			t.Errorf("version %s: attendu ErrProductVersionInvalid, obtenu %v", v, err)
		}
	}
}

func TestDefinitionValidateExtremeVersion(t *testing.T) {
	def := mustCanonicalDefinition(t)
	def.Version = Version{math.MaxInt, math.MaxInt, math.MaxInt}
	if err := def.Validate(); err != nil {
		t.Fatalf("version extrême positive rejetée: %v", err)
	}
}

func TestParseVersionValid(t *testing.T) {
	cases := []struct {
		in   string
		want Version
	}{
		{"1.0.0", Version{1, 0, 0}},
		{"2.2.0", Version{2, 2, 0}},
		{"10.20.30", Version{10, 20, 30}},
		{"0.0.1", Version{0, 0, 1}},
	}
	for _, c := range cases {
		got, err := ParseVersion(c.in)
		if err != nil || got != c.want {
			t.Errorf("ParseVersion(%q) = %v, %v ; attendu %v", c.in, got, err, c.want)
		}
	}
}

func TestParseVersionInvalid(t *testing.T) {
	for _, in := range []string{
		"", "1", "1.0", "1.0.0.0", "invalide.version", "1.0.0abc",
		"-1.0.0", "1.-2.0", "+1.0.0", "1..0", " 1.0.0", "1.0.0 ", "1.0.x",
		"99999999999999999999.0.0",
	} {
		if _, err := ParseVersion(in); !errors.Is(err, ErrProductVersionInvalid) {
			t.Errorf("ParseVersion(%q): attendu ErrProductVersionInvalid, obtenu %v", in, err)
		}
	}
}

func TestNewCrashGameDefinitionIsCanonicalAndValid(t *testing.T) {
	def, err := NewCrashGameDefinition("2.2.0")
	if err != nil {
		t.Fatalf("erreur inattendue: %v", err)
	}
	if def.ID != CrashGameID || def.Name != "223CRASH" || !def.Enabled || def.Version.String() != "2.2.0" {
		t.Errorf("définition canonique altérée: %+v", def)
	}
	if err := def.Validate(); err != nil {
		t.Errorf("définition canonique invalide: %v", err)
	}
	bad, err := NewCrashGameDefinition("abc")
	if err == nil || bad != (Definition{}) {
		t.Errorf("version invalide: attendu erreur et Definition vide, obtenu %+v, %v", bad, err)
	}
}
