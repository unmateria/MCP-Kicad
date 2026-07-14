package sexp

import (
	"strings"
)

// Write serializes a slice of top-level nodes back to S-expression text.
// The output is intended to be byte-compatible with KiCad file formatting.
func Write(nodes []*Node) string {
	var sb strings.Builder
	for i, n := range nodes {
		if i > 0 {
			sb.WriteByte('\n')
		}
		writeNode(&sb, n, 0)
	}
	sb.WriteByte('\n')
	return sb.String()
}

// WriteNode serializes a single node to a string.
func WriteNode(n *Node) string {
	var sb strings.Builder
	writeNode(&sb, n, 0)
	return sb.String()
}

func writeNode(sb *strings.Builder, n *Node, depth int) {
	if !n.IsList() {
		sb.WriteString(n.Value)
		return
	}
	sb.WriteByte('(')
	for i, child := range n.Children {
		if i > 0 {
			// Decide whether to use a space or newline+indent.
			// Simple heuristic: inline if the child is a short atom or string,
			// otherwise newline+indent.
			if needsNewline(child, depth) {
				sb.WriteByte('\n')
				writeIndent(sb, depth+1)
			} else {
				sb.WriteByte(' ')
			}
		}
		writeNode(sb, child, depth+1)
	}
	sb.WriteByte(')')
}

// needsNewline decides if a child node should be placed on a new line.
// List children that are not trivially small get their own line.
func needsNewline(n *Node, parentDepth int) bool {
	if !n.IsList() {
		return false
	}
	// Short lists with only atom children can stay inline.
	if len(n.Children) <= 3 {
		allAtoms := true
		for _, c := range n.Children {
			if c.IsList() {
				allAtoms = false
				break
			}
		}
		if allAtoms {
			return false
		}
	}
	return true
}

func writeIndent(sb *strings.Builder, depth int) {
	for i := 0; i < depth; i++ {
		sb.WriteString("  ")
	}
}
