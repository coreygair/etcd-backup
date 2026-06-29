package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"filippo.io/age"
)

// ParseRecipients parses a combination of inline recipient strings
// and multi-recipient files.
//
// Fails if no recipients are parsed.
func ParseRecipients(recipients, recipientFiles []string) ([]age.Recipient, error) {
	var out []age.Recipient

	for _, r := range recipients {
		parsed, err := age.ParseRecipients(strings.NewReader(r))
		if err != nil {
			return nil, fmt.Errorf("invalid recipient %q: %w", r, err)
		}
		out = append(out, parsed...)
	}

	for _, path := range recipientFiles {
		f, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("failed to open recipient file %q: %w", path, err)
		}

		parsed, err := age.ParseRecipients(f)
		_ = f.Close()
		if err != nil {
			return nil, fmt.Errorf("invalid recipient file %q: %w", path, err)
		}
		out = append(out, parsed...)
	}

	if len(out) == 0 {
		return out, errors.New("no recipients")
	}

	return out, nil
}

// EncryptSnapshot encrypts a snapshot stream with a set of age recipients.
func EncryptSnapshot(recipients []age.Recipient, src io.Reader) (io.Reader, error) {
	if len(recipients) == 0 {
		return nil, errors.New("no recipients")
	}

	pr, pw := io.Pipe()

	go func() {
		w, err := age.Encrypt(pw, recipients...)
		if err != nil {
			pw.CloseWithError(fmt.Errorf("failed to initialize age encryption: %w", err))
			return
		}

		if _, err := io.Copy(w, src); err != nil {
			pw.CloseWithError(fmt.Errorf("failed to encrypt snapshot: %w", err))
			return
		}

		if err := w.Close(); err != nil {
			pw.CloseWithError(fmt.Errorf("failed to finalize age encryption: %w", err))
			return
		}

		_ = pw.Close()
	}()

	return pr, nil
}
