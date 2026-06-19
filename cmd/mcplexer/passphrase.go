package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// promptPassphrase reads a single passphrase from stdin without echo when
// stdin is a terminal, and from a single line of stdin otherwise.
//
// Plain-stdin mode exists for tests, scripted use, and pipe inputs; in those
// contexts we are not in a position to enforce no-echo anyway.
func promptPassphrase(prompt string) (string, error) {
	if envPass := os.Getenv("MCPLEXER_SIGNER_PASSPHRASE"); envPass != "" {
		return envPass, nil
	}
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		fmt.Fprint(os.Stderr, prompt)
		raw, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", fmt.Errorf("read passphrase: %w", err)
		}
		s := strings.TrimRight(string(raw), "\r\n")
		if s == "" {
			return "", errors.New("empty passphrase")
		}
		return s, nil
	}

	r := bufio.NewReader(os.Stdin)
	line, err := r.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) && line == "" {
		return "", fmt.Errorf("read passphrase: %w", err)
	}
	s := strings.TrimRight(line, "\r\n")
	if s == "" {
		return "", errors.New("empty passphrase")
	}
	return s, nil
}

// promptPassphraseConfirm asks twice and verifies they match.
func promptPassphraseConfirm() (string, error) {
	a, err := promptPassphrase("Choose a passphrase: ")
	if err != nil {
		return "", err
	}
	b, err := promptPassphrase("Confirm passphrase:  ")
	if err != nil {
		return "", err
	}
	if a != b {
		return "", errors.New("passphrases did not match")
	}
	return a, nil
}
