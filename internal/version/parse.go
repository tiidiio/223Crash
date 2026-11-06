package version

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Parse analyse une chaîne de caractères pour en extraire une Version SemVer 2.0.0 valide.
func Parse(input string) (Version, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return Version{}, errors.New("chaîne de version vide")
	}

	// Extraction des métadonnées de build (+)
	var buildParts []string
	if idx := strings.Index(input, "+"); idx != -1 {
		buildStr := input[idx+1:]
		input = input[:idx]
		if buildStr == "" {
			return Version{}, errors.New("métadonnées de build vides après '+'")
		}
		buildParts = strings.Split(buildStr, ".")
	}

	// Extraction des préversions (-)
	var preParts []string
	if idx := strings.Index(input, "-"); idx != -1 {
		preStr := input[idx+1:]
		input = input[:idx]
		if preStr == "" {
			return Version{}, errors.New("préversion vide après '-'")
		}
		preParts = strings.Split(preStr, ".")
	}

	// Séparation Major.Minor.Patch
	parts := strings.Split(input, ".")
	if len(parts) != 3 {
		return Version{}, fmt.Errorf("format de version invalide %q (attendu MAJOR.MINOR.PATCH)", input)
	}

	major, err := parseNumericComponent(parts[0], "major")
	if err != nil {
		return Version{}, err
	}

	minor, err := parseNumericComponent(parts[1], "minor")
	if err != nil {
		return Version{}, err
	}

	patch, err := parseNumericComponent(parts[2], "patch")
	if err != nil {
		return Version{}, err
	}

	v := Version{
		Major:      major,
		Minor:      minor,
		Patch:      patch,
		PreRelease: preParts,
		Build:      buildParts,
	}

	if err := v.Validate(); err != nil {
		return Version{}, err
	}

	return v, nil
}

func parseNumericComponent(s, name string) (uint64, error) {
	if s == "" {
		return 0, fmt.Errorf("composante %s vide", name)
	}
	if len(s) > 1 && s[0] == '0' {
		return 0, fmt.Errorf("zéro initial interdit sur la composante numérique %s (%s)", name, s)
	}
	val, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("composante %s invalide (%s): %w", name, s, err)
	}
	return val, nil
}
