// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package groupkit

import (
	"encoding/xml"
	"iter"
	"net/http"

	"github.com/LukasHirt/groupkit/internal/dav"
)

// xmlName keeps the property maps below short.
type xmlName = xml.Name

// discoveryAllow is the Allow header of the root and the principals.
const discoveryAllow = "OPTIONS, PROPFIND"

// Kinds of discovery resource.
const (
	kindRoot      = "root"
	kindPrincipal = "principal"
)

func (s *Server) principalHref(user string) string {
	return s.prefix + "/principals/" + dav.EscapeHref(user) + "/"
}

func (s *Server) rootHref() string { return s.prefix + "/" }

func fixed(p dav.Prop) dav.PropDef {
	return dav.PropDef{AllProp: true, Get: func() (dav.Prop, error) { return p, nil }}
}

func emptyReports() dav.PropDef { return fixed(dav.EmptyProp(dav.SupportedReportSet)) }

// rootResource describes the root for the current user.
func (s *Server) rootResource(user string) dav.Resource {
	return dav.Resource{Href: s.rootHref(), Props: map[xmlName]dav.PropDef{
		dav.ResourceType:         fixed(dav.TypesProp(dav.Collection)),
		dav.CurrentUserPrincipal: fixed(dav.HrefProp(dav.CurrentUserPrincipal, s.principalHref(user))),
		dav.SupportedReportSet:   emptyReports(),
	}}
}

// principalResource describes a principal. Its home-set properties come from
// the mounted protocol handlers.
func (s *Server) principalResource(user string) dav.Resource {
	href := s.principalHref(user)
	props := map[xmlName]dav.PropDef{
		dav.ResourceType:         fixed(dav.TypesProp(dav.Collection, dav.Principal)),
		dav.DisplayName:          fixed(dav.TextProp(dav.DisplayName, user)),
		dav.PrincipalURL:         fixed(dav.HrefProp(dav.PrincipalURL, href)),
		dav.CurrentUserPrincipal: fixed(dav.HrefProp(dav.CurrentUserPrincipal, href)),
		dav.Owner:                fixed(dav.HrefProp(dav.Owner, href)),
		dav.SupportedReportSet:   emptyReports(),
		dav.CurrentUserPrivilegeSet: fixed(dav.Prop{Name: dav.CurrentUserPrivilegeSet, Write: func(w *dav.Writer) {
			w.Start(dav.Privilege)
			w.Empty(dav.Read)
			w.End()
		}}),
	}
	for _, home := range s.homeSets {
		for name, def := range home(user) {
			props[name] = def
		}
	}
	return dav.Resource{Href: href, Props: props}
}

// serveDiscovery answers a request for the root or a principal. urlUser is
// the still-escaped user segment of a principal URL.
func (s *Server) serveDiscovery(w http.ResponseWriter, r *http.Request, kind, urlUser string) {
	user, err := s.currentUser(r)
	if err != nil {
		s.authError(w, r, kind, err)
		return
	}
	if user == "" {
		s.log.Error("current user returned an empty name", "method", r.Method, "resource", kind)
		internalError(w)
		return
	}
	if kind == kindPrincipal {
		name, err := dav.CleanSegment(urlUser)
		if err != nil {
			dav.WriteErr(w, err)
			return
		}
		// A principal is only readable by its own user; whether another one
		// exists is never revealed.
		if name != user {
			forbidden(w)
			return
		}
	}

	switch r.Method {
	case http.MethodOptions:
		h := w.Header()
		h.Set("DAV", s.davHeader)
		h.Set("Allow", discoveryAllow)
		w.WriteHeader(http.StatusOK)
	case "PROPFIND":
		s.propfind(w, r, kind, user)
	default:
		w.Header().Set("Allow", discoveryAllow)
		dav.WriteError(w, &dav.Error{Status: http.StatusMethodNotAllowed, Message: "method not allowed"})
	}
}

func (s *Server) propfind(w http.ResponseWriter, r *http.Request, kind, user string) {
	body, err := dav.ReadBody(w, r, dav.MaxXMLBytes)
	if err != nil {
		dav.WriteErr(w, err)
		return
	}
	req, err := dav.ParsePropfind(body)
	if err != nil {
		dav.WriteErr(w, err)
		return
	}
	depth, err := dav.ParseDepth(r.Header.Get("Depth"), dav.Depth1)
	if err != nil {
		dav.WriteErr(w, err)
		return
	}

	var responses iter.Seq[dav.Response]
	if kind == kindRoot {
		responses = func(yield func(dav.Response) bool) {
			if !yield(dav.PropFind(req, s.rootResource(user))) {
				return
			}
			if depth == dav.Depth1 {
				yield(dav.PropFind(req, s.principalResource(user)))
			}
		}
	} else {
		responses = func(yield func(dav.Response) bool) {
			yield(dav.PropFind(req, s.principalResource(user)))
		}
	}
	if err := dav.WriteMultiStatus(w, responses, ""); err != nil {
		s.log.Warn("propfind response cut short", "method", r.Method, "resource", kind, "error", err)
	}
}
