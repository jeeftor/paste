package app

import (
	"io"
	"log"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
)

const maxAppLogLines = 500

var (
	appLogs = &logBuffer{}

	sensitiveQueryValue = regexp.MustCompile(`(?i)([?&](?:api[_-]?key|key|token|access_token|password|secret)=)[^&\s]+`)
	sensitiveLogValue   = regexp.MustCompile(`(?i)((?:authorization|api[_-]?key|token|password|secret)\s*[:=]\s*)(?:Bearer\s+)?[^\s,]+`)
)

// logBuffer retains a bounded, redacted copy of the application's log output.
type logBuffer struct {
	mu      sync.RWMutex
	lines   []string
	partial string
}

// Write adds complete log lines to the buffer after redacting sensitive values.
func (buffer *logBuffer) Write(data []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()

	parts := strings.Split(buffer.partial+string(data), "\n")
	buffer.partial = parts[len(parts)-1]
	for _, line := range parts[:len(parts)-1] {
		buffer.addLocked(line)
	}
	return len(data), nil
}

func (buffer *logBuffer) addLocked(line string) {
	line = redactLogLine(line)
	if line == "" {
		return
	}
	buffer.lines = append(buffer.lines, line)
	if len(buffer.lines) > maxAppLogLines {
		buffer.lines = buffer.lines[len(buffer.lines)-maxAppLogLines:]
	}
}

func (buffer *logBuffer) snapshot() []string {
	buffer.mu.RLock()
	defer buffer.mu.RUnlock()
	return append([]string(nil), buffer.lines...)
}

func redactLogLine(line string) string {
	line = sensitiveQueryValue.ReplaceAllString(line, "$1[REDACTED]")
	return sensitiveLogValue.ReplaceAllString(line, "$1[REDACTED]")
}

func configureLogCapture() {
	output := io.MultiWriter(os.Stderr, appLogs)
	log.SetOutput(output)
	slog.SetDefault(slog.New(slog.NewTextHandler(output, nil)))
}

// apiLogsHandler returns the most recent in-memory server log lines.
func apiLogsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, map[string]interface{}{"lines": appLogs.snapshot()})
}
