// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package filestore_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/LukasHirt/groupkit/store"
	"github.com/LukasHirt/groupkit/store/filestore"
)

// Write an object, close the store, open the directory again and read the
// object back. The ETag is derived from the content, so it is the same after
// the restart.
func ExampleOpen() {
	dir, err := os.MkdirTemp("", "filestore-example-")
	if err != nil {
		fmt.Println(err)
		return
	}
	defer func() { _ = os.RemoveAll(dir) }()
	root := filepath.Join(dir, "data")
	ctx := context.Background()
	ref := store.CollectionRef{Owner: "alice", Kind: store.KindAddressBook, Name: "contacts"}

	st, err := filestore.Open(root)
	if err != nil {
		fmt.Println(err)
		return
	}
	if _, err := st.CreateCollection(ctx, ref, store.Props{DisplayName: "Contacts"}); err != nil {
		fmt.Println(err)
		return
	}
	card := []byte("BEGIN:VCARD\r\nVERSION:4.0\r\nUID:ada-1\r\nFN:Ada Lovelace\r\nEND:VCARD\r\n")
	obj, err := st.PutObject(ctx, ref, "ada.vcf", card, store.PutOptions{UID: "ada-1"})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println("written:", obj.ETag)
	if err := st.Close(); err != nil {
		fmt.Println(err)
		return
	}

	st, err = filestore.Open(root)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer func() { _ = st.Close() }()
	obj, err = st.GetObject(ctx, ref, "ada.vcf")
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println("after reopening:", obj.ETag)
	// Output:
	// written: 2db73258e38f89fca1e0c23ffb35e398
	// after reopening: 2db73258e38f89fca1e0c23ffb35e398
}
