package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// LoadDotEnv loads environment variables from a .env file without overriding existing OS environment variables.
// Lookup order: explicitPath -> ALERT_WEBHOOK_ENV_FILE -> ./.env -> <executable_dir>/.env.
// Returns the loaded file path (or "" if no .env file exists).
func LoadDotEnv(explicitPath string) (string, error) {
	candidates := make([]string, 0, 3)

	if p := strings.TrimSpace(explicitPath); p != "" {
		candidates = append(candidates, p)
	} else if p := strings.TrimSpace(os.Getenv("ALERT_WEBHOOK_ENV_FILE")); p != "" {
		candidates = append(candidates, p)
	} else {
		candidates = append(candidates, ".env")
		if exePath, err := os.Executable(); err == nil {
			exeEnv := filepath.Join(filepath.Dir(exePath), ".env")
			absCwd, err1 := filepath.Abs(".env")
			absExe, err2 := filepath.Abs(exeEnv)
			if err1 != nil || err2 != nil || absCwd != absExe {
				candidates = append(candidates, exeEnv)
			}
		}
	}

	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", fmt.Errorf("检查环境变量文件 %s 失败: %w", candidate, err)
		}
		if info.IsDir() {
			continue
		}
		if err := LoadDotEnvFromFile(candidate); err != nil {
			return "", err
		}
		return candidate, nil
	}

	return "", nil
}

// LoadDotEnvFromFile parses a .env file and sets variables into the process environment
// only if they are not already defined (existing OS env vars always win).
func LoadDotEnvFromFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("读取环境变量文件 %s 失败: %w", path, err)
	}

	content := strings.TrimPrefix(string(data), "\ufeff")
	lines := strings.Split(content, "\n")

	for i, rawLine := range lines {
		line := strings.TrimSpace(strings.TrimSuffix(rawLine, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if strings.HasPrefix(line, "export") && len(line) > len("export") {
			rest := line[len("export"):]
			if rest[0] == ' ' || rest[0] == '\t' {
				line = strings.TrimSpace(rest)
			}
		}

		eqIdx := strings.IndexByte(line, '=')
		if eqIdx <= 0 {
			continue
		}

		key := strings.TrimSpace(line[:eqIdx])
		if !isValidEnvKey(key) {
			return fmt.Errorf("环境变量文件 %s 第 %d 行变量名无效: %q", path, i+1, key)
		}

		val := parseDotEnvValue(line[eqIdx+1:])
		if _, exists := os.LookupEnv(key); !exists {
			if err := os.Setenv(key, val); err != nil {
				return fmt.Errorf("设置环境变量 %s 失败: %w", key, err)
			}
		}
	}

	return nil
}

func isValidEnvKey(key string) bool {
	if key == "" {
		return false
	}
	for i, r := range key {
		if i == 0 {
			if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_') {
				return false
			}
			continue
		}
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_') {
			return false
		}
	}
	return true
}

func parseDotEnvValue(raw string) string {
	trimmed := strings.TrimLeftFunc(raw, unicode.IsSpace)
	if trimmed == "" {
		return ""
	}

	switch trimmed[0] {
	case '\'':
		if end := strings.IndexByte(trimmed[1:], '\''); end >= 0 {
			return trimmed[1 : 1+end]
		}
		return strings.TrimRightFunc(trimmed[1:], unicode.IsSpace)
	case '"':
		var sb strings.Builder
		escaped := false
		for i := 1; i < len(trimmed); i++ {
			ch := trimmed[i]
			if escaped {
				switch ch {
				case 'n':
					sb.WriteByte('\n')
				case 'r':
					sb.WriteByte('\r')
				case 't':
					sb.WriteByte('\t')
				case '"', '\\':
					sb.WriteByte(ch)
				default:
					sb.WriteByte('\\')
					sb.WriteByte(ch)
				}
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == '"' {
				return sb.String()
			}
			sb.WriteByte(ch)
		}
		if escaped {
			sb.WriteByte('\\')
		}
		return sb.String()
	default:
		// Strip inline '#' comments only when preceded by whitespace.
		for i := 0; i < len(trimmed); i++ {
			if trimmed[i] == '#' && i > 0 && unicode.IsSpace(rune(trimmed[i-1])) {
				return strings.TrimSpace(trimmed[:i])
			}
		}
		return strings.TrimSpace(trimmed)
	}
}
