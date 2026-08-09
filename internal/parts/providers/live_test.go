package providers

import (
	"context"
	"math"
	"os"
	"testing"
	"time"

	"mcp-kicad/internal/fplib"
)

// TestLiveSources talks to the real repositories. It is off by default —
// `go test ./...` runs on a CI machine with no network and must stay green —
// but the URLs, branch names and directory layouts baked into repos.go are
// claims about the outside world, and this is how they get re-measured:
//
//	MCP_KICAD_LIVE=1 go test ./internal/parts/providers/ -run TestLiveSources -v
func TestLiveSources(t *testing.T) {
	if os.Getenv("MCP_KICAD_LIVE") == "" {
		t.Skip("set MCP_KICAD_LIVE=1 to check the real repositories")
	}
	root := t.TempDir()
	if cache := os.Getenv("MCP_KICAD_LIVE_CACHE"); cache != "" {
		root = cache // reuse a warm index instead of downloading again
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	cases := []struct {
		provider string
		query    string
	}{
		{"jlcpcb", "LTV-217-B-G"},
		{"cern", "AA52F"},
		{"espressif", "ESP32-C3-MINI-1"},
		{"digikey-lib", "NE555"},
		{"sparkfun", "LED"},
	}
	for _, tc := range cases {
		t.Run(tc.provider, func(t *testing.T) {
			p, err := Get(Env{LibsRoot: root}, tc.provider)
			if err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			cands, err := p.Search(ctx, Query{Text: tc.query, Limit: 3})
			if err != nil {
				t.Fatalf("search: %v", err)
			}
			t.Logf("%s: %d hits for %q in %s", tc.provider, len(cands), tc.query, time.Since(start).Round(time.Millisecond))
			for _, c := range cands {
				t.Logf("  %-40s %-12v %s", c.MPN, c.Has, c.Description)
			}
			if len(cands) == 0 {
				t.Fatalf("%s returned nothing for %q", tc.provider, tc.query)
			}

			b, err := p.Fetch(ctx, cands[0].ID)
			if err != nil {
				t.Fatalf("fetch %s: %v", cands[0].ID, err)
			}
			if len(b.Assets[Symbol]) == 0 {
				t.Fatal("fetch returned no symbol")
			}
			t.Logf("  fetched %s: symbol %d B, footprint %d B, model %d B, ref %q, notes %v",
				b.MPN, len(b.Assets[Symbol]), len(b.Assets[Footprint]), len(b.Assets[Model3D]),
				b.FootprintRef, b.Notes)
		})
	}
}

// TestLiveEasyEDAAgreesWithJLCPCB checks the EasyEDA converter against an
// independently authored KiCad footprint for the same package. LTV-217-B-G
// exists as EasyEDA JSON, which we convert ourselves, and as a KiCad file
// somebody else drew.
//
// What it asserts, and why only this:
//
//   - PITCH must match exactly. It is a property of the package, not of
//     whoever drew it, and a wrong scale constant shows up here immediately.
//   - CHIRALITY must match. A footprint that came out MIRRORED cannot be
//     assembled, and no amount of rotating fixes it. Measured as the sign of
//     the cross product across three pads.
//
// It deliberately does NOT compare absolute pad positions or pad sizes.
// Measured on this pair: our conversion sits 180° from CDFER's, and the two
// libraries choose different pad lengths (2.00 mm against 1.62 mm), which
// moves every pad centre. Both are legitimate authoring choices — the pad
// NUMBERS are what determine assembly — so demanding they agree would fail on
// a correct conversion.
//
//	MCP_KICAD_LIVE=1 go test ./internal/parts/providers/ -run TestLiveEasyEDA -v
func TestLiveEasyEDAAgreesWithJLCPCB(t *testing.T) {
	if os.Getenv("MCP_KICAD_LIVE") == "" {
		t.Skip("set MCP_KICAD_LIVE=1 to check the converter against the real libraries")
	}
	root := t.TempDir()
	if cache := os.Getenv("MCP_KICAD_LIVE_CACHE"); cache != "" {
		root = cache
	}
	env := Env{LibsRoot: root}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	lcsc, err := Get(env, "lcsc")
	if err != nil {
		t.Fatal(err)
	}
	ours, err := lcsc.Fetch(ctx, "C115450")
	if err != nil {
		t.Fatal(err)
	}
	converted, err := fplib.Parse(ours.Assets[Footprint])
	if err != nil {
		t.Fatal(err)
	}

	jlc, err := Get(env, "jlcpcb")
	if err != nil {
		t.Fatal(err)
	}
	cands, err := jlc.Search(ctx, Query{Text: "LTV-217-B-G"})
	if err != nil || len(cands) == 0 {
		t.Fatalf("jlcpcb has no LTV-217-B-G to compare against (%v)", err)
	}
	theirs, err := jlc.Fetch(ctx, cands[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	reference, err := fplib.Parse(theirs.Assets[Footprint])
	if err != nil {
		t.Fatal(err)
	}

	ourPads := byNumber(converted.Pads())
	refPads := byNumber(reference.Pads())
	if len(refPads) == 0 || len(ourPads) == 0 {
		t.Fatal("one of the footprints has no numbered pads")
	}
	for n := range refPads {
		if _, ok := ourPads[n]; !ok {
			t.Errorf("pad %s is in the reference but not in our conversion", n)
		}
	}
	for _, n := range []string{"1", "2", "3", "4"} {
		p, q := ourPads[n], refPads[n]
		t.Logf("pad %-3s ours(%7.3f,%7.3f) %.2f×%.2f   ref(%7.3f,%7.3f) %.2f×%.2f",
			n, p.X, p.Y, p.W, p.H, q.X, q.Y, q.W, q.H)
	}

	oursPitch := math.Abs(ourPads["1"].Y - ourPads["2"].Y)
	refPitch := math.Abs(refPads["1"].Y - refPads["2"].Y)
	t.Logf("pitch: ours %.3f mm, reference %.3f mm", oursPitch, refPitch)
	if math.Abs(oursPitch-refPitch) > 0.02 {
		t.Errorf("pitch disagrees: %.3f vs %.3f mm — the scale constant is wrong", oursPitch, refPitch)
	}

	oursHand := chirality(ourPads["1"], ourPads["2"], ourPads["3"])
	refHand := chirality(refPads["1"], refPads["2"], refPads["3"])
	t.Logf("chirality: ours %+.2f, reference %+.2f", oursHand, refHand)
	if oursHand*refHand < 0 {
		t.Error("the converted footprint is MIRRORED relative to the reference — it could not be assembled")
	}
}

func byNumber(pads []fplib.Pad) map[string]fplib.Pad {
	out := map[string]fplib.Pad{}
	for _, p := range pads {
		if p.Number != "" {
			out[p.Number] = p
		}
	}
	return out
}

// chirality is the signed area of the triangle three pads make. Rotating a
// footprint keeps the sign; mirroring it flips it, which is the one difference
// between two authors that is not a matter of taste.
func chirality(a, b, c fplib.Pad) float64 {
	return (b.X-a.X)*(c.Y-a.Y) - (b.Y-a.Y)*(c.X-a.X)
}
