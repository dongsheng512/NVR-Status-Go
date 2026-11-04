// Package xmlnode is a tiny read-only XML DOM used by the device protocol
// packages. It mirrors the parts of Python's ElementTree the original code
// relies on: direct-child and descendant search plus findtext.
package xmlnode

import (
	"encoding/xml"
	"fmt"
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
			if err.Error() == "EOF" {
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

func trimText(n *Node) {
	n.Text = strings.TrimSpace(n.Text)
	for _, c := range n.Children {
		trimText(c)
	}
}

// Find returns the first node matching a path. Paths are "Tag" for a direct
// child or ".//Tag" for any descendant; "A/B" walks direct children.
func (n *Node) Find(path string) *Node {
	all := n.FindAll(path)
	if len(all) == 0 {
		return nil
	}
	return all[0]
}

// FindAll returns every node matching a path.
func (n *Node) FindAll(path string) []*Node {
	path = strings.TrimSpace(path)
	if path == "" || path == "." {
		return []*Node{n}
	}
	if strings.HasPrefix(path, ".//") {
		rest := strings.TrimPrefix(path, ".//")
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

func (n *Node) descendants(name string) []*Node {
	var out []*Node
	var walk func(*Node)
	walk = func(cur *Node) {
		for _, c := range cur.Children {
			if c.Name == name {
				out = append(out, c)
			}
			walk(c)
		}
	}
	walk(n)
	return out
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
