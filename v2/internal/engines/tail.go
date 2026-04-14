package engines

import (
	"encoding/json"
	"fmt"
	"strings"
)

// wrapEngineError builds the error returned from an engine command failure.
// Priority for the surfaced message:
//  1. Structured fatal message (e.g., restic --json exit_error.message)
//  2. Last meaningful line of captured stderr
//  3. Exit-status text from err
//
// The prefix is always included so the error identifies which engine step
// failed. Returned errors land in RunResult.ErrorMessage → last_error on
// the server, where the classifier can inspect real engine output.
func wrapEngineError(prefix string, err error, fatalMsg, stderrTail string) error {
	detail := strings.TrimSpace(fatalMsg)
	if detail == "" {
		detail = lastMeaningfulLine(stderrTail)
	}
	if detail == "" {
		return fmt.Errorf("%s: %w", prefix, err)
	}
	return fmt.Errorf("%s: %s (%w)", prefix, detail, err)
}

// lastMeaningfulLine returns the last non-empty line from text.
// Restic/rsync typically print the root cause on the final line ("Fatal: ...",
// "rsync error: ..."), so the tail line is the most useful for classification.
// If the line is a restic --json exit_error event, the embedded message is
// extracted so classifiers see clean text rather than a raw JSON blob.
func lastMeaningfulLine(text string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if msg, ok := unwrapResticExitError(line); ok {
			return msg
		}
		return line
	}
	return ""
}

func unwrapResticExitError(line string) (string, bool) {
	if !strings.HasPrefix(line, `{"message_type":"exit_error"`) {
		return "", false
	}
	var e struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(line), &e); err != nil || e.Message == "" {
		return "", false
	}
	return e.Message, true
}

// errorTailBytes is the maximum size of engine error output kept for the
// last_error field sent to the management server. Generous enough for any
// realistic restic/rsync error, small enough never to blow up payloads.
const errorTailBytes = 2048

// tailBuffer captures up to maxBytes from the tail of the stream written
// to it. Used to pull real error text out of a long running engine without
// buffering the entire stdout/stderr in memory.
type tailBuffer struct {
	data     []byte
	maxBytes int
}

func newTailBuffer(maxBytes int) *tailBuffer {
	return &tailBuffer{maxBytes: maxBytes}
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.data = append(t.data, p...)
	if len(t.data) > t.maxBytes {
		t.data = append(t.data[:0:0], t.data[len(t.data)-t.maxBytes:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	return strings.TrimSpace(string(t.data))
}
