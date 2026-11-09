package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"golang.org/x/term"

	"hostlier/internal/model"
)

// promptResult carries asynchronously read terminal input or its cancellation error.
type promptResult struct {
	value string
	err   error
}

// promptTerminal temporarily configures terminal echo and restores it after input or cancellation.
func promptTerminal(ctx context.Context, input *os.File, reader *bufio.Reader, output io.Writer, question string, hidden bool) (string, error) {
	// Disable terminal echo while accepting secret input.
	if hidden {
		// Disable echo before exposing the prompt, so an immediate paste cannot race
		// terminal setup. Raw input also lets Ctrl-C cancel without echoing bytes.
		previous, err := term.MakeRaw(int(input.Fd()))
		// Leave terminal state untouched when hidden-input mode cannot be established.
		if err != nil {
			return "", err
		}
		defer term.Restore(int(input.Fd()), previous)
	}
	fmt.Fprint(output, question)
	results := make(chan promptResult, 1)
	// Read terminal input asynchronously so context cancellation can restore terminal state.
	go func() { value, err := readPrompt(reader, hidden); results <- promptResult{value: value, err: err} }()

	// Wait for completed input or cancellation so terminal state is always restored.
	select {
	// Restore terminal state and report prompt cancellation.
	case <-ctx.Done():
		// Restore terminal echo and report cancellation after hidden input.
		if hidden {
			fmt.Fprint(output, "\r\n")
		}
		return "", model.Err("CANCELLED", "Cancelled; no changes applied")
	// Return completed input after restoring the terminal mode.
	case result := <-results:
		// Print a newline after hidden input without exposing its contents.
		if hidden {
			fmt.Fprint(output, "\r\n")
		}
		return result.value, result.err
	}
}

// readPrompt chooses line-oriented visible input or a bounded raw secret reader.
func readPrompt(reader *bufio.Reader, hidden bool) (string, error) {
	// Read secret bytes through the non-echoing editing path.
	if hidden {
		return readHiddenPrompt(reader)
	}
	value, err := reader.ReadString('\n')
	// Reject this path: Cancelled; no changes applied.
	if err != nil {
		return "", model.Err("CANCELLED", "Cancelled; no changes applied")
	}
	return strings.TrimSuffix(strings.TrimSuffix(value, "\n"), "\r"), nil
}

// readHiddenPrompt interprets terminal editing keys without writing secret bytes.
func readHiddenPrompt(reader *bufio.Reader) (string, error) {
	value := make([]byte, 0, 128)
	// Read hidden editing bytes until newline, cancellation, read error or size limit.
	for {
		character, err := reader.ReadByte()
		// Reject this path: Cancelled; no changes applied.
		if err != nil {
			return "", model.Err("CANCELLED", "Cancelled; no changes applied")
		}
		var done bool
		value, done, err = hiddenCharacter(value, character)
		// Abort the secret prompt when the terminal reader fails.
		if err != nil {
			return "", err
		}
		// Finish only when editing reports a complete line.
		if done {
			return string(value), nil
		}
	}
}

// hiddenCharacter applies one raw editing key and enforces the secret input limit.
func hiddenCharacter(value []byte, character byte) ([]byte, bool, error) {
	// Interpret editing and cancellation bytes before accepting ordinary secret input.
	switch character {
	// Finish hidden input on either terminal newline convention.
	case '\r', '\n':
		return value, true, nil
	// Treat Ctrl-C and Ctrl-D as explicit secret-input cancellation.
	case 3, 4:
		return nil, false, model.Err("CANCELLED", "Cancelled; no changes applied")
	// Handle backspace without echoing the secret.
	case 8, 127:
		// Ignore backspace when the secret buffer is already empty.
		if len(value) > 0 {
			_, size := utf8.DecodeLastRune(value)
			value = value[:len(value)-size]
		}
	// Clear the buffered secret on Ctrl-U.
	case 21:
		value = value[:0]
	// Append ordinary input bytes without terminal echo.
	default:
		// Reject this path: Input exceeds 65536 byte limit.
		if character >= 32 || character == '\t' {
			value = append(value, character)
			// Reject this path: Input exceeds 65536 byte limit.
			if len(value) > 65536 {
				return nil, false, model.Err("INPUT_LIMIT", "Input exceeds 65536 byte limit")
			}
		}
	}
	return value, false, nil
}
