package ui

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"
)

// ErrCancelled is returned by Ask and AskPassword when the user presses Enter
// with no input, signalling they want to cancel the current operation.
var ErrCancelled = errors.New("cancelled")

type Prompter struct {
	reader *bufio.Reader
}

func NewPrompter() Prompter {
	return Prompter{
		reader: bufio.NewReader(os.Stdin),
	}
}

// Ask prompts for input. Pressing Enter with no input returns ErrCancelled.
func (p Prompter) Ask(question string, validate func(string) error) (string, error) {
	for {
		fmt.Printf("  %s (Enter to cancel): ", question)
		text, err := p.reader.ReadString('\n')
		if err != nil {
			return "", err
		}

		text = strings.TrimSpace(text)
		if text == "" {
			return "", ErrCancelled
		}

		if validate != nil {
			if err := validate(text); err != nil {
				fmt.Printf("  %s[!]%s %v\n", colRed, colReset, err)
				continue
			}
		}

		return text, nil
	}
}

// AskOptional prompts for optional input. Pressing Enter with no input returns
// ("", nil) — the caller should treat an empty result as "use default/skip".
// Does NOT cancel — use this only for genuinely optional fields.
func (p Prompter) AskOptional(question string) (string, error) {
	fmt.Printf("  %s: ", question)
	text, err := p.reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(text), nil
}

// Confirm asks a yes/no question. Requires y or n — Enter alone is not accepted.
func (p Prompter) Confirm(question string) (bool, error) {
	for {
		fmt.Printf("  %s [y/n]: ", question)
		text, err := p.reader.ReadString('\n')
		if err != nil {
			return false, err
		}
		switch strings.ToLower(strings.TrimSpace(text)) {
		case "y":
			return true, nil
		case "n":
			return false, nil
		default:
			fmt.Printf("  %s[!]%s enter y or n\n", colRed, colReset)
		}
	}
}

// AskPassword prompts for a password with masked input. Pressing Enter with no
// input returns ErrCancelled.
func (p Prompter) AskPassword(question string) (string, error) {
	for {
		fmt.Printf("  %s (Enter to cancel): ", question)
		if term.IsTerminal(int(os.Stdin.Fd())) {
			password, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Println()
			if err != nil {
				return "", err
			}
			value := strings.TrimSpace(string(password))
			if value == "" {
				return "", ErrCancelled
			}
			return value, nil
		}
		// Non-terminal fallback (piped input).
		text, err := p.reader.ReadString('\n')
		if err != nil {
			return "", err
		}
		value := strings.TrimSpace(text)
		if value == "" {
			return "", ErrCancelled
		}
		return value, nil
	}
}

// ReadLine reads a raw line of input from stdin (no prompt printed).
func (p Prompter) ReadLine() (string, error) {
	line, err := p.reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func (p Prompter) Select(title string, options []string) (int, string, error) {
	if len(options) == 0 {
		return 0, "", fmt.Errorf("no options available")
	}

	for {
		if title != "" {
			fmt.Printf("  %s%s%s\n", colBold, title, colReset)
			fmt.Println()
		}
		for i, option := range options {
			fmt.Printf("  %s%d)%s  %s\n", colBold, i+1, colReset, option)
		}
		fmt.Println()
		Divider()
		fmt.Println()
		fmt.Printf("  Choose [1-%d] or Enter to go back: ", len(options))

		answer, err := p.reader.ReadString('\n')
		if err != nil {
			return -1, "", err
		}
		answer = strings.TrimSpace(answer)

		if answer == "" {
			return -1, "", nil
		}

		number, err := strconv.Atoi(answer)
		if err != nil || number < 1 || number > len(options) {
			fmt.Printf("  %s[!]%s enter a number between 1 and %d\n", colRed, colReset, len(options))
			continue
		}

		index := number - 1
		return index, options[index], nil
	}
}
