package ui

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"
)

type Prompter struct {
	reader *bufio.Reader
}

func NewPrompter() Prompter {
	return Prompter{
		reader: bufio.NewReader(os.Stdin),
	}
}

func (p Prompter) Ask(question string, validate func(string) error) (string, error) {
	for {
		fmt.Printf("  %s: ", question)
		text, err := p.reader.ReadString('\n')
		if err != nil {
			return "", err
		}

		text = strings.TrimSpace(text)
		if validate != nil {
			if err := validate(text); err != nil {
				fmt.Printf("  %s[!]%s %v\n", colRed, colReset, err)
				continue
			}
		}

		return text, nil
	}
}

func (p Prompter) Confirm(question string) (bool, error) {
	answer, err := p.Ask(question+" [y/n]", func(value string) error {
		switch strings.ToLower(value) {
		case "y", "n":
			return nil
		}
		return fmt.Errorf("enter y or n")
	})
	if err != nil {
		return false, err
	}
	return strings.ToLower(answer) == "y", nil
}

// AskPassword prompts for a password with masked input (characters are not echoed).
// Falls back to plain Ask if stdin is not a terminal (e.g. piped input).
func (p Prompter) AskPassword(question string) (string, error) {
	for {
		fmt.Printf("  %s: ", question)
		if term.IsTerminal(int(os.Stdin.Fd())) {
			password, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Println()
			if err != nil {
				return "", err
			}
			value := strings.TrimSpace(string(password))
			if value == "" {
				fmt.Printf("  %s[!]%s Password cannot be empty.\n", colRed, colReset)
				continue
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
			fmt.Printf("  %s[!]%s Password cannot be empty.\n", colRed, colReset)
			continue
		}
		return value, nil
	}
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

		answer, err := p.Ask(
			fmt.Sprintf("Choose [1-%d] or Enter to go back", len(options)),
			func(value string) error {
				if strings.TrimSpace(value) == "" {
					return nil
				}
				number, err := strconv.Atoi(value)
				if err != nil {
					return fmt.Errorf("enter a number")
				}
				if number < 1 || number > len(options) {
					return fmt.Errorf("enter a number between 1 and %d", len(options))
				}
				return nil
			})
		if err != nil {
			return -1, "", err
		}

		if strings.TrimSpace(answer) == "" {
			return -1, "", nil
		}

		index, _ := strconv.Atoi(answer)
		index--
		return index, options[index], nil
	}
}
