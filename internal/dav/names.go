// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package dav

import "encoding/xml"

// Namespace URIs. The conventional prefixes used when writing XML are
// D for DAV:, C for CalDAV, CR for CardDAV, CS for the calendarserver.org
// namespace and A for the Apple iCal namespace.
const (
	NSDAV     = "DAV:"
	NSCalDAV  = "urn:ietf:params:xml:ns:caldav"
	NSCardDAV = "urn:ietf:params:xml:ns:carddav"
	NSCS      = "http://calendarserver.org/ns/"
	NSApple   = "http://apple.com/ns/ical/"
)

// N builds an expanded XML name.
func N(space, local string) xml.Name { return xml.Name{Space: space, Local: local} }

// Naming rule: a variable is the element's local name in Go style
// (calendar-data becomes CalendarData). DAV: names carry no prefix, except
// that a name whose plain identifier is a Go type or function in this package
// has an El suffix (ResponseEl, PropstatEl, PropEl, PropFindEl, ErrorEl). In
// the CalDAV and CardDAV namespaces, the query and filter elements (Filter,
// CompFilter, TextMatch and the like) and any local name that exists in more
// than one namespace carry a Cal or CR prefix (CalFilter, CRFilter, CalProp,
// CRProp, CalMaxResourceSize, CRMaxResourceSize). Every other name keeps the
// plain form (CalendarData, AddressData, GetCTag).

// DAV: names.
var (
	Multistatus             = N(NSDAV, "multistatus")
	ResponseEl              = N(NSDAV, "response")
	Href                    = N(NSDAV, "href")
	PropstatEl              = N(NSDAV, "propstat")
	PropEl                  = N(NSDAV, "prop")
	Status                  = N(NSDAV, "status")
	ErrorEl                 = N(NSDAV, "error")
	ResourceType            = N(NSDAV, "resourcetype")
	Collection              = N(NSDAV, "collection")
	DisplayName             = N(NSDAV, "displayname")
	GetETag                 = N(NSDAV, "getetag")
	GetContentType          = N(NSDAV, "getcontenttype")
	GetContentLength        = N(NSDAV, "getcontentlength")
	GetLastModified         = N(NSDAV, "getlastmodified")
	CreationDate            = N(NSDAV, "creationdate")
	SyncToken               = N(NSDAV, "sync-token")
	SyncCollection          = N(NSDAV, "sync-collection")
	SyncLevel               = N(NSDAV, "sync-level")
	Limit                   = N(NSDAV, "limit")
	NResults                = N(NSDAV, "nresults")
	Owner                   = N(NSDAV, "owner")
	CurrentUserPrincipal    = N(NSDAV, "current-user-principal")
	PrincipalURL            = N(NSDAV, "principal-URL")
	CurrentUserPrivilegeSet = N(NSDAV, "current-user-privilege-set")
	Principal               = N(NSDAV, "principal")
	WriteProperties         = N(NSDAV, "write-properties")
	WriteContent            = N(NSDAV, "write-content")
	Bind                    = N(NSDAV, "bind")
	Unbind                  = N(NSDAV, "unbind")
	Privilege               = N(NSDAV, "privilege")
	Read                    = N(NSDAV, "read")
	Write                   = N(NSDAV, "write")
	All                     = N(NSDAV, "all")
	SupportedReportSet      = N(NSDAV, "supported-report-set")
	SupportedReport         = N(NSDAV, "supported-report")
	Report                  = N(NSDAV, "report")
	AllProp                 = N(NSDAV, "allprop")
	PropName                = N(NSDAV, "propname")
	Include                 = N(NSDAV, "include")
	PropertyUpdate          = N(NSDAV, "propertyupdate")
	Set                     = N(NSDAV, "set")
	Remove                  = N(NSDAV, "remove")
	MkCol                   = N(NSDAV, "mkcol")
	PropFindEl              = N(NSDAV, "propfind")
	PrincipalCollectionSet  = N(NSDAV, "principal-collection-set")
	ValidResourceType       = N(NSDAV, "valid-resourcetype")
)

// CalDAV names.
var (
	Calendar                      = N(NSCalDAV, "calendar")
	CalendarHomeSet               = N(NSCalDAV, "calendar-home-set")
	CalendarData                  = N(NSCalDAV, "calendar-data")
	CalendarQuery                 = N(NSCalDAV, "calendar-query")
	CalendarMultiget              = N(NSCalDAV, "calendar-multiget")
	CalFilter                     = N(NSCalDAV, "filter")
	CalCompFilter                 = N(NSCalDAV, "comp-filter")
	CalPropFilter                 = N(NSCalDAV, "prop-filter")
	CalParamFilter                = N(NSCalDAV, "param-filter")
	CalTimeRange                  = N(NSCalDAV, "time-range")
	CalTextMatch                  = N(NSCalDAV, "text-match")
	CalIsNotDefined               = N(NSCalDAV, "is-not-defined")
	CalComp                       = N(NSCalDAV, "comp")
	CalProp                       = N(NSCalDAV, "prop")
	SupportedCalendarComponentSet = N(NSCalDAV, "supported-calendar-component-set")
	SupportedCalendarData         = N(NSCalDAV, "supported-calendar-data")
	SupportedCollationSet         = N(NSCalDAV, "supported-collation-set")
	CalendarDescription           = N(NSCalDAV, "calendar-description")
	CalendarTimezone              = N(NSCalDAV, "calendar-timezone")
	MkCalendar                    = N(NSCalDAV, "mkcalendar")
	CalMaxResourceSize            = N(NSCalDAV, "max-resource-size")
	MinDateTime                   = N(NSCalDAV, "min-date-time")
	MaxDateTime                   = N(NSCalDAV, "max-date-time")
	MaxInstances                  = N(NSCalDAV, "max-instances")
	MaxAttendeesPerInstance       = N(NSCalDAV, "max-attendees-per-instance")
	CalValidFilter                = N(NSCalDAV, "valid-filter")
	CalSupportedFilter            = N(NSCalDAV, "supported-filter")
	CalExpand                     = N(NSCalDAV, "expand")
	CalLimitRecurrenceSet         = N(NSCalDAV, "limit-recurrence-set")
	CalTimezone                   = N(NSCalDAV, "timezone")
)

// CardDAV names.
var (
	Addressbook             = N(NSCardDAV, "addressbook")
	AddressbookHomeSet      = N(NSCardDAV, "addressbook-home-set")
	AddressData             = N(NSCardDAV, "address-data")
	CRLimit                 = N(NSCardDAV, "limit")
	CRNResults              = N(NSCardDAV, "nresults")
	CRProp                  = N(NSCardDAV, "prop")
	AddressbookQuery        = N(NSCardDAV, "addressbook-query")
	AddressbookMultiget     = N(NSCardDAV, "addressbook-multiget")
	SupportedAddressData    = N(NSCardDAV, "supported-address-data")
	AddressbookDescription  = N(NSCardDAV, "addressbook-description")
	CRMaxResourceSize       = N(NSCardDAV, "max-resource-size")
	CRFilter                = N(NSCardDAV, "filter")
	CRPropFilter            = N(NSCardDAV, "prop-filter")
	CRParamFilter           = N(NSCardDAV, "param-filter")
	CRTextMatch             = N(NSCardDAV, "text-match")
	CRIsNotDefined          = N(NSCardDAV, "is-not-defined")
	CRValidFilter           = N(NSCardDAV, "valid-filter")
	CRSupportedFilter       = N(NSCardDAV, "supported-filter")
	CRAddressDataType       = N(NSCardDAV, "address-data-type")
	CRSupportedCollationSet = N(NSCardDAV, "supported-collation-set")
)

// Calendar Server and Apple names.
var (
	GetCTag       = N(NSCS, "getctag")
	CalendarColor = N(NSApple, "calendar-color")
	CalendarOrder = N(NSApple, "calendar-order")
)
