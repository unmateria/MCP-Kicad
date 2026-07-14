package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// SessionLogger writes a structured log file for one server process lifetime.
// Each file is named mcp-kicad-YYYYMMDD-HHMMSS.log inside <outputDir>/logs/.
// The log is plain text — one line per event — so Claude Code can read it
// directly with Read tool to diagnose issues without user copy-paste.
type SessionLogger struct {
	mu   sync.Mutex
	f    *os.File
	path string
}

// NewSessionLogger creates a new log file under <outputDir>/logs/.
// Returns a non-nil logger even if the file cannot be opened (logs are silently
// dropped in that case, never panics).
func NewSessionLogger(outputDir string) *SessionLogger {
	logDir := filepath.Join(outputDir, "logs")
	_ = os.MkdirAll(logDir, 0o755)
	stamp := time.Now().Format("20060102-150405")
	logPath := filepath.Join(logDir, "mcp-kicad-"+stamp+".log")
	f, _ := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	l := &SessionLogger{f: f, path: logPath}
	l.write("SESSION_START", fmt.Sprintf("pid=%d log=%s", os.Getpid(), logPath))
	return l
}

// Path returns the absolute path to the current log file.
func (l *SessionLogger) Path() string { return l.path }

// Info logs a free-form informational message.
func (l *SessionLogger) Info(msg string) { l.write("INFO", msg) }

// Error logs an error message.
func (l *SessionLogger) Error(msg string) { l.write("ERROR", msg) }

func (l *SessionLogger) write(level, msg string) {
	if l == nil || l.f == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	ts := time.Now().Format("15:04:05.000")
	_, _ = fmt.Fprintf(l.f, "[%s] %-14s %s\n", ts, level, msg)
}

// WrapTool wraps an MCP tool handler to log every call: input JSON, result
// text (truncated to 800 chars), duration, and any Go error or panic text.
//
// Usage in Register* functions:
//
//	mcp.AddTool(s, tool, WrapTool(env.Log, "tool_name", env.handleXxx))
func WrapTool[T any](
	l *SessionLogger,
	name string,
	h func(context.Context, *mcp.CallToolRequest, T) (*mcp.CallToolResult, any, error),
) func(context.Context, *mcp.CallToolRequest, T) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input T) (*mcp.CallToolResult, any, error) {
		start := time.Now()

		// Log input — marshal the typed struct so field names are human-readable.
		inputBytes, _ := json.Marshal(input)
		inputStr := string(inputBytes)
		if len(inputStr) > 600 {
			inputStr = inputStr[:600] + "…"
		}
		l.write("TOOL_IN", fmt.Sprintf("tool=%-28s input=%s", name, inputStr))

		result, a, err := h(ctx, req, input)
		dur := time.Since(start)

		// Extract text content from result for logging.
		var resultText string
		if result != nil {
			for _, c := range result.Content {
				if tc, ok := c.(*mcp.TextContent); ok {
					resultText = tc.Text
					break
				}
			}
		}
		truncated := resultText
		if len(truncated) > 800 {
			truncated = truncated[:800] + "…[truncated]"
		}

		if err != nil {
			l.write("TOOL_ERR", fmt.Sprintf("tool=%-28s dur=%4dms err=%v", name, dur.Milliseconds(), err))
		} else if strings.HasPrefix(resultText, "internal error (panic)") {
			l.write("TOOL_PANIC", fmt.Sprintf("tool=%-28s dur=%4dms result=%q", name, dur.Milliseconds(), truncated))
		} else {
			l.write("TOOL_OUT", fmt.Sprintf("tool=%-28s dur=%4dms result=%q", name, dur.Milliseconds(), truncated))
		}

		return result, a, err
	}
}
