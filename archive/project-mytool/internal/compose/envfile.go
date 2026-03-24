package compose

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// LoadEnvFile reads a .env file and returns the key=value pairs as a map.
// Supports:
//   - KEY=VALUE (unquoted)
//   - KEY="VALUE" (double-quoted, supports \n, \t, \\, \" escapes)
//   - KEY='VALUE' (single-quoted, literal — no escape processing)
//   - KEY= (empty value)
//   - Lines starting with # are comments
//   - Blank lines are skipped
//   - Leading/trailing whitespace on keys and unquoted values is trimmed
//   - export KEY=VALUE (optional "export" prefix is stripped)
func LoadEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open env file %s: %w", path, err)
	}
	defer f.Close()

	env := make(map[string]string)
	scanner := bufio.NewScanner(f)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()

		// Strip inline comments only for unquoted values
		// (handled per-value below, not here)

		// Trim leading/trailing whitespace
		line = strings.TrimSpace(line)

		// Skip blank lines and comments
		if line == "" || line[0] == '#' {
			continue
		}

		// Strip optional "export " prefix
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(line[7:])
		}

		// Find the first = separator
		eqIdx := strings.IndexByte(line, '=')
		if eqIdx < 0 {
			// Bare key without = — treat as KEY with empty value
			key := strings.TrimSpace(line)
			if key != "" {
				env[key] = ""
			}
			continue
		}

		key := strings.TrimSpace(line[:eqIdx])
		if key == "" {
			continue // skip lines with empty key
		}

		rawValue := line[eqIdx+1:]

		value, err := parseEnvValue(rawValue)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, lineNum, err)
		}

		env[key] = value
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to read env file %s: %w", path, err)
	}

	return env, nil
}

// parseEnvValue parses the value portion of a KEY=VALUE line.
func parseEnvValue(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}

	// Double-quoted value
	if raw[0] == '"' {
		return parseDoubleQuoted(raw)
	}

	// Single-quoted value
	if raw[0] == '\'' {
		return parseSingleQuoted(raw)
	}

	// Unquoted value — strip inline comments
	if idx := strings.IndexByte(raw, '#'); idx > 0 {
		// Only treat as comment if preceded by whitespace
		if raw[idx-1] == ' ' || raw[idx-1] == '\t' {
			raw = raw[:idx]
		}
	}

	return strings.TrimSpace(raw), nil
}

// parseDoubleQuoted extracts a double-quoted string value with escape support.
func parseDoubleQuoted(raw string) (string, error) {
	if len(raw) < 2 || raw[0] != '"' {
		return "", fmt.Errorf("invalid double-quoted value")
	}

	var b strings.Builder
	i := 1 // skip opening quote
	for i < len(raw) {
		ch := raw[i]
		if ch == '"' {
			// Closing quote found
			return b.String(), nil
		}
		if ch == '\\' && i+1 < len(raw) {
			next := raw[i+1]
			switch next {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case '\\':
				b.WriteByte('\\')
			case '"':
				b.WriteByte('"')
			default:
				// Keep the backslash for unknown escapes
				b.WriteByte('\\')
				b.WriteByte(next)
			}
			i += 2
			continue
		}
		b.WriteByte(ch)
		i++
	}

	return "", fmt.Errorf("unterminated double-quoted value")
}

// parseSingleQuoted extracts a single-quoted string value (literal, no escapes).
func parseSingleQuoted(raw string) (string, error) {
	if len(raw) < 2 || raw[0] != '\'' {
		return "", fmt.Errorf("invalid single-quoted value")
	}

	closeIdx := strings.IndexByte(raw[1:], '\'')
	if closeIdx < 0 {
		return "", fmt.Errorf("unterminated single-quoted value")
	}

	return raw[1 : 1+closeIdx], nil
}

// ResolveEnvFiles loads environment variables from env_file paths for a sandbox,
// merging them with inline env vars. Inline env vars take precedence over file vars.
// Paths are resolved relative to the compose file directory.
func ResolveEnvFiles(sandbox *SandboxConfig, composeDir string) (map[string]string, error) {
	if len(sandbox.EnvFile) == 0 {
		return sandbox.Env, nil
	}

	merged := make(map[string]string)

	// Load env files in order (later files override earlier ones)
	for _, envPath := range sandbox.EnvFile {
		// Resolve relative paths against the compose file directory
		if !filepath.IsAbs(envPath) {
			envPath = filepath.Join(composeDir, envPath)
		}

		fileEnv, err := LoadEnvFile(envPath)
		if err != nil {
			return nil, fmt.Errorf("env_file %q: %w", envPath, err)
		}

		for k, v := range fileEnv {
			merged[k] = v
		}
	}

	// Inline env vars override file vars
	for k, v := range sandbox.Env {
		merged[k] = v
	}

	return merged, nil
}
