// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package carddav

import (
	"context"
	"encoding/xml"
	"strconv"

	"github.com/LukasHirt/groupkit/internal/dav"
	"github.com/LukasHirt/groupkit/store"
)

// vcardContentType is the content type of contact objects.
const vcardContentType = "text/vcard; charset=utf-8"

// Privileges granted per kind of resource. The engine does no per-object
// access control, so these describe what the endpoint supports.
var (
	homePrivileges   = []xml.Name{dav.Read, dav.Bind}
	bookPrivileges   = []xml.Name{dav.Read, dav.Write, dav.WriteProperties, dav.WriteContent, dav.Bind, dav.Unbind}
	objectPrivileges = []xml.Name{dav.Read, dav.Write, dav.WriteContent}
)

// bookReports are the reports an address book supports.
var bookReports = []xml.Name{dav.AddressbookQuery, dav.AddressbookMultiget, dav.SyncCollection}

// principalHref returns the escaped URL of a user's principal resource.
func (h *Handler) principalHref(user string) string {
	return h.prefix + "/principals/" + dav.EscapeHref(user) + "/"
}

// bookHref returns the escaped URL of an address book, with a trailing slash.
func (h *Handler) bookHref(user, book string) string {
	return h.prefix + "/addressbooks/" + dav.EscapeHref(user, book) + "/"
}

// objectHref returns the escaped URL of an object.
func (h *Handler) objectHref(user, book, name string) string {
	return h.prefix + "/addressbooks/" + dav.EscapeHref(user, book, name)
}

// fixed is a property whose value is already known.
func fixed(p dav.Prop) dav.PropDef {
	return dav.PropDef{AllProp: true, Get: func() (dav.Prop, error) { return p, nil }}
}

func text(name xml.Name, s string) dav.PropDef { return fixed(dav.TextProp(name, s)) }

func privilegeSet(privs []xml.Name) dav.Prop {
	return dav.Prop{Name: dav.CurrentUserPrivilegeSet, Write: func(w *dav.Writer) {
		for _, p := range privs {
			w.Start(dav.Privilege)
			w.Empty(p)
			w.End()
		}
	}}
}

func reportSet(reports []xml.Name) dav.Prop {
	if len(reports) == 0 {
		return dav.EmptyProp(dav.SupportedReportSet)
	}
	return dav.Prop{Name: dav.SupportedReportSet, Write: func(w *dav.Writer) {
		for _, r := range reports {
			w.Start(dav.SupportedReport)
			w.Start(dav.Report)
			w.Empty(r)
			w.End()
			w.End()
		}
	}}
}

// commonProps are the properties every resource has.
func (h *Handler) commonProps(user string, privs, reports []xml.Name, types ...xml.Name) map[xml.Name]dav.PropDef {
	principal := h.principalHref(user)
	return map[xml.Name]dav.PropDef{
		dav.ResourceType:            fixed(dav.TypesProp(types...)),
		dav.CurrentUserPrincipal:    fixed(dav.HrefProp(dav.CurrentUserPrincipal, principal)),
		dav.Owner:                   fixed(dav.HrefProp(dav.Owner, principal)),
		dav.CurrentUserPrivilegeSet: fixed(privilegeSet(privs)),
		dav.SupportedReportSet:      fixed(reportSet(reports)),
	}
}

// homeResource describes a user's address book home. The URL user is the
// display name.
func (h *Handler) homeResource(user string) dav.Resource {
	props := h.commonProps(user, homePrivileges, nil, dav.Collection)
	props[dav.DisplayName] = text(dav.DisplayName, user)
	return dav.Resource{Href: h.HomeHref(user), Props: props}
}

// bookResource describes an address book.
func (h *Handler) bookResource(user string, c store.Collection) dav.Resource {
	props := h.commonProps(user, bookPrivileges, bookReports, dav.Collection, dav.Addressbook)
	name := c.Props.DisplayName
	if name == "" {
		name = c.Ref.Name
	}
	props[dav.DisplayName] = text(dav.DisplayName, name)
	props[dav.GetCTag] = text(dav.GetCTag, strconv.FormatUint(c.Revision, 10))
	props[dav.SyncToken] = text(dav.SyncToken, syncToken(c))
	props[dav.AddressbookDescription] = text(dav.AddressbookDescription, c.Props.Description)
	props[dav.CRMaxResourceSize] = text(dav.CRMaxResourceSize, strconv.FormatInt(h.maxSize, 10))
	props[dav.SupportedAddressData] = fixed(dav.Prop{Name: dav.SupportedAddressData, Write: func(w *dav.Writer) {
		for _, v := range []string{"3.0", "4.0"} {
			w.Start(dav.CRAddressDataType,
				dav.Attr{Name: "content-type", Value: "text/vcard"},
				dav.Attr{Name: "version", Value: v})
			w.End()
		}
	}})
	return dav.Resource{Href: h.bookHref(user, c.Ref.Name), Props: props}
}

// objectResource describes a contact object from its listing entry. data
// fetches the object's bytes; it is only called when address-data is asked
// for by name, so a listing reads no object data.
func (h *Handler) objectResource(user, book string, o store.Object, data func() ([]byte, error)) dav.Resource {
	props := h.commonProps(user, objectPrivileges, nil)
	props[dav.DisplayName] = text(dav.DisplayName, o.Name)
	props[dav.GetETag] = text(dav.GetETag, dav.QuoteETag(o.ETag))
	props[dav.GetContentType] = text(dav.GetContentType, vcardContentType)
	props[dav.GetContentLength] = text(dav.GetContentLength, strconv.FormatInt(o.Size, 10))
	props[dav.GetLastModified] = text(dav.GetLastModified, dav.HTTPDate(o.ModTime))
	props[dav.AddressData] = dav.PropDef{ // AllProp is false: it is heavy
		Get: func() (dav.Prop, error) {
			b, err := data()
			if err != nil {
				return dav.Prop{}, err
			}
			s := string(b)
			return dav.Prop{Name: dav.AddressData, Write: func(w *dav.Writer) { w.Text(s) }}, nil
		},
	}
	return dav.Resource{Href: h.objectHref(user, book, o.Name), Props: props}
}

// objectResponse builds the PROPFIND response for one object of a book. The
// object's data is read from the store only if the request names address-data.
func (h *Handler) objectResponse(ctx context.Context, owner, user, book string, o store.Object, req dav.PropfindRequest) dav.Response {
	ref := store.CollectionRef{Owner: owner, Kind: store.KindAddressBook, Name: book}
	res := h.objectResource(user, book, o, func() ([]byte, error) {
		full, err := h.store.GetObject(ctx, ref, o.Name)
		if err != nil {
			h.log.Warn("cannot read object", "collection", book, "error", err)
			return nil, err
		}
		return full.Data, nil
	})
	return dav.PropFind(req, res)
}
