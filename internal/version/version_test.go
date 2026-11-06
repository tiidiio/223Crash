package version

import (
	"testing"
)

func TestParseValidVersions(t *testing.T) {
	tests := []struct {
		input    string
		expected Version
	}{
		{
			input:    "2.2.0",
			expected: Version{Major: 2, Minor: 2, Patch: 0},
		},
		{
			input:    "1.0.0-alpha",
			expected: Version{Major: 1, Minor: 0, Patch: 0, PreRelease: []string{"alpha"}},
		},
		{
			input:    "2.2.0-rc.1+build.123",
			expected: Version{Major: 2, Minor: 2, Patch: 0, PreRelease: []string{"rc", "1"}, Build: []string{"build", "123"}},
		},
	}

	for _, tc := range tests {
		v, err := Parse(tc.input)
		if err != nil {
			t.Fatalf("échec du parsing pour %q: %v", tc.input, err)
		}
		if v.Major != tc.expected.Major || v.Minor != tc.expected.Minor || v.Patch != tc.expected.Patch {
			t.Errorf("pour %q, obtenu %v, attendu %v", tc.input, v, tc.expected)
		}
		if v.String() != tc.input {
			t.Errorf("stringification incorrecte: obtenu %q, attendu %q", v.String(), tc.input)
		}
	}
}

func TestParseInvalidVersions(t *testing.T) {
	invalids := []string{
		"invalid",
		"1.2",
		"1.2.3.4",
		"01.2.3",
		"1.02.3",
		"1.2.03",
		"1.2.3-01",
		"1.2.3+",
		"1.2.3-",
	}

	for _, input := range invalids {
		_, err := Parse(input)
		if err == nil {
			t.Errorf("attendu une erreur pour l'entrée invalide %q, obtenu nil", input)
		}
	}
}

func TestSemVerPrecedence(t *testing.T) {
	comparisons := []struct {
		v1     string
		v2     string
		result int
	}{
		{"1.0.0-alpha", "1.0.0-alpha.1", -1},
		{"1.0.0-alpha.1", "1.0.0-alpha.beta", -1},
		{"1.0.0-alpha.beta", "1.0.0-beta", -1},
		{"1.0.0-beta", "1.0.0-beta.2", -1},
		{"1.0.0-beta.2", "1.0.0-beta.11", -1},
		{"1.0.0-beta.11", "1.0.0-rc.1", -1},
		{"1.0.0-rc.1", "1.0.0", -1},
		{"2.1.1", "2.1.0", 1},
		{"2.2.0+build.1", "2.2.0+build.2", 0},
	}

	for _, tc := range comparisons {
		ver1, err1 := Parse(tc.v1)
		ver2, err2 := Parse(tc.v2)
		if err1 != nil || err2 != nil {
			t.Fatalf("erreur de test sur %q ou %q", tc.v1, tc.v2)
		}

		res := ver1.Compare(ver2)
		if res != tc.result {
			t.Errorf("comparaison de %s et %s: attendu %d, obtenu %d", tc.v1, tc.v2, tc.result, res)
		}
	}
}
