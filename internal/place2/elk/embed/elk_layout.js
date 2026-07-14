// elk_layout.js — embedded into the Go binary via go:embed.
//
// Reads a single JSON object from stdin (the ELK graph), invokes elkjs to
// compute positions, and writes the laid-out JSON back to stdout. Errors go
// to stderr; the Go side propagates them.
//
// Usage (from Go):
//   node -e "$(go:embed contents)" <<< $JSON
// or, when distributed standalone:
//   node elk_layout.js < input.json > output.json
'use strict';

const ELK = require('elkjs');

async function main() {
  let raw = '';
  process.stdin.setEncoding('utf8');
  for await (const chunk of process.stdin) {
    raw += chunk;
  }
  let graph;
  try {
    graph = JSON.parse(raw);
  } catch (e) {
    console.error('parse error:', e.message);
    process.exit(2);
  }
  const elk = new ELK();
  try {
    const layouted = await elk.layout(graph);
    process.stdout.write(JSON.stringify(layouted));
  } catch (e) {
    console.error('elk error:', e.message);
    process.exit(3);
  }
}

main();
