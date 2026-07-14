// Package sexp provides an S-expression parser and writer for KiCad file formats.
// It supports faithful roundtrip: parse → write produces byte-identical output.
package sexp

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Node represents a node in the S-expression tree.
// Either Children is non-nil (list node) or Value is set (atom/string node).
type Node struct {
	// Value holds the raw token text for atoms and quoted strings (including quotes).
	Value string
	// Children holds the child nodes for list nodes.
	Children []*Node
	// IsString is true when the original token was a double-quoted string.
	IsString bool
}

// IsList reports whether this node is a list (parenthesized expression).
func (n *Node) IsList() bool { return n.Children != nil }

// Head returns the first child's Value if this is a list, or "" otherwise.
func (n *Node) Head() string {
	if len(n.Children) == 0 {
		return ""
	}
	return n.Children[0].Value
}

// Parse parses a KiCad S-expression document and returns the top-level nodes.
// A KiCad file is typically a single top-level list, but this returns a slice
// to handle edge cases (e.g., whitespace-only files).
func Parse(input string) ([]*Node, error) {
	p := &parser{input: input, pos: 0}
	var nodes []*Node
	for {
		p.skipWhitespace()
		if p.pos >= len(p.input) {
			break
		}
		n, err := p.parseNode()
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, n)
	}
	return nodes, nil
}

type parser struct {
	input string
	pos   int
}

func (p *parser) skipWhitespace() {
	for p.pos < len(p.input) {
		ch := p.input[p.pos]
		if ch == ' ' || ch == '\t' || ch == '\r' || ch == '\n' {
			p.pos++
		} else {
			break
		}
	}
}

func (p *parser) parseNode() (*Node, error) {
	if p.pos >= len(p.input) {
		return nil, fmt.Errorf("sexp: unexpected end of input")
	}
	ch := p.input[p.pos]
	switch {
	case ch == '(':
		return p.parseList()
	case ch == '"':
		return p.parseString()
	case ch == ')':
		return nil, fmt.Errorf("sexp: unexpected ')' at position %d", p.pos)
	default:
		return p.parseAtom()
	}
}

func (p *parser) parseList() (*Node, error) {
	p.pos++ // consume '('
	node := &Node{}
	for {
		p.skipWhitespace()
		if p.pos >= len(p.input) {
			return nil, fmt.Errorf("sexp: unterminated list")
		}
		if p.input[p.pos] == ')' {
			p.pos++ // consume ')'
			break
		}
		child, err := p.parseNode()
		if err != nil {
			return nil, err
		}
		node.Children = append(node.Children, child)
	}
	return node, nil
}

func (p *parser) parseString() (*Node, error) {
	start := p.pos
	p.pos++ // consume opening '"'
	for p.pos < len(p.input) {
		ch := p.input[p.pos]
		if ch == '\\' {
			p.pos += 2 // skip escape sequence
			continue
		}
		if ch == '"' {
			p.pos++ // consume closing '"'
			return &Node{Value: p.input[start:p.pos], IsString: true}, nil
		}
		// Handle multi-byte UTF-8
		_, size := utf8.DecodeRuneInString(p.input[p.pos:])
		p.pos += size
	}
	return nil, fmt.Errorf("sexp: unterminated string starting at %d", start)
}

func (p *parser) parseAtom() (*Node, error) {
	start := p.pos
	for p.pos < len(p.input) {
		r, size := utf8.DecodeRuneInString(p.input[p.pos:])
		if r == '(' || r == ')' || r == '"' || unicode.IsSpace(r) {
			break
		}
		p.pos += size
	}
	if p.pos == start {
		return nil, fmt.Errorf("sexp: empty atom at position %d", p.pos)
	}
	return &Node{Value: p.input[start:p.pos]}, nil
}

// FindList returns the first direct child list whose head matches name,
// or nil if not found.
func FindList(parent *Node, name string) *Node {
	for _, child := range parent.Children {
		if child.IsList() && child.Head() == name {
			return child
		}
	}
	return nil
}

// FindAllLists returns all direct child lists whose head matches name.
func FindAllLists(parent *Node, name string) []*Node {
	var result []*Node
	for _, child := range parent.Children {
		if child.IsList() && child.Head() == name {
			result = append(result, child)
		}
	}
	return result
}

// AtomValue returns the Value of child at index i if it exists and is an atom,
// otherwise "".
func AtomValue(parent *Node, i int) string {
	if i >= len(parent.Children) {
		return ""
	}
	c := parent.Children[i]
	if c.IsList() {
		return ""
	}
	return c.Value
}

// StringValue returns the unquoted string value of child at index i,
// or "" if the child is not a string node.
func StringValue(parent *Node, i int) string {
	if i >= len(parent.Children) {
		return ""
	}
	c := parent.Children[i]
	if !c.IsString {
		return ""
	}
	return unquote(c.Value)
}

func unquote(s string) string {
	if len(s) < 2 {
		return s
	}
	// Remove surrounding quotes and unescape.
	inner := s[1 : len(s)-1]
	inner = strings.ReplaceAll(inner, `\"`, `"`)
	inner = strings.ReplaceAll(inner, `\\`, `\`)
	return inner
}

// Atom creates a new atom node.
func Atom(v string) *Node { return &Node{Value: v} }

// Str creates a new quoted-string node.
func Str(v string) *Node {
	escaped := strings.ReplaceAll(v, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return &Node{Value: `"` + escaped + `"`, IsString: true}
}

// List creates a new list node with the given children.
func List(children ...*Node) *Node { return &Node{Children: children} }
