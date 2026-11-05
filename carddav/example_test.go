// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package carddav_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"

	"github.com/LukasHirt/groupkit/carddav"
	"github.com/LukasHirt/groupkit/store"
	"github.com/LukasHirt/groupkit/store/memory"
)

// Mount only this protocol, for a program that does its own discovery. Resolve
// maps the user in the URL to the store owner. Here every URL user owns the
// data filed under the same name.
func ExampleNew() {
	st := memory.New()
	ref := store.CollectionRef{Owner: "alice", Kind: store.KindAddressBook, Name: "contacts"}
	if _, err := st.CreateCollection(context.Background(), ref, store.Props{DisplayName: "Default"}); err != nil {
		fmt.Println(err)
		return
	}
	h, err := carddav.New(carddav.Config{
		Store:  st,
		Prefix: "/dav",
		Resolve: func(r *http.Request, urlUser string) (string, error) {
			return urlUser, nil
		},
	})
	if err != nil {
		fmt.Println(err)
		return
	}

	req := httptest.NewRequest("PROPFIND", "/dav/addressbooks/alice/", nil)
	req.Header.Set("Depth", "0")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	fmt.Println(rec.Code)
	// Output:
	// 207
}
