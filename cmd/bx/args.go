package main

import (
	"fmt"
	"os"
	"strings"
)

// unknownFlag is the CLI's one answer to a flag a command does not know:
// an error, so a typo never silently becomes a positional argument or a
// no-op. Four commands used to ignore unknown flags; they warn on stderr
// for one release (docs/bx.md "Unknown flags") before erroring like the
// rest — pass lenient=true from those and only those.
func unknownFlag(cmd, flag string, lenient bool) error {
	if !lenient {
		return fmt.Errorf("unknown flag %s", flag)
	}
	fmt.Fprintf(os.Stderr, "bx %s: warning: unknown flag %s ignored — it becomes an error in the next release\n", cmd, flag)
	return nil
}

// isFlag reports whether an argument is a flag (-x or --x), not a value.
func isFlag(a string) bool { return strings.HasPrefix(a, "-") && a != "-" }

// nextArg returns the value that follows the flag at args[*i] and advances
// *i past it. A flag at the end of the line is an error the caller prints —
// never an index panic (`bx org set x --name` once crashed on exactly that).
func nextArg(args []string, i *int) (string, error) {
	if *i+1 >= len(args) {
		return "", fmt.Errorf("%s needs a value", args[*i])
	}
	*i++
	return args[*i], nil
}
