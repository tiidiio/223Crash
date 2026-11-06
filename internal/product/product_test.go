package product

import (
	"testing"
)

func TestNewCrashGameDefinition(t *testing.T) {
	def, err := NewCrashGameDefinition("2.2.0")
	if err != nil {
		t.Fatalf("échec de la création de la définition Crash: %v", err)
	}

	if def.ID != CrashGameID {
		t.Errorf("attendu ID %q, obtenu %q", CrashGameID, def.ID)
	}

	if def.Name != "223CRASH" {
		t.Errorf("attendu nom '223CRASH', obtenu %q", def.Name)
	}

	if def.Version.String() != "2.2.0" {
		t.Errorf("attendu version '2.2.0', obtenu %q", def.Version.String())
	}

	if !def.Enabled {
		t.Errorf("le produit 223CRASH doit être activé par défaut")
	}
}

func TestNewCrashGameDefinitionInvalidVersion(t *testing.T) {
	_, err := NewCrashGameDefinition("invalide.version")
	if err == nil {
		t.Errorf("attendu une erreur de parsing de version, obtenu nil")
	}
}

func TestProductValidation(t *testing.T) {
	def, _ := NewCrashGameDefinition("1.0.0")

	// Test de validation nominale
	if err := def.Validate(); err != nil {
		t.Errorf("validation inattendue en échec: %v", err)
	}

	// Test de violation de périmètre (ID altéré)
	def.ID = "autre-jeu"
	if err := def.Validate(); err == nil {
		t.Errorf("attendu une erreur de violation de périmètre, obtenu nil")
	}
}
