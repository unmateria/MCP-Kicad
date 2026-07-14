// Package rank provides visual quality ranking of layout candidates by
// asking the Claude API which schematic image looks the most professional.
//
// The package falls back to "no opinion" (empty winner) when the API key is
// missing or the request fails. Callers should pair this with their own
// geometric scorer so the system always produces a result.
package rank

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	apiURL          = "https://api.anthropic.com/v1/messages"
	apiVersion      = "2023-06-01"
	rankModel       = "claude-haiku-4-5-20251001"
	requestTimeout  = 20 * time.Second
	maxImageBytes   = 1024 * 1024 // 1 MB cap per image — bigger gets rejected
	defaultMaxTokens = 200
)

// ImageCandidate is one schematic to be ranked. ID is an arbitrary stable
// identifier the caller uses to map back to its own data.
type ImageCandidate struct {
	ID       string
	PNGBytes []byte
}

// Decision is the outcome of a single ranking call.
type Decision struct {
	WinnerID string
	Reason   string
}

// pairwisePrompt is the fixed prompt used for all comparisons. Keeping it
// constant makes results comparable across runs.
const pairwisePrompt = "Compara estos esquemas KiCad numerados (0, 1, 2…). " +
	"Elige el más limpio según criterios profesionales: menos cruces de hilos, símbolos alineados " +
	"vertical/horizontalmente, white-space distribuido, decoupling caps cerca de pines de power, " +
	"sin wires dando vueltas innecesarias. " +
	"Responde con un JSON exactamente: {\"winner\": <indice>, \"reason\": \"<hasta 80 chars>\"}."

type messageRequest struct {
	Model     string         `json:"model"`
	MaxTokens int            `json:"max_tokens"`
	Messages  []apiMessage   `json:"messages"`
	System    string         `json:"system,omitempty"`
}

type apiMessage struct {
	Role    string         `json:"role"`
	Content []contentBlock `json:"content"`
}

type contentBlock struct {
	Type   string       `json:"type"`
	Text   string       `json:"text,omitempty"`
	Source *imageSource `json:"source,omitempty"`
}

type imageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type messageResponse struct {
	Content []responseBlock `json:"content"`
	Error   *apiError       `json:"error,omitempty"`
}

type responseBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type apiError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// rank invokes the API to compare candidates. apiKey must be non-empty.
// Returns the chosen index (0-based into candidates) and reason text.
func rank(ctx context.Context, apiKey string, candidates []ImageCandidate) (winnerIdx int, reason string, err error) {
	if len(candidates) < 2 {
		return 0, "", fmt.Errorf("need ≥2 candidates, got %d", len(candidates))
	}
	for i, c := range candidates {
		if len(c.PNGBytes) == 0 {
			return 0, "", fmt.Errorf("candidate %d has empty PNG", i)
		}
		if len(c.PNGBytes) > maxImageBytes {
			return 0, "", fmt.Errorf("candidate %d PNG is %d bytes (cap %d)", i, len(c.PNGBytes), maxImageBytes)
		}
	}

	blocks := make([]contentBlock, 0, len(candidates)*2+1)
	for i, c := range candidates {
		blocks = append(blocks, contentBlock{Type: "text", Text: fmt.Sprintf("Imagen %d:", i)})
		blocks = append(blocks, contentBlock{
			Type: "image",
			Source: &imageSource{
				Type:      "base64",
				MediaType: "image/png",
				Data:      base64.StdEncoding.EncodeToString(c.PNGBytes),
			},
		})
	}
	blocks = append(blocks, contentBlock{Type: "text", Text: pairwisePrompt})

	req := messageRequest{
		Model:     rankModel,
		MaxTokens: defaultMaxTokens,
		Messages: []apiMessage{
			{Role: "user", Content: blocks},
		},
	}
	body, err := json.Marshal(req)
	if err != nil {
		return 0, "", fmt.Errorf("marshal: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, apiURL, bytes.NewReader(body))
	if err != nil {
		return 0, "", fmt.Errorf("new request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("anthropic-version", apiVersion)
	httpReq.Header.Set("x-api-key", apiKey)

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return 0, "", fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, "", fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return 0, "", fmt.Errorf("api status %d: %s", resp.StatusCode, string(respBody))
	}

	var mr messageResponse
	if err := json.Unmarshal(respBody, &mr); err != nil {
		return 0, "", fmt.Errorf("decode: %w (%s)", err, string(respBody))
	}
	if mr.Error != nil {
		return 0, "", fmt.Errorf("api error: %s — %s", mr.Error.Type, mr.Error.Message)
	}
	if len(mr.Content) == 0 {
		return 0, "", fmt.Errorf("api returned no content")
	}
	var text string
	for _, b := range mr.Content {
		if b.Type == "text" {
			text += b.Text
		}
	}
	idx, reason, ok := parseDecision(text)
	if !ok {
		return 0, "", fmt.Errorf("could not parse decision from response: %q", text)
	}
	if idx < 0 || idx >= len(candidates) {
		return 0, "", fmt.Errorf("winner index %d out of bounds (have %d candidates)", idx, len(candidates))
	}
	return idx, reason, nil
}

var jsonObjectRe = regexp.MustCompile(`\{[^{}]*\}`)

// parseDecision extracts {winner, reason} from the model's text reply. The
// model is asked to emit clean JSON but sometimes wraps it in prose; we scan
// for the first JSON object that has a "winner" key.
func parseDecision(text string) (int, string, bool) {
	for _, m := range jsonObjectRe.FindAllString(text, -1) {
		var d struct {
			Winner json.RawMessage `json:"winner"`
			Reason string          `json:"reason"`
		}
		if err := json.Unmarshal([]byte(m), &d); err != nil {
			continue
		}
		if len(d.Winner) == 0 {
			continue
		}
		// Winner may be a number or a quoted string like "0".
		s := strings.Trim(string(d.Winner), `"`)
		idx, err := strconv.Atoi(s)
		if err != nil {
			continue
		}
		return idx, d.Reason, true
	}
	return 0, "", false
}

// Logf is a swap-able logger so tests can capture output.
var Logf = func(format string, args ...any) { log.Printf("[rank] "+format, args...) }
