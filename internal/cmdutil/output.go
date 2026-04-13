package cmdutil

import (
	"fmt"
	"os"
)

// Minimal colored output. Auto-disables when stdout is not a TTY or when
// NO_COLOR is set (https://no-color.org/).
var useColor = shouldColor()

func shouldColor() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

func SetColor(enabled bool) { useColor = enabled }

func color(code, msg string) string {
	if !useColor {
		return msg
	}
	return "\x1b[" + code + "m" + msg + "\x1b[0m"
}

func Success(format string, a ...any) {
	fmt.Fprintln(os.Stdout, color("32", "✓ ")+fmt.Sprintf(format, a...))
}

func Fail(format string, a ...any) {
	fmt.Fprintln(os.Stderr, color("31", "✗ ")+fmt.Sprintf(format, a...))
}

func Warn(format string, a ...any) {
	fmt.Fprintln(os.Stderr, color("33", "! ")+fmt.Sprintf(format, a...))
}

func Info(format string, a ...any) {
	fmt.Fprintln(os.Stderr, color("36", "» ")+fmt.Sprintf(format, a...))
}

func ExitIfError(err error) {
	if err == nil {
		return
	}
	Fail("%v", err)
	os.Exit(1)
}
