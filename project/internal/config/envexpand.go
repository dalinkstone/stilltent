package config

import (
	"os"
	"strings"
)

// ExpandEnv expands environment variable references in a string.
// Supports ${VAR}, ${VAR:-default}, and $VAR syntax.
// This is used to expand env vars in YAML config files before parsing,
// matching the behavior described in the sandbox configuration format spec
// (e.g., ANTHROPIC_API_KEY: ${ANTHROPIC_API_KEY}).
func ExpandEnv(s string) string {
	var buf strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == '$' && i+1 < len(s) {
			if s[i+1] == '{' {
				// ${VAR} or ${VAR:-default}
				end := strings.Index(s[i:], "}")
				if end == -1 {
					buf.WriteByte(s[i])
					i++
					continue
				}
				expr := s[i+2 : i+end]
				// Check for :- default syntax
				if idx := strings.Index(expr, ":-"); idx >= 0 {
					varName := expr[:idx]
					defaultVal := expr[idx+2:]
					if val, ok := os.LookupEnv(varName); ok {
						buf.WriteString(val)
					} else {
						buf.WriteString(defaultVal)
					}
				} else {
					buf.WriteString(os.Getenv(expr))
				}
				i += end + 1
			} else if isVarNameStart(s[i+1]) {
				// $VAR
				j := i + 1
				for j < len(s) && isVarNameChar(s[j]) {
					j++
				}
				varName := s[i+1 : j]
				buf.WriteString(os.Getenv(varName))
				i = j
			} else {
				buf.WriteByte(s[i])
				i++
			}
		} else {
			buf.WriteByte(s[i])
			i++
		}
	}
	return buf.String()
}

// ExpandEnvBytes expands environment variable references in raw YAML bytes.
// Each line is processed independently so that env vars in values are expanded
// before YAML parsing.
func ExpandEnvBytes(data []byte) []byte {
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		lines[i] = ExpandEnv(line)
	}
	return []byte(strings.Join(lines, "\n"))
}

func isVarNameStart(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || c == '_'
}

func isVarNameChar(c byte) bool {
	return isVarNameStart(c) || (c >= '0' && c <= '9')
}
