package elk

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

//go:embed embed/elk_layout.js
var layoutScript string

// ErrNotAvailable is returned when Node.js or elkjs isn't installed and the
// caller should fall back to the pure-Go Sugiyama implementation.
var ErrNotAvailable = errors.New("elkjs subprocess not available; install Node.js + `npm install -g elkjs`")

// Layouter encapsulates the elkjs subprocess. It is safe for concurrent use;
// each Run call spawns a fresh subprocess.
type Layouter struct {
	// NodeBin is the path to the node executable. Auto-detected by Detect().
	NodeBin string
	// Timeout caps the subprocess wall clock. ELK on a 16-symbol graph is
	// well under a second; 5s is generous.
	Timeout time.Duration

	scriptPath string
	once       sync.Once
	scriptErr  error
}

// Detect returns a configured Layouter when Node.js is available on PATH,
// or ErrNotAvailable otherwise. The actual elkjs availability is verified
// lazily on the first Run() call to avoid paying that cost at startup.
func Detect() (*Layouter, error) {
	bin, err := exec.LookPath("node")
	if err != nil {
		// Try common Windows install location.
		alt := `C:\Program Files\nodejs\node.exe`
		if _, e := os.Stat(alt); e == nil {
			bin = alt
		} else {
			return nil, ErrNotAvailable
		}
	}
	return &Layouter{NodeBin: bin, Timeout: 5 * time.Second}, nil
}

// Run sends `graph` through the embedded Node script and returns the laid-out
// graph. On timeout or any subprocess error, returns the wrapped error so the
// caller can fall back.
func (l *Layouter) Run(ctx context.Context, graph Graph) (Graph, error) {
	if l == nil {
		return Graph{}, ErrNotAvailable
	}
	if err := l.ensureScript(); err != nil {
		return Graph{}, err
	}

	if l.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, l.Timeout)
		defer cancel()
	}

	in, err := json.Marshal(graph)
	if err != nil {
		return Graph{}, fmt.Errorf("elk: marshal: %w", err)
	}

	cmd := exec.CommandContext(ctx, l.NodeBin, l.scriptPath)
	cmd.Stdin = bytes.NewReader(in)
	// On Windows, npm's global packages live in %APPDATA%\npm\node_modules
	// but Node's default require search doesn't include that directory.
	// Splice it into NODE_PATH so the embedded script can `require('elkjs')`.
	cmd.Env = augmentNodePath(os.Environ())
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return Graph{}, fmt.Errorf("elk: subprocess: %w (%s)", err, stderr.String())
	}
	var out Graph
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		return Graph{}, fmt.Errorf("elk: unmarshal: %w (stdout=%q)", err, stdout.String())
	}
	return out, nil
}

// augmentNodePath adds the global npm modules directory to NODE_PATH if it
// exists and isn't already there. Without this, `require('elkjs')` from a
// stand-alone script can fail on Windows even though `npm install -g elkjs`
// completed successfully.
func augmentNodePath(env []string) []string {
	dirs := globalNodeModuleDirs()
	if len(dirs) == 0 {
		return env
	}
	const sep = string(os.PathListSeparator)
	join := func(extra string) string {
		if extra == "" {
			return ""
		}
		// Re-emit a fresh NODE_PATH; we intentionally drop any prior value so
		// stale entries on the user's shell don't shadow ours.
		return "NODE_PATH=" + extra
	}
	out := env[:0]
	seen := false
	for _, kv := range env {
		if len(kv) >= 10 && kv[:10] == "NODE_PATH=" {
			seen = true
			merged := kv[10:] + sep + join(dirsJoin(dirs, sep))[10:]
			out = append(out, "NODE_PATH="+merged)
			continue
		}
		out = append(out, kv)
	}
	if !seen {
		out = append(out, join(dirsJoin(dirs, sep)))
	}
	return out
}

func dirsJoin(dirs []string, sep string) string {
	if len(dirs) == 0 {
		return ""
	}
	out := dirs[0]
	for _, d := range dirs[1:] {
		out += sep + d
	}
	return out
}

// globalNodeModuleDirs returns paths likely to contain globally-installed
// npm packages on the host. We don't shell out to `npm root -g` because that
// pulls in node startup overhead — instead we probe the well-known locations.
func globalNodeModuleDirs() []string {
	var out []string
	candidates := []string{
		os.Getenv("APPDATA") + `\npm\node_modules`, // Windows user-global
		`C:\Program Files\nodejs\node_modules`,     // Windows install-wide
		`/usr/local/lib/node_modules`,              // Linux/macOS
		`/usr/lib/node_modules`,
	}
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if info, err := os.Stat(c); err == nil && info.IsDir() {
			out = append(out, c)
		}
	}
	return out
}

// ensureScript writes the embedded JS to a tmp file the first time it is
// needed. The file is reused across Run calls in the same process.
func (l *Layouter) ensureScript() error {
	l.once.Do(func() {
		dir, err := os.MkdirTemp("", "mcp-kicad-elk-")
		if err != nil {
			l.scriptErr = err
			return
		}
		path := filepath.Join(dir, "elk_layout.js")
		if err := os.WriteFile(path, []byte(layoutScript), 0o644); err != nil {
			l.scriptErr = err
			return
		}
		l.scriptPath = path
	})
	return l.scriptErr
}
