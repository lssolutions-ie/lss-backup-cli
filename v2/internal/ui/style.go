package ui

import "fmt"

// ANSI color variables — set to empty strings when color output is unavailable.
// Populated by the platform-specific init (vt_unix.go / vt_windows.go).
var (
	colYellow string // bold yellow  — headers, warnings
	colGreen  string // normal green — success, OK
	colRed    string // normal red   — errors, missing
	colCyan   string // normal cyan  — dividers, accents
	colBold   string // bold only    — menu numbers, labels
	colReset  string // reset all
)

// setColors is called by the platform-specific init when ANSI output is confirmed available.
func setColors() {
	colYellow = "\033[1;33m"
	colGreen  = "\033[0;32m"
	colRed    = "\033[0;31m"
	colCyan   = "\033[0;36m"
	colBold   = "\033[1m"
	colReset  = "\033[0m"
}

// Bold wraps s in bold ANSI codes. Returns s unchanged when colors are disabled.
func Bold(s string) string { return colBold + s + colReset }

// Green wraps s in green ANSI codes. Returns s unchanged when colors are disabled.
func Green(s string) string { return colGreen + s + colReset }

// Red wraps s in red ANSI codes. Returns s unchanged when colors are disabled.
func Red(s string) string { return colRed + s + colReset }

const ruleLine = "──────────────────────────────────────────────────"

// ClearScreen clears the terminal using ANSI escape codes.
// Does nothing when ANSI output is unavailable (e.g. piped output, old Windows).
func ClearScreen() {
	if colReset != "" {
		fmt.Print("\033[H\033[2J")
	}
}

// Header renders a full-screen title: blank line, bold-yellow title, cyan rule, blank line.
// Call at the top of each menu loop iteration after ClearScreen.
func Header(title string) {
	fmt.Println()
	fmt.Printf("  %s%s%s\n", colYellow, title, colReset)
	fmt.Printf("  %s%s%s\n", colCyan, ruleLine, colReset)
	fmt.Println()
}

// SectionHeader renders a section divider within a screen (no screen clear).
func SectionHeader(title string) {
	fmt.Println()
	fmt.Printf("  %s%s%s\n", colYellow, title, colReset)
	fmt.Printf("  %s%s%s\n", colCyan, ruleLine, colReset)
	fmt.Println()
}

// Divider renders a cyan rule line with 2-space indent.
func Divider() {
	fmt.Printf("  %s%s%s\n", colCyan, ruleLine, colReset)
}

// StatusOK prints a green [OK] tag + message.
func StatusOK(msg string) {
	fmt.Printf("  %s[OK]%s      %s\n", colGreen, colReset, msg)
}

// StatusWarn prints a yellow [WARN] tag + message.
func StatusWarn(msg string) {
	fmt.Printf("  %s[WARN]%s    %s\n", colYellow, colReset, msg)
}

// StatusError prints a red [ERROR] tag + message.
func StatusError(msg string) {
	fmt.Printf("  %s[ERROR]%s   %s\n", colRed, colReset, msg)
}

// StatusMissing prints a red [MISSING] tag + message.
func StatusMissing(msg string) {
	fmt.Printf("  %s[MISSING]%s %s\n", colRed, colReset, msg)
}

// KeyValue prints an aligned label + value pair with 2-space indent.
func KeyValue(label, value string) {
	fmt.Printf("  %-20s %s\n", label, value)
}

// Println2 prints msg with a 2-space indent. An empty msg prints a blank line.
func Println2(msg string) {
	if msg == "" {
		fmt.Println()
	} else {
		fmt.Printf("  %s\n", msg)
	}
}
