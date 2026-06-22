package handlers

import (
	"fmt"
	"os"
	"sort"
	"strings"

	cli "github.com/urfave/cli/v2"
)

// CmdEnvList handles the env list command
func CmdEnvList(c *cli.Context) error {
	// Get all environment variables
	envVars := os.Environ()

	// Filter git-ci related variables if not verbose
	verbose := c.Bool("verbose")

	var filtered []string
	for _, env := range envVars {
		if verbose || strings.HasPrefix(env, "GIT_CI_") || strings.HasPrefix(env, "CI") {
			filtered = append(filtered, env)
		}
	}

	// Sort for consistent output
	sort.Strings(filtered)

	if len(filtered) == 0 {
		fmt.Println("No git-ci environment variables set")
		return nil
	}

	fmt.Println("Environment variables:")
	fmt.Println(strings.Repeat("-", 60))

	for _, env := range filtered {
		parts := strings.SplitN(env, "=", 2)
		if len(parts) == 2 {
			fmt.Printf("%-30s = %s\n", parts[0], parts[1])
		}
	}

	return nil
}

// detectSaveFlag returns true when the user typed --save, either as a
// registered flag (c.Bool("save")) or as a raw positional argument
// (urfave/cli quirk where --save was not consumed as a flag in some
// invocations). Used in both the empty-keyValArgs guard and the
// override-detection block so the user always sees a self-explaining
// error when --save is set without a KEY=VALUE positional.
func detectSaveFlag(c *cli.Context, rawArgs []string) bool {
	if c.Bool("save") {
		return true
	}
	for _, arg := range rawArgs {
		if arg == "--save" {
			return true
		}
	}
	return false
}

// CmdEnvSet handles the env set command
func CmdEnvSet(c *cli.Context) error {
	// Filter args to only those that look like KEY=VALUE so flags written
	// after positional args (e.g. `env set KEY=v --save --file foo`) don't
	// leak through and cause an "invalid format" error.
	rawArgs := c.Args().Slice()
	keyValArgs := make([]string, 0, len(rawArgs))
	for _, arg := range rawArgs {
		if strings.Contains(arg, "=") {
			keyValArgs = append(keyValArgs, arg)
		}
	}

	if len(keyValArgs) == 0 {
		// Bug #6 (v0.4.1): when --save is set, the generic "no
		// environment variables specified" message leaves the user
		// guessing why the invocation failed. Extend the message to
		// tie the failure to --save by name so it's self-explaining.
		// The "no environment variables specified" substring is
		// preserved so the existing TestCmdEnvSet_NoArgsStillErrors
		// keeps passing.
		if detectSaveFlag(c, rawArgs) {
			return fmt.Errorf("no environment variables specified; --save requires at least one KEY=VALUE argument. Usage: git-ci env set KEY=VALUE [KEY=VALUE...]")
		}
		return fmt.Errorf("no environment variables specified. Usage: git-ci env set KEY=VALUE [KEY=VALUE...]")
	}

	// Parse and set environment variables
	for _, arg := range keyValArgs {
		parts := strings.SplitN(arg, "=", 2)
		if len(parts) != 2 || parts[0] == "" {
			return fmt.Errorf("invalid format: %s. Expected KEY=VALUE", arg)
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		if key == "" {
			return fmt.Errorf("empty key in: %s", arg)
		}

		// Set environment variable
		if err := os.Setenv(key, value); err != nil {
			return fmt.Errorf("failed to set %s: %w", key, err)
		}

		fmt.Printf("✓ Set %s=%s\n", key, value)
	}

	// Determine save intent (c.Bool + urfave/cli quirk fallback via
	// detectSaveFlag) and envFile (c.String + rawArgs quirk
	// fallback for the --file <path> positional form).
	//
	// urfave/cli v2.27.7 (pinned in go.mod) stops parsing flags after
	// the first positional, so --file / --save that appear AFTER a
	// positional (e.g. `env set KEY=v --save --file out`) leak into
	// c.Args(). In that case c.String("file") returns the registered
	// default (".env") rather than "", so we must ALWAYS scan
	// rawArgs for `--file` / `--file=`. Scanning unconditionally is
	// safe: when urfave/cli consumed the flag normally, rawArgs
	// won't contain it and the loop is a no-op. Revisit this when
	// bumping urfave/cli.
	save := detectSaveFlag(c, rawArgs)
	envFile := c.String("file")
	for i, arg := range rawArgs {
		if strings.HasPrefix(arg, "--file=") {
			envFile = strings.TrimPrefix(arg, "--file=")
		} else if arg == "--file" && i+1 < len(rawArgs) {
			envFile = rawArgs[i+1]
		}
	}

	// Optionally save to .env file
	if save {
		if envFile == "" {
			envFile = ".env"
		}

		if err := saveEnvFile(keyValArgs, envFile); err != nil {
			return fmt.Errorf("failed to save to %s: %w", envFile, err)
		}

		fmt.Printf("\n✓ Saved to %s\n", envFile)
	}

	return nil
}

// CmdEnvLoad handles the env load command
func CmdEnvLoad(c *cli.Context) error {
	envFile := c.String("file")
	if envFile == "" {
		envFile = ".env"
	}

	// Check if file exists
	if _, err := os.Stat(envFile); os.IsNotExist(err) {
		return fmt.Errorf("environment file not found: %s", envFile)
	}

	// Load environment variables
	env, err := loadEnvFile(envFile)
	if err != nil {
		return fmt.Errorf("failed to load %s: %w", envFile, err)
	}

	if len(env) == 0 {
		fmt.Printf("No environment variables found in %s\n", envFile)
		return nil
	}

	fmt.Printf("Loading environment from %s:\n", envFile)
	fmt.Println(strings.Repeat("-", 60))

	// Set environment variables
	for key, value := range env {
		if err := os.Setenv(key, value); err != nil {
			return fmt.Errorf("failed to set %s: %w", key, err)
		}

		// Mask sensitive values in output
		displayValue := value
		if isSensitive(key) && len(value) > 4 {
			displayValue = value[:2] + strings.Repeat("*", len(value)-4) + value[len(value)-2:]
		}

		fmt.Printf("%-30s = %s\n", key, displayValue)
	}

	fmt.Printf("\n✓ Loaded %d environment variable(s)\n", len(env))

	return nil
}

// saveEnvFile saves environment variables to a file
func saveEnvFile(vars []string, filename string) error {
	// Read existing file if it exists
	existing := make(map[string]string)
	if data, err := os.ReadFile(filename); err == nil {
		lines := strings.Split(string(data), "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}

			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				existing[parts[0]] = parts[1]
			}
		}
	}

	// Update with new variables
	for _, v := range vars {
		parts := strings.SplitN(v, "=", 2)
		if len(parts) == 2 {
			existing[parts[0]] = parts[1]
		}
	}

	// Build file content
	var content strings.Builder
	content.WriteString("# git-ci environment variables\n")
	content.WriteString("# Generated by git-ci\n\n")

	// Sort keys for consistent output
	keys := make([]string, 0, len(existing))
	for k := range existing {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// Group variables by prefix
	grouped := make(map[string][]string)
	for _, key := range keys {
		prefix := "OTHER"
		if strings.HasPrefix(key, "GIT_CI_") {
			prefix = "GIT_CI"
		} else if strings.Contains(key, "_") {
			parts := strings.Split(key, "_")
			if len(parts) > 0 {
				prefix = parts[0]
			}
		}

		grouped[prefix] = append(grouped[prefix], key)
	}

	// Write grouped variables
	prefixes := make([]string, 0, len(grouped))
	for p := range grouped {
		prefixes = append(prefixes, p)
	}
	sort.Strings(prefixes)

	for _, prefix := range prefixes {
		if prefix != "OTHER" {
			content.WriteString(fmt.Sprintf("\n# %s variables\n", prefix))
		} else if len(grouped[prefix]) > 0 {
			content.WriteString("\n# Other variables\n")
		}

		for _, key := range grouped[prefix] {
			value := existing[key]

			// Quote value if it contains spaces or special characters
			if needsQuoting(value) {
				value = fmt.Sprintf(`"%s"`, value)
			}

			content.WriteString(fmt.Sprintf("%s=%s\n", key, value))
		}
	}

	// Write to file
	return os.WriteFile(filename, []byte(content.String()), 0644)
}

// isSensitive checks if an environment variable key is sensitive
func isSensitive(key string) bool {
	// TODO:
	// not optimizal for now... will find a better way later on
	sensitive := []string{
		"PASSWORD", "SECRET", "TOKEN", "KEY", "CREDENTIAL",
		"PRIVATE", "AUTH", "API_KEY", "ACCESS", "CERT",
	}

	upperKey := strings.ToUpper(key)
	for _, s := range sensitive {
		if strings.Contains(upperKey, s) {
			return true
		}
	}

	return false
}

// needsQuoting checks if a value needs quoting in env file
func needsQuoting(value string) bool {
	// Check for spaces or special characters
	specialChars := []string{" ", "\t", "\n", "#", "$", "&", "|", ";", "(", ")", "[", "]", "{", "}", "<", ">", "!", "?", "*", "~", "`"}

	for _, char := range specialChars {
		if strings.Contains(value, char) {
			return true
		}
	}

	return false
}
