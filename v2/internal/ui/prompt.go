package ui

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
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
		fmt.Printf("%s: ", question)
		text, err := p.reader.ReadString('\n')
		if err != nil {
			return "", err
		}

		text = strings.TrimSpace(text)
		if validate != nil {
			if err := validate(text); err != nil {
				fmt.Println("Invalid input:", err)
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

func (p Prompter) Select(title string, options []string) (int, string, error) {
	if len(options) == 0 {
		return 0, "", fmt.Errorf("no options available")
	}

	for {
		fmt.Println(title)
		for i, option := range options {
			fmt.Printf("%d. %s\n", i+1, option)
		}

		answer, err := p.Ask("Select option number", func(value string) error {
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
			return 0, "", err
		}

		index, _ := strconv.Atoi(answer)
		index--
		return index, options[index], nil
	}
}
