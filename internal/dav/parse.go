// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package dav

import (
	"bytes"
	"encoding/xml"
	"errors"
	"slices"
)

// maxProps caps the property names or updates one request body may carry.
const maxProps = 1000

// PropfindRequest is a parsed PROPFIND body. Exactly one of AllProp, PropName
// or a non-empty Props selects the mode, except that with AllProp Props holds
// the names from the optional DAV:include element.
type PropfindRequest struct {
	AllProp  bool
	PropName bool
	Props    []xml.Name
}

// PropUpdate is one property from a PROPPATCH or extended MKCOL body. Node is
// the property element itself, so the caller reads its text or children.
type PropUpdate struct {
	Name   xml.Name
	Remove bool
	Node   *Node
}

// CollectionRequest is a parsed extended MKCOL or MKCALENDAR body. Props holds
// every property to set in document order; ResourceTypes holds the children
// of DAV:resourcetype, which Props also lists.
type CollectionRequest struct {
	Props         []PropUpdate
	ResourceTypes []xml.Name
}

// parseBody parses a request body into a tree. Every failure is a 400 whose
// message is the ErrXML text, never the input.
func parseBody(body []byte) (*Node, error) {
	root, err := ParseXML(body)
	if err != nil {
		if errors.Is(err, ErrXML) {
			return nil, BadRequest(ErrXML.Error())
		}
		return nil, BadRequest("malformed XML")
	}
	return root, nil
}

// blank reports whether body holds only white space.
func blank(body []byte) bool { return len(bytes.TrimSpace(body)) == 0 }

// ParsePropfind parses a PROPFIND body. An empty body means allprop. Every
// error is a 400 *Error.
func ParsePropfind(body []byte) (PropfindRequest, error) {
	if blank(body) {
		return PropfindRequest{AllProp: true}, nil
	}
	root, err := parseBody(body)
	if err != nil {
		return PropfindRequest{}, err
	}
	return propfindNode(root)
}

func propfindNode(root *Node) (PropfindRequest, error) {
	if root == nil || root.Name != PropFindEl {
		return PropfindRequest{}, BadRequest("expected propfind")
	}
	all, names, prop := root.Kids(AllProp), root.Kids(PropName), root.Kids(PropEl)
	if len(all)+len(names)+len(prop) != 1 {
		return PropfindRequest{}, BadRequest("propfind needs exactly one of allprop, propname, prop")
	}
	var req PropfindRequest
	switch {
	case len(all) == 1:
		req.AllProp = true
		if inc := root.Child(Include); inc != nil {
			props, err := PropNames(inc.Children)
			if err != nil {
				return PropfindRequest{}, err
			}
			req.Props = props
		}
	case len(names) == 1:
		req.PropName = true
	default:
		props, err := PropNames(prop[0].Children)
		if err != nil {
			return PropfindRequest{}, err
		}
		req.Props = props
	}
	return req, nil
}

// PropNames lists the names of nodes in order, without duplicates.
func PropNames(nodes []*Node) ([]xml.Name, error) {
	var out []xml.Name
	seen := make(map[xml.Name]struct{}, len(nodes))
	for _, n := range nodes {
		if _, dup := seen[n.Name]; dup {
			continue
		}
		seen[n.Name] = struct{}{}
		if len(out) >= maxProps {
			return nil, BadRequest("too many properties")
		}
		out = append(out, n.Name)
	}
	return out, nil
}

// ParsePropPatch parses a PROPPATCH body into its updates in document order.
// Every error is a 400 *Error.
func ParsePropPatch(body []byte) ([]PropUpdate, error) {
	root, err := parseBody(body)
	if err != nil {
		return nil, err
	}
	return propPatchNode(root)
}

func propPatchNode(root *Node) ([]PropUpdate, error) {
	if root == nil || root.Name != PropertyUpdate {
		return nil, BadRequest("expected propertyupdate")
	}
	var out []PropUpdate
	for _, c := range root.Children {
		if c.Name != Set && c.Name != Remove {
			continue
		}
		var err error
		out, err = appendUpdates(out, c, c.Name == Remove)
		if err != nil {
			return nil, err
		}
	}
	if len(out) == 0 {
		return nil, BadRequest("propertyupdate has no properties")
	}
	return out, nil
}

// appendUpdates adds the properties of a set or remove block. The block must
// hold a prop element with at least one property.
func appendUpdates(out []PropUpdate, block *Node, remove bool) ([]PropUpdate, error) {
	prop := block.Child(PropEl)
	if prop == nil || len(prop.Children) == 0 {
		return nil, BadRequest("set or remove without properties")
	}
	for _, p := range prop.Children {
		if len(out) >= maxProps {
			return nil, BadRequest("too many properties")
		}
		out = append(out, PropUpdate{Name: p.Name, Remove: remove, Node: p})
	}
	return out, nil
}

// ParseMkcol parses an extended MKCOL body. An empty body is a plain request
// and gives the zero value. Every error is a 400 *Error.
func ParseMkcol(body []byte) (CollectionRequest, error) {
	return parseCollection(body, MkCol)
}

// ParseMkcalendar parses a MKCALENDAR body. An empty body gives the zero
// value. Every error is a 400 *Error.
func ParseMkcalendar(body []byte) (CollectionRequest, error) {
	return parseCollection(body, MkCalendar)
}

func parseCollection(body []byte, rootName xml.Name) (CollectionRequest, error) {
	if blank(body) {
		return CollectionRequest{}, nil
	}
	root, err := parseBody(body)
	if err != nil {
		return CollectionRequest{}, err
	}
	return collectionNode(root, rootName)
}

func collectionNode(root *Node, rootName xml.Name) (CollectionRequest, error) {
	if root == nil || root.Name != rootName {
		return CollectionRequest{}, BadRequest("unexpected root element")
	}
	if root.Child(Remove) != nil {
		return CollectionRequest{}, BadRequest("remove is not allowed here")
	}
	var req CollectionRequest
	for _, set := range root.Kids(Set) {
		var err error
		req.Props, err = appendUpdates(req.Props, set, false)
		if err != nil {
			return CollectionRequest{}, err
		}
	}
	for _, p := range req.Props {
		if p.Name != ResourceType {
			continue
		}
		for _, t := range p.Node.Children {
			if !slices.Contains(req.ResourceTypes, t.Name) {
				req.ResourceTypes = append(req.ResourceTypes, t.Name)
			}
		}
	}
	return req, nil
}
