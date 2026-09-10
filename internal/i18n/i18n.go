package i18n

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	defaultLanguage = "en"
	localesDir      = "internal/i18n/locales"
)

var supportedLanguages = []string{
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

type Locale struct {
	Meta map[string]interface{}
	Keys map[string]interface{}
}

type Manager struct {
	mu sync.RWMutex

	localesDir string
	fallback   string
	locales    map[string]*Locale
}

func New() *Manager {
	manager := &Manager{
		localesDir: localesDir,
		fallback:   defaultLanguage,
		locales:    make(map[string]*Locale, len(supportedLanguages)),
	}

	for _, code := range supportedLanguages {
		if err := manager.LoadLocale(code); err != nil {
			panic(fmt.Sprintf("i18n: failed to load locale %q: %v", code, err))
		}
	}

	return manager
}

func (m *Manager) LoadLocale(code string) error {
	code = normalizeLanguage(code)

	if code == "" {
		return fmt.Errorf("invalid locale code")
	}

	if !isSupportedLanguage(code) {
		return fmt.Errorf("unsupported locale: %s", code)
	}

	m.mu.RLock()
	_, loaded := m.locales[code]
	m.mu.RUnlock()

	if loaded {
		return nil
	}

	path := filepath.Join(m.localesDir, code+".json")

	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("load locale %q: %w", code, err)
	}

	var document map[string]interface{}

	if err := json.Unmarshal(raw, &document); err != nil {
		return fmt.Errorf("parse locale %q: %w", code, err)
	}

	meta, ok := document["meta"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("locale %q: invalid meta object", code)
	}

	keys, ok := document["keys"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("locale %q: invalid keys object", code)
	}

	localeCode, ok := meta["code"].(string)
	if !ok || localeCode != code {
		return fmt.Errorf(
			"locale %q: meta.code is invalid",
			code,
		)
	}

	locale := &Locale{
		Meta: meta,
		Keys: keys,
	}

	m.mu.Lock()
	m.locales[code] = locale
	m.mu.Unlock()

	return nil
}

func (m *Manager) T(code, key string, params map[string]string) string {
	code = normalizeLanguage(code)

	if code == "" {
		code = m.fallback
	}

	value, err := m.translate(code, key)

	if err == nil && value != "" {
		return replacePlaceholders(value, params)
	}

	if code != m.fallback {
		value, err = m.translate(m.fallback, key)

		if err == nil && value != "" {
			return replacePlaceholders(value, params)
		}
	}

	return replacePlaceholders(key, params)
}

func (m *Manager) translate(code, key string) (string, error) {
	if err := m.LoadLocale(code); err != nil {
		return "", err
	}

	m.mu.RLock()
	locale := m.locales[code]
	m.mu.RUnlock()

	if locale == nil {
		return "", fmt.Errorf("locale %q is not loaded", code)
	}

	value, ok := locale.Keys[key]
	if !ok {
		return "", fmt.Errorf(
			"translation key %q not found in locale %q",
			key,
			code,
		)
	}

	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf(
			"translation key %q in locale %q is not a string",
			key,
			code,
		)
	}

	return text, nil
}

func replacePlaceholders(text string, params map[string]string) string {
	for name, value := range params {
		placeholder := name

		if !strings.HasPrefix(placeholder, "{") {
			placeholder = "{" + placeholder
		}

		if !strings.HasSuffix(placeholder, "}") {
			placeholder += "}"
		}

		text = strings.ReplaceAll(text, placeholder, value)
	}

	return text
}

func normalizeLanguage(code string) string {
	code = strings.TrimSpace(strings.ToLower(code))

	if index := strings.IndexByte(code, '-'); index >= 0 {
		code = code[:index]
	}

	if index := strings.IndexByte(code, '_'); index >= 0 {
		code = code[:index]
	}

	return code
}

func isSupportedLanguage(code string) bool {
	for _, supported := range supportedLanguages {
		if supported == code {
			return true
		}
	}

	return false
}
