package semaphore

import (
	"encoding/json"
	"strings"
)

// ParsedArgs is the part of a template's "arguments" that ACP can express.
type ParsedArgs struct {
	Limit, Tags, SkipTags string
	Verbosity             int
	Check, Diff           bool
	// Unmapped lists flag names (never values: they may carry secrets).
	Unmapped []string
}

// ParseArguments parses Semaphore's arguments field, a JSON array of strings
// (older versions: a plain command-line string).
func ParseArguments(raw string) ParsedArgs {
	var out ParsedArgs
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" || raw == "[]" {
		return out
	}
	var args []string
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		args = strings.Fields(raw)
	}
	// Flatten entries that hold several words ("--limit foo").
	var flat []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") && strings.Contains(a, " ") {
			flat = append(flat, strings.Fields(a)...)
		} else {
			flat = append(flat, a)
		}
	}
	for i := 0; i < len(flat); i++ {
		a := flat[i]
		name, val, hasVal := strings.Cut(a, "=")
		next := func() string {
			if hasVal {
				return val
			}
			if i+1 < len(flat) {
				i++
				return flat[i]
			}
			return ""
		}
		switch {
		case name == "--limit" || name == "-l":
			out.Limit = next()
		case name == "--tags" || name == "-t":
			out.Tags = next()
		case name == "--skip-tags":
			out.SkipTags = next()
		case name == "--check" || name == "-C":
			out.Check = true
		case name == "--diff" || name == "-D":
			out.Diff = true
		case isVerbose(a):
			if n := len(a) - 1; n > out.Verbosity {
				out.Verbosity = min(n, 4)
			}
		case strings.HasPrefix(a, "-"):
			out.Unmapped = append(out.Unmapped, name)
			if !hasVal && i+1 < len(flat) && !strings.HasPrefix(flat[i+1], "-") && takesValue(name) {
				i++
			}
		default:
			out.Unmapped = append(out.Unmapped, "(positional argument)")
		}
	}
	return out
}

func isVerbose(a string) bool {
	if len(a) < 2 || a[0] != '-' {
		return false
	}
	return strings.Trim(a[1:], "v") == ""
}

func takesValue(flag string) bool {
	switch flag {
	case "-e", "--extra-vars", "-i", "--inventory", "-u", "--user", "--private-key", "--vault-id",
		"--vault-password-file", "--vault-pass-file", "-T", "--timeout", "-f", "--forks", "--start-at-task", "-c", "--connection", "-M", "--module-path":
		return true
	}
	return false
}
