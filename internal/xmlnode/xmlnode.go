// Package xmlnode is a tiny read-only XML DOM used by the device protocol
// packages. It mirrors the parts of Python's ElementTree the original code
// relies on: direct-child and descendant search plus findtext.
package xmlnode

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Node is one XML element.
type Node struct {
	Name     string
	Text     string
	Children []*Node
}

const maxXMLBytes = 8 << 20 // 8 MiB, same limit as the Python client

// Parse builds a DOM. It rejects DOCTYPE/ENTITY declarations and oversized
// documents, like nvr_core/isapi_client._safe_fromstring.
func Parse(text string) (*Node, error) {
	if len(text) > maxXMLBytes {
		return nil, fmt.Errorf("xml response too large: %d bytes", len(text))
	}
	head := text
	if len(head) > 4096 {
		head = head[:4096]
	}
	lower := strings.ToLower(head)
	if strings.Contains(lower, "<!doctype") || strings.Contains(lower, "<!entity") {
		return nil, fmt.Errorf("xml contains DOCTYPE/ENTITY, refused")
	}

	dec := xml.NewDecoder(strings.NewReader(text))
	var root *Node
	var stack []*Node
	for {
		tok, err := dec.Token()
		if err != nil {
			// errors.Is, not err.Error()=="EOF": a wrapped error would slip
			// through the string comparison and be reported as a parse failure.
			// A truncated document reports "unexpected EOF", not io.EOF, so it
			// still falls through to the error return below.
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			n := &Node{Name: t.Name.Local}
			if len(stack) > 0 {
				parent := stack[len(stack)-1]
				parent.Children = append(parent.Children, n)
			} else if root == nil {
				root = n
			}
			stack = append(stack, n)
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			if len(stack) > 0 {
				stack[len(stack)-1].Text += string(t)
			}
		}
	}
	if root == nil {
		return nil, fmt.Errorf("empty xml")
	}
	trimText(root)
	return root, nil
}

// trimText trims every node's text. Iterative for the same reason as
// descendants: a deeply nested document must not consume the goroutine stack.
func trimText(root *Node) {
	stack := []*Node{root}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		cur.Text = strings.TrimSpace(cur.Text)
		stack = append(stack, cur.Children...)
	}
}

// Find returns the first node matching a path. Paths are "Tag" for a direct
// child or ".//Tag" for any descendant; "A/B" walks direct children.
func (n *Node) Find(path string) *Node {
	path = strings.TrimSpace(path)
	if path == "" || path == "." {
		return n
	}
	if rest, ok := strings.CutPrefix(path, ".//"); ok {
		parts := strings.Split(rest, "/")
		if len(parts) == 1 {
			// The common case (".//playbackURI"): stop at the first hit
			// instead of collecting every match and taking index 0.
			return n.firstDescendant(parts[0])
		}
		// ".//A/B" cannot be short-circuited to "first A, then B": if the first
		// A has no B child, FindAll would fall through to the next A.
		for _, a := range n.descendants(parts[0]) {
			if hit := firstByPath(a, parts[1:]); hit != nil {
				return hit
			}
		}
		return nil
	}
	return firstByPath(n, strings.Split(path, "/"))
}

// firstByPath walks direct children along parts and returns the final node.
func firstByPath(n *Node, parts []string) *Node {
	cur := n
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		var next *Node
		for _, c := range cur.Children {
			if c.Name == part {
				next = c
				break
			}
		}
		if next == nil {
			return nil
		}
		cur = next
	}
	return cur
}

// FindAll returns every node matching a path.
func (n *Node) FindAll(path string) []*Node {
	path = strings.TrimSpace(path)
	if path == "" || path == "." {
		return []*Node{n}
	}
	if rest, ok := strings.CutPrefix(path, ".//"); ok {
		parts := strings.Split(rest, "/")
		cur := n.descendants(parts[0])
		for _, part := range parts[1:] {
			if part == "" {
				continue
			}
			cur = childrenNamed(cur, part)
		}
		return cur
	}
	parts := strings.Split(path, "/")
	cur := []*Node{n}
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		cur = childrenNamed(cur, part)
	}
	return cur
}

// FindText returns the text of the first match, or def.
func (n *Node) FindText(path, def string) string {
	if m := n.Find(path); m != nil && m.Text != "" {
		return m.Text
	}
	return def
}

// descendants walks the subtree in document order.
//
// Iterative on purpose: maxXMLBytes (8 MiB) allows documents nested hundreds of
// thousands of levels deep, and a recursive walk would overflow the goroutine
// stack. Stack overflow is a fatal error — recover() cannot catch it, so the
// whole process dies instead of returning a parse error.
func (n *Node) descendants(name string) []*Node {
	var out []*Node
	stack := make([]*Node, 0, len(n.Children))
	for i := len(n.Children) - 1; i >= 0; i-- {
		stack = append(stack, n.Children[i])
	}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if cur.Name == name {
			out = append(out, cur)
		}
		for i := len(cur.Children) - 1; i >= 0; i-- {
			stack = append(stack, cur.Children[i])
		}
	}
	return out
}

// firstDescendant is descendants but returns the first hit in document order,
// stopping the walk right there.
func (n *Node) firstDescendant(name string) *Node {
	stack := make([]*Node, 0, len(n.Children))
	for i := len(n.Children) - 1; i >= 0; i-- {
		stack = append(stack, n.Children[i])
	}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if cur.Name == name {
			return cur
		}
		for i := len(cur.Children) - 1; i >= 0; i-- {
			stack = append(stack, cur.Children[i])
		}
	}
	return nil
}

func childrenNamed(parents []*Node, name string) []*Node {
	var out []*Node
	for _, p := range parents {
		for _, c := range p.Children {
			if c.Name == name {
				out = append(out, c)
			}
		}
	}
	return out
}
