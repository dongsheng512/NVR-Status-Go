package isapi

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
)

// XMLMaxBytes is the maximum accepted XML body size, mirroring the Python
// client's _XML_MAX_BYTES (8 MiB).
const XMLMaxBytes = 8 * 1024 * 1024

var (
	// ErrDTD is returned when a document contains a DOCTYPE or ENTITY
	// declaration. Device XML never legitimately needs either, and rejecting
	// them prevents entity-expansion attacks.
	ErrDTD = errors.New("XML contains DOCTYPE/ENTITY")
	// ErrXMLTooLarge is returned for documents larger than XMLMaxBytes.
	ErrXMLTooLarge = errors.New("XML response exceeds 8 MiB")
)

// Node is one element of a small read-only XML DOM. It mirrors the subset of
// Python's ElementTree used by the original client: direct-child and
// descendant search plus findtext. Tag holds the local name, so XML namespace
// declarations and prefixes are stripped by encoding/xml automatically.
type Node struct {
	Tag      string
	Children []*Node

	// text is the element's direct character data (not descendants').
	text string
}

// Text returns the element's own character data with surrounding whitespace
// trimmed. It never includes text belonging to child elements.
func (n *Node) Text() string {
	if n == nil {
		return ""
	}
	return strings.TrimSpace(n.text)
}

// FindText returns the text of the first node matching path, or def when no
// node matches. A matching but empty element yields "" (like ElementTree's
// findtext).
func (n *Node) FindText(path, def string) string {
	if n == nil {
		return def
	}
	matches := n.FindAll(path)
	if len(matches) == 0 {
		return def
	}
	return matches[0].Text()
}

// FindAll returns every node matching path. "tag" selects direct children,
// ".//tag" selects descendants at any depth, and "a/b" walks a chain of
// direct children. A path of "." (or "") returns the receiver.
func (n *Node) FindAll(path string) []*Node {
	if n == nil {
		return nil
	}
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
	if strings.HasPrefix(path, "./") {
		path = strings.TrimPrefix(path, "./")
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

func (n *Node) descendants(tag string) []*Node {
	var out []*Node
	var walk func(*Node)
	walk = func(cur *Node) {
		for _, child := range cur.Children {
			if child.Tag == tag {
				out = append(out, child)
			}
			walk(child)
		}
	}
	walk(n)
	return out
}

func childrenNamed(parents []*Node, tag string) []*Node {
	var out []*Node
	for _, parent := range parents {
		for _, child := range parent.Children {
			if child.Tag == tag {
				out = append(out, child)
			}
		}
	}
	return out
}

// ParseXML decodes an XML document into a Node tree. Like the Python client it
// refuses DOCTYPE/ENTITY declarations (checked in the first 4096 bytes,
// case-insensitively) and documents larger than 8 MiB.
func ParseXML(data []byte, tag string) (*Node, error) {
	label := strings.TrimSpace(tag)
	if label == "" {
		label = "XML"
	}
	if err := checkXMLSafety(data, label); err != nil {
		return nil, err
	}
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})

	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = true
	root, err := decodeNode(dec)
	if err != nil {
		return nil, fmt.Errorf("%s解析失败: %w", label, err)
	}
	return root, nil
}

func checkXMLSafety(data []byte, label string) error {
	head := data
	if len(head) > 4096 {
		head = head[:4096]
	}
	lower := bytes.ToLower(head)
	if bytes.Contains(lower, []byte("<!doctype")) || bytes.Contains(lower, []byte("<!entity")) {
		return fmt.Errorf("%s含 DOCTYPE/ENTITY, 已拒绝解析: %w", label, ErrDTD)
	}
	if len(data) > XMLMaxBytes {
		return fmt.Errorf("%s响应过大, 已拒绝解析: %w", label, ErrXMLTooLarge)
	}
	return nil
}

func decodeNode(dec *xml.Decoder) (*Node, error) {
	var root *Node
	var stack []*Node
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			node := &Node{Tag: t.Name.Local}
			if len(stack) == 0 {
				if root != nil {
					return nil, errors.New("multiple root elements")
				}
				root = node
			} else {
				parent := stack[len(stack)-1]
				parent.Children = append(parent.Children, node)
			}
			stack = append(stack, node)
		case xml.CharData:
			if len(stack) > 0 {
				stack[len(stack)-1].text += string(t)
			}
		case xml.EndElement:
			if len(stack) == 0 {
				return nil, errors.New("unexpected end element")
			}
			stack = stack[:len(stack)-1]
		}
	}
	if root == nil {
		return nil, errors.New("empty XML document")
	}
	return root, nil
}
