// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package groupkit_test

import (
	"context"
	"strings"
	"testing"

	"github.com/LukasHirt/groupkit/store"
	"github.com/LukasHirt/groupkit/store/memory"
	"github.com/LukasHirt/groupkit/store/storetest"
)

// TestClientSeesReadOnlyCollections runs the go-webdav clients against a
// server whose store grants read access only: listing and reading work, and
// every write comes back as a 403.
func TestClientSeesReadOnlyCollections(t *testing.T) {
	mem := memory.New()
	ctx := context.Background()
	calRef := store.CollectionRef{Owner: "alice", Kind: store.KindCalendar, Name: "work"}
	bookRef := store.CollectionRef{Owner: "alice", Kind: store.KindAddressBook, Name: "contacts"}
	for _, ref := range []store.CollectionRef{calRef, bookRef} {
		if _, err := mem.CreateCollection(ctx, ref, store.Props{DisplayName: ref.Name}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := mem.PutObject(ctx, calRef, "lunch.ics", []byte(utcEvent("lunch-1", "20260501T120000Z", "Lunch")), store.PutOptions{UID: "lunch-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.PutObject(ctx, bookRef, "ada.vcf", []byte("BEGIN:VCARD\r\nVERSION:4.0\r\nUID:ada\r\nFN:Ada\r\nEND:VCARD\r\n"), store.PutOptions{UID: "ada"}); err != nil {
		t.Fatal(err)
	}
	ts := startBasicServer(t, storetest.WithAccess(mem, store.AccessRead))
	ctx = ctxTimeout(t)

	forbidden := func(t *testing.T, what string, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), "403") {
			t.Errorf("%s: %v, want a 403", what, err)
		}
	}

	t.Run("calendar", func(t *testing.T) {
		c := calClient(t, ts.URL+"/dav/", "alice")
		_, cals := discoverCalendars(t, ctx, c)
		if len(cals) != 1 {
			t.Fatalf("calendars = %+v", cals)
		}
		work := cals[0].Path
		objs, err := c.QueryCalendar(ctx, work, timeQuery(at(2026, 1, 1, 0, 0), at(2027, 1, 1, 0, 0)))
		if err != nil || len(objs) != 1 {
			t.Fatalf("QueryCalendar: %d objects, %v", len(objs), err)
		}
		if _, err := c.GetCalendarObject(ctx, work+"lunch.ics"); err != nil {
			t.Errorf("GetCalendarObject: %v", err)
		}
		_, err = c.PutCalendarObject(ctx, work+"new.ics", decodeCal(t, utcEvent("new-1", "20260502T120000Z", "New")))
		forbidden(t, "PutCalendarObject", err)
		forbidden(t, "RemoveAll(object)", c.RemoveAll(ctx, work+"lunch.ics"))
		forbidden(t, "RemoveAll(calendar)", c.RemoveAll(ctx, work))
		if _, err := c.GetCalendarObject(ctx, work+"lunch.ics"); err != nil {
			t.Errorf("the object is gone after a refused delete: %v", err)
		}
	})

	t.Run("address book", func(t *testing.T) {
		c := cardClient(t, ts.URL+"/dav/", "alice")
		_, books := discover(t, ctx, c)
		if len(books) != 1 {
			t.Fatalf("address books = %+v", books)
		}
		contacts := books[0].Path
		if _, err := c.GetAddressObject(ctx, contacts+"ada.vcf"); err != nil {
			t.Errorf("GetAddressObject: %v", err)
		}
		_, err := c.PutAddressObject(ctx, contacts+"new.vcf", testCard("new", "New"))
		forbidden(t, "PutAddressObject", err)
		forbidden(t, "RemoveAll(object)", c.RemoveAll(ctx, contacts+"ada.vcf"))
		forbidden(t, "RemoveAll(address book)", c.RemoveAll(ctx, contacts))
	})
}
