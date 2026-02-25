package runners

import (
	"regexp"
	"strings"
	"unicode"
)

var githubExprPattern = regexp.MustCompile(`\$\{\{\s*([^}]+?)\s*\}\}`)

// resolveGitHubExpressions resolves common GitHub Actions expressions in shell commands.
// Unknown expressions are removed to avoid raw `${{ ... }}` reaching the shell.
func resolveGitHubExpressions(command string) string {
	return githubExprPattern.ReplaceAllStringFunc(command, func(match string) string {
		parts := githubExprPattern.FindStringSubmatch(match)
		if len(parts) != 2 {
			return match
		}

		expr := strings.TrimSpace(parts[1])

		switch {
		case strings.HasPrefix(expr, "matrix."):
			key := strings.TrimPrefix(expr, "matrix.")
			return "${MATRIX_" + normalizeExpressionKey(key) + "}"
		case strings.HasPrefix(expr, "env."):
			key := strings.TrimPrefix(expr, "env.")
			return "${" + strings.TrimSpace(key) + "}"
		case expr == "runner.os":
			return "Linux"
		case expr == "runner.arch":
			return "X64"
		case expr == "github.workspace":
			return "${GITHUB_WORKSPACE:-/workspace}"
		case expr == "github.repository":
			return "${GITHUB_REPOSITORY:-local/repo}"
		case expr == "github.ref":
			return "${GITHUB_REF:-refs/heads/main}"
		case expr == "github.sha":
			return "${GITHUB_SHA:-local}"
		default:
			return ""
		}
	})
}

// normalizeExpressionKey converts keys like "go-version" to "GO_VERSION".
func normalizeExpressionKey(key string) string {
	var b strings.Builder
	for _, r := range key {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToUpper(r))
			continue
		}
		b.WriteRune('_')
	}

	out := b.String()
	out = strings.Trim(out, "_")
	for strings.Contains(out, "__") {
		out = strings.ReplaceAll(out, "__", "_")
	}
	if out == "" {
		return "VALUE"
	}
	return out
}
