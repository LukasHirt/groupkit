// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package vcard

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

const twoCards = "BEGIN:VCARD\nVERSION:3.0\nFN:Ada Lovelace\nUID:ada\nEND:VCARD\n" +
	"\n" +
	"BEGIN:VCARD\nVERSION:4.0\nFN:Charles Babbage\nUID:charles\nEND:VCARD\n"

func TestSplitCards(t *testing.T) {
	objects, err := Split([]byte(twoCards))
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 2 {
		t.Fatalf("got %d objects", len(objects))
	}
	want := []struct{ data, version, uid string }{
		{"BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Ada Lovelace\r\nUID:ada\r\nEND:VCARD\r\n", "3.0", "ada"},
		{"BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Charles Babbage\r\nUID:charles\r\nEND:VCARD\r\n", "4.0", "charles"},
	}
	for i, o := range objects {
		if o.Err != nil {
			t.Errorf("object %d: Err = %v", i, o.Err)
			continue
		}
		if string(o.Data) != want[i].data {
			t.Errorf("object %d: Data = %q, want %q", i, o.Data, want[i].data)
		}
		if o.Card == nil || o.Card.Version != want[i].version || o.Card.UID() != want[i].uid {
			t.Errorf("object %d: Card = %+v", i, o.Card)
		}
	}
}

func TestSplitKeepsFoldsAndBytes(t *testing.T) {
	// The second card is already CRLF and has a folded line whose
	// continuation looks like a BEGIN marker. It has to come out unchanged.
	second := "BEGIN:VCARD\r\nVERSION:4.0\r\nNOTE:line one\r\n BEGIN:VCARD\r\n\tEND:VCARD\r\nFN:X\r\nEND:VCARD\r\n"
	in := "\xef\xbb\xbfBEGIN:VCARD\nVERSION:3.0\nFN:A\nEND:VCARD\n" + second
	objects, err := Split([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 2 {
		t.Fatalf("got %d objects: %+v", len(objects), objects)
	}
	if objects[0].Err != nil || !bytes.Equal(objects[0].Data, []byte("BEGIN:VCARD\r\nVERSION:3.0\r\nFN:A\r\nEND:VCARD\r\n")) {
		t.Errorf("first object = %q, %v", objects[0].Data, objects[0].Err)
	}
	if objects[1].Err != nil {
		t.Fatalf("second object: %v", objects[1].Err)
	}
	if string(objects[1].Data) != second {
		t.Errorf("second object = %q", objects[1].Data)
	}
	if got := objects[1].Card.Find("NOTE")[0].Raw; got != "line oneBEGIN:VCARDEND:VCARD" {
		t.Errorf("NOTE = %q", got)
	}
}

func TestSplitMarkersInAnyCase(t *testing.T) {
	in := "begin:vcard\nVERSION:4.0\nFN:A\nend: vcard\nBEGIN: VCARD\nVERSION:4.0\nFN:B\nEND:VCARD\n"
	objects, err := Split([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 2 || objects[0].Err != nil || objects[1].Err != nil {
		t.Fatalf("objects = %+v", objects)
	}
}

func TestSplitReportsBrokenCardByIndex(t *testing.T) {
	cases := map[string]struct {
		broken string
		err    error
	}{
		"old version": {"BEGIN:VCARD\nVERSION:2.1\nFN:Old\nEND:VCARD\n", ErrUnsupportedVersion},
		"bad line":    {"BEGIN:VCARD\nVERSION:4.0\nno colon here\nEND:VCARD\n", ErrInvalid},
		"no version":  {"BEGIN:VCARD\nFN:X\nEND:VCARD\n", ErrInvalid},
	}
	for name, tt := range cases {
		t.Run(name, func(t *testing.T) {
			good := "BEGIN:VCARD\nVERSION:4.0\nFN:Good\nEND:VCARD\n"
			objects, err := Split([]byte(good + tt.broken + good))
			if err != nil {
				t.Fatal(err)
			}
			if len(objects) != 3 {
				t.Fatalf("got %d objects", len(objects))
			}
			if objects[0].Err != nil || objects[2].Err != nil {
				t.Errorf("good cards failed: %v, %v", objects[0].Err, objects[2].Err)
			}
			if !errors.Is(objects[1].Err, tt.err) {
				t.Errorf("object 1: Err = %v, want %v", objects[1].Err, tt.err)
			}
			if objects[1].Card != nil {
				t.Errorf("object 1: Card = %+v", objects[1].Card)
			}
			if !strings.HasPrefix(string(objects[1].Data), "BEGIN:VCARD\r\n") {
				t.Errorf("object 1: Data = %q", objects[1].Data)
			}
		})
	}
}

func TestSplitMissingEnd(t *testing.T) {
	// A BEGIN inside a card starts a new card. The one left open is the
	// broken one, and the cards after it are fine.
	in := "BEGIN:VCARD\nVERSION:4.0\nFN:Open\nBEGIN:VCARD\nVERSION:4.0\nFN:Next\nEND:VCARD\n"
	objects, err := Split([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 2 {
		t.Fatalf("got %d objects", len(objects))
	}
	if !errors.Is(objects[0].Err, ErrInvalid) || objects[0].Card != nil {
		t.Errorf("object 0 = %+v", objects[0])
	}
	if objects[1].Err != nil || objects[1].Card.Find("FN")[0].Raw != "Next" {
		t.Errorf("object 1 = %+v", objects[1])
	}

	// The last card without END is broken too.
	objects, err = Split([]byte("BEGIN:VCARD\nVERSION:4.0\nFN:Open\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 1 || !errors.Is(objects[0].Err, ErrInvalid) {
		t.Errorf("objects = %+v", objects)
	}
}

func TestSplitTextOutsideCard(t *testing.T) {
	for name, in := range map[string]string{
		"before":  "FN:Lost\nBEGIN:VCARD\nVERSION:4.0\nEND:VCARD\n",
		"between": "BEGIN:VCARD\nVERSION:4.0\nEND:VCARD\nFN:Lost\nBEGIN:VCARD\nVERSION:4.0\nEND:VCARD\n",
		"after":   "BEGIN:VCARD\nVERSION:4.0\nEND:VCARD\nEND:VCARD\n",
	} {
		t.Run(name, func(t *testing.T) {
			objects, err := Split([]byte(in))
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
			if objects != nil {
				t.Errorf("objects = %+v", objects)
			}
		})
	}
}

func TestSplitEmpty(t *testing.T) {
	for name, in := range map[string]string{"empty": "", "blank": "\n \r\n\t\n"} {
		t.Run(name, func(t *testing.T) {
			objects, err := Split([]byte(in))
			if err != nil {
				t.Fatal(err)
			}
			if len(objects) != 0 {
				t.Errorf("objects = %+v", objects)
			}
		})
	}
}

func FuzzSplit(f *testing.F) {
	f.Add([]byte(twoCards))
	f.Add([]byte(card30))
	f.Add([]byte("BEGIN:VCARD\nVERSION:4.0\nFN:Open\nBEGIN:VCARD\nVERSION:4.0\nEND:VCARD\n"))
	f.Add([]byte("\xef\xbb\xbfbegin:vcard\r\nVERSION:3.0\r\nNOTE:a\r\n BEGIN:VCARD\r\nend:vcard\r\n\r\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		objects, err := Split(data)
		if err != nil {
			if objects != nil {
				t.Fatal("objects with an error")
			}
			return
		}
		for i, o := range objects {
			if !bytes.HasPrefix(bytes.ToUpper(o.Data), []byte("BEGIN:")) {
				t.Fatalf("object %d does not start with BEGIN: %q", i, o.Data)
			}
			c, perr := Parse(o.Data)
			if (perr == nil) != (o.Err == nil) || (c == nil) != (o.Card == nil) {
				t.Fatalf("object %d: Parse gives %v, Split gave %v", i, perr, o.Err)
			}
		}
	})
}
