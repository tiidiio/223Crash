package adminstore

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type envAssignment struct {
	key   string
	value string
}

func LoadEnvFile(path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("configuration path is required")
	}

	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("cannot open configuration file: %w", err)
	}
	defer file.Close()

	var assignments []envAssignment

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1024*1024)

	lineNumber := 0

	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())

		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if strings.HasPrefix(line, "export") {
			rest := strings.TrimPrefix(line, "export")
			if rest == "" || (rest[0] != ' ' && rest[0] != '\t') {
				return fmt.Errorf("invalid export declaration at line %d", lineNumber)
			}
			line = strings.TrimSpace(rest)
			if line == "" {
				return fmt.Errorf("empty export declaration at line %d", lineNumber)
			}
		}

		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return fmt.Errorf("invalid configuration syntax at line %d", lineNumber)
		}

		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)

		if key == "" {
			return fmt.Errorf("empty configuration key at line %d", lineNumber)
		}

		for i, c := range key {
			valid := c == '_' ||
				(c >= 'A' && c <= 'Z') ||
				(c >= 'a' && c <= 'z') ||
				(i > 0 && c >= '0' && c <= '9')

			if !valid {
				return fmt.Errorf("invalid configuration key at line %d", lineNumber)
			}
		}

		if value != "" {
			switch value[0] {
			case '"':
				end := -1
				escaped := false

				for i := 1; i < len(value); i++ {
					if escaped {
						escaped = false
						continue
					}
					if value[i] == '\\' {
						escaped = true
						continue
					}
					if value[i] == '"' {
						end = i
						break
					}
				}

				if end < 0 {
					return fmt.Errorf("unmatched quote at line %d", lineNumber)
				}

				decoded, err := strconv.Unquote(value[:end+1])
				if err != nil {
					return fmt.Errorf("invalid quoted value at line %d", lineNumber)
				}

				trailing := strings.TrimSpace(value[end+1:])
				if trailing != "" && !strings.HasPrefix(trailing, "#") {
					return fmt.Errorf("unexpected content after quoted value at line %d", lineNumber)
				}

				value = decoded

			case '\'':
				end := strings.IndexByte(value[1:], '\'')
				if end < 0 {
					return fmt.Errorf("unmatched quote at line %d", lineNumber)
				}

				end++
				trailing := strings.TrimSpace(value[end+1:])
				if trailing != "" && !strings.HasPrefix(trailing, "#") {
					return fmt.Errorf("unexpected content after quoted value at line %d", lineNumber)
				}

				value = value[1:end]

			default:
				if strings.ContainsAny(value, "\"'") {
					return fmt.Errorf("invalid quote at line %d", lineNumber)
				}

				if comment := strings.Index(value, " #"); comment >= 0 {
					value = strings.TrimSpace(value[:comment])
				}
			}
		}

		if strings.ContainsAny(value, "\x00\r\n") {
			return fmt.Errorf("invalid configuration value at line %d", lineNumber)
		}

		assignments = append(assignments, envAssignment{
			key:   key,
			value: value,
		})
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("cannot read configuration file: %w", err)
	}

	// Parse complete before changing the process environment.
	// Existing environment variables always have priority.
	pending := make(map[string]string, len(assignments))

	for _, assignment := range assignments {
		if _, exists := os.LookupEnv(assignment.key); exists {
			continue
		}
		pending[assignment.key] = assignment.value
	}

	// Apply atomically with rollback if Setenv fails.
	original := make(map[string]*string, len(pending))

	for key := range pending {
		if previous, exists := os.LookupEnv(key); exists {
			value := previous
			original[key] = &value
		} else {
			original[key] = nil
		}
	}

	applied := make([]string, 0, len(pending))

	for key, value := range pending {
		if err := os.Setenv(key, value); err != nil {
			for _, appliedKey := range applied {
				if previous := original[appliedKey]; previous != nil {
					_ = os.Setenv(appliedKey, *previous)
				} else {
					_ = os.Unsetenv(appliedKey)
				}
			}

			return fmt.Errorf("cannot apply configuration variable %q", key)
		}

		applied = append(applied, key)
	}

	return nil
}
