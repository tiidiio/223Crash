package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	localesDir  = "internal/i18n/locales"
	minKeyCount = 50
)

var expectedLocales = []string{
	"en",
	"fr",
	"es",
	"pt",
	"de",
	"it",
	"ru",
	"tr",
	"sw",
	"ha",
	"yo",
	"zu",
	"ar",
	"zh",
	"ja",
	"ko",
	"th",
	"hi",
	"bn",
	"fa",
}

var expectedFonts = map[string]string{
	"ar": "Noto Naskh Arabic",
	"fa": "Noto Naskh Arabic",
	"zh": "Noto Sans CJK SC",
	"ja": "Noto Sans CJK JP",
	"ko": "Noto Sans CJK KR",
	"th": "Noto Sans Thai",
	"hi": "Noto Sans Devanagari",
}

type localeData struct {
	Code         string
	Name         string
	Direction    string
	Font         string
	Keys         map[string]interface{}
	Placeholders map[string]map[string]struct{}
}

func main() {
	errorsFound := 0

	expectedSet := make(map[string]struct{}, len(expectedLocales))
	for _, code := range expectedLocales {
		expectedSet[code] = struct{}{}
	}

	files, err := os.ReadDir(localesDir)
	if err != nil {
		fmt.Printf("ERROR: cannot read locales directory %q: %v\n", localesDir, err)
		os.Exit(1)
	}

	actualFiles := make(map[string]struct{})

	for _, entry := range files {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()

		if !strings.HasSuffix(name, ".json") {
			continue
		}

		code := strings.TrimSuffix(name, ".json")
		actualFiles[code] = struct{}{}

		if _, ok := expectedSet[code]; !ok {
			fmt.Printf("ERROR: unexpected locale file: %s\n", name)
			errorsFound++
		}
	}

	for _, code := range expectedLocales {
		if _, ok := actualFiles[code]; !ok {
			fmt.Printf("ERROR: missing locale file: %s.json\n", code)
			errorsFound++
		}
	}

	locales := make(map[string]*localeData, len(expectedLocales))

	for _, code := range expectedLocales {
		path := filepath.Join(localesDir, code+".json")

		data, err := loadLocaleFile(path)
		if err != nil {
			fmt.Printf("ERROR: %s: %v\n", code+".json", err)
			errorsFound++
			continue
		}

		meta, ok := getObject(data, "meta")
		if !ok {
			fmt.Printf("ERROR: %s: missing or invalid \"meta\" object\n", code+".json")
			errorsFound++
			continue
		}

		keys, ok := getObject(data, "keys")
		if !ok {
			fmt.Printf("ERROR: %s: missing or invalid \"keys\" object\n", code+".json")
			errorsFound++
			continue
		}

		locale := &localeData{
			Code:         getString(meta, "code"),
			Name:         getString(meta, "name"),
			Direction:    getString(meta, "direction"),
			Font:         getString(meta, "font"),
			Keys:         keys,
			Placeholders: make(map[string]map[string]struct{}),
		}

		locales[code] = locale

		if locale.Code == "" {
			fmt.Printf("ERROR: %s: meta.code is missing or empty\n", code+".json")
			errorsFound++
		} else if locale.Code != code {
			fmt.Printf(
				"ERROR: %s: meta.code=%q does not match filename code %q\n",
				code+".json",
				locale.Code,
				code,
			)
			errorsFound++
		}

		if locale.Name == "" {
			fmt.Printf("ERROR: %s: meta.name is missing or empty\n", code+".json")
			errorsFound++
		}

		expectedDirection := "ltr"
		if code == "ar" || code == "fa" {
			expectedDirection = "rtl"
		}

		if locale.Direction != expectedDirection {
			fmt.Printf(
				"ERROR: %s: meta.direction=%q, expected %q\n",
				code+".json",
				locale.Direction,
				expectedDirection,
			)
			errorsFound++
		}

		expectedFont := "Inter"
		if font, ok := expectedFonts[code]; ok {
			expectedFont = font
		}

		if locale.Font != expectedFont {
			fmt.Printf(
				"ERROR: %s: meta.font=%q, expected %q\n",
				code+".json",
				locale.Font,
				expectedFont,
			)
			errorsFound++
		}

		if len(keys) < minKeyCount {
			fmt.Printf(
				"ERROR: %s: only %d keys found, minimum is %d\n",
				code+".json",
				len(keys),
				minKeyCount,
			)
			errorsFound++
		}

		for key, value := range keys {
			text, ok := value.(string)
			if !ok {
				fmt.Printf(
					"ERROR: %s: key %q must contain a string value\n",
					code+".json",
					key,
				)
				errorsFound++
				continue
			}

			locale.Placeholders[key] = extractPlaceholders(text)
		}
	}

	reference, ok := locales["en"]
	if !ok {
		fmt.Println("ERROR: English locale cannot be loaded; key comparison is impossible")
		errorsFound++
	} else {
		referenceKeys := sortedKeys(reference.Keys)

		for _, code := range expectedLocales {
			locale, ok := locales[code]
			if !ok {
				continue
			}

			currentKeys := sortedKeys(locale.Keys)

			missing, extra := compareKeys(referenceKeys, currentKeys)

			for _, key := range missing {
				fmt.Printf(
					"ERROR: %s.json: missing key %q\n",
					code,
					key,
				)
				errorsFound++
			}

			for _, key := range extra {
				fmt.Printf(
					"ERROR: %s.json: unexpected key %q\n",
					code,
					key,
				)
				errorsFound++
			}

			for _, key := range referenceKeys {
				expectedPlaceholders := reference.Placeholders[key]
				actualPlaceholders := locale.Placeholders[key]

				missingPlaceholders := comparePlaceholderSets(
					expectedPlaceholders,
					actualPlaceholders,
				)

				extraPlaceholders := comparePlaceholderSets(
					actualPlaceholders,
					expectedPlaceholders,
				)

				for _, placeholder := range missingPlaceholders {
					fmt.Printf(
						"ERROR: %s.json: key %q is missing placeholder %s\n",
						code,
						key,
						placeholder,
					)
					errorsFound++
				}

				for _, placeholder := range extraPlaceholders {
					fmt.Printf(
						"ERROR: %s.json: key %q contains unexpected placeholder %s\n",
						code,
						key,
						placeholder,
					)
					errorsFound++
				}
			}
		}
	}

	fmt.Println()

	if errorsFound > 0 {
		fmt.Printf("VALIDATION FAILED: %d error(s)\n", errorsFound)
		os.Exit(1)
	}

	fmt.Printf(
		"VALIDATION OK: %d locales, minimum %d keys per locale, identical keys and placeholders\n",
		len(expectedLocales),
		minKeyCount,
	)
}

func loadLocaleFile(path string) (map[string]interface{}, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var data map[string]interface{}

	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}

	return data, nil
}

func getObject(data map[string]interface{}, key string) (map[string]interface{}, bool) {
	value, ok := data[key]
	if !ok {
		return nil, false
	}

	object, ok := value.(map[string]interface{})
	return object, ok
}

func getString(data map[string]interface{}, key string) string {
	value, ok := data[key]
	if !ok {
		return ""
	}

	text, ok := value.(string)
	if !ok {
		return ""
	}

	return text
}

func sortedKeys(data map[string]interface{}) []string {
	keys := make([]string, 0, len(data))

	for key := range data {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	return keys
}

func compareKeys(reference, current []string) (missing, extra []string) {
	referenceSet := make(map[string]struct{}, len(reference))
	currentSet := make(map[string]struct{}, len(current))

	for _, key := range reference {
		referenceSet[key] = struct{}{}
	}

	for _, key := range current {
		currentSet[key] = struct{}{}
	}

	for _, key := range reference {
		if _, ok := currentSet[key]; !ok {
			missing = append(missing, key)
		}
	}

	for _, key := range current {
		if _, ok := referenceSet[key]; !ok {
			extra = append(extra, key)
		}
	}

	sort.Strings(missing)
	sort.Strings(extra)

	return missing, extra
}

func extractPlaceholders(text string) map[string]struct{} {
	result := make(map[string]struct{})

	for _, placeholder := range []string{
		"{amount}",
		"{multiplier}",
		"{seconds}",
	} {
		if strings.Contains(text, placeholder) {
			result[placeholder] = struct{}{}
		}
	}

	return result
}

func comparePlaceholderSets(expected, actual map[string]struct{}) []string {
	var missing []string

	for placeholder := range expected {
		if _, ok := actual[placeholder]; !ok {
			missing = append(missing, placeholder)
		}
	}

	sort.Strings(missing)

	return missing
}
