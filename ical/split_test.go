// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package ical

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden files under testdata/split")

// TestSplitGolden runs Split on every testdata/split/<case>/input.ics and
// compares the encoded objects with <case>/<index>.ics. Run the test with
// -update to rewrite those files after a deliberate change.
func TestSplitGolden(t *testing.T) {
	inputs, err := filepath.Glob(filepath.Join("testdata", "split", "*", "input.ics"))
	if err != nil {
		t.Fatal(err)
	}
	if len(inputs) == 0 {
		t.Fatal("no cases under testdata/split")
	}
	for _, in := range inputs {
		dir := filepath.Dir(in)
		t.Run(filepath.Base(dir), func(t *testing.T) {
			data, err := os.ReadFile(in)
			if err != nil {
				t.Fatal(err)
			}
			objects, err := Split(data)
			if err != nil {
				t.Fatal(err)
			}
			if *update {
				writeGolden(t, dir, objects)
			}
			goldens, err := filepath.Glob(filepath.Join(dir, "[0-9]*.ics"))
			if err != nil {
				t.Fatal(err)
			}
			if len(goldens) != len(objects) {
				t.Fatalf("got %d objects, %d golden files", len(objects), len(goldens))
			}
			for i, o := range objects {
				name := filepath.Join(dir, fmt.Sprintf("%d.ics", i))
				want, err := os.ReadFile(name)
				if err != nil {
					t.Fatal(err)
				}
				got := o.Calendar.Encode()
				if !bytes.Equal(got, want) {
					t.Errorf("object %d differs from %s\ngot:\n%s\nwant:\n%s", i, name, got, want)
				}
				if uid := goldenUID(t, want); uid != o.UID {
					t.Errorf("object %d: UID = %q, golden has %q", i, o.UID, uid)
				}
			}
		})
	}
}

func writeGolden(t *testing.T, dir string, objects []Object) {
	t.Helper()
	old, err := filepath.Glob(filepath.Join(dir, "[0-9]*.ics"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range old {
		if err := os.Remove(f); err != nil {
			t.Fatal(err)
		}
	}
	for i, o := range objects {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%d.ics", i)), o.Calendar.Encode(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// goldenUID returns the UID of the first component that is not a VTIMEZONE,
// which is the UID Split should report for the object.
func goldenUID(t *testing.T, data []byte) string {
	t.Helper()
	cal, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cal.Children {
		if c.Name == "VTIMEZONE" {
			continue
		}
		if p := c.Prop("UID"); p != nil {
			return p.Text()
		}
		return ""
	}
	return ""
}

func TestSplitErrors(t *testing.T) {
	for name, in := range map[string]string{
		"empty":     "",
		"no begin":  "VERSION:2.0\r\n",
		"unclosed":  "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:a\r\nEND:VCALENDAR\r\n",
		"bad line":  "BEGIN:VCALENDAR\r\nno colon\r\nEND:VCALENDAR\r\n",
		"not a cal": "BEGIN:VCARD\r\nEND:VCARD\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			objects, err := Split([]byte(in))
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
			if objects != nil {
				t.Errorf("objects = %v", objects)
			}
		})
	}
}

func TestSplitNoObjects(t *testing.T) {
	in := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:x\r\n" +
		"BEGIN:VTIMEZONE\r\nTZID:UTC\r\nEND:VTIMEZONE\r\nEND:VCALENDAR\r\n"
	objects, err := Split([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 0 {
		t.Errorf("got %d objects", len(objects))
	}
}

// Objects must not share memory, so a change to one does not reach another.
func TestSplitObjectsAreIndependent(t *testing.T) {
	in := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID;X=1:x\r\n" +
		"BEGIN:VTIMEZONE\r\nTZID:Z\r\nX-A:1\r\nEND:VTIMEZONE\r\n" +
		"BEGIN:VEVENT\r\nUID:a\r\nDTSTART;TZID=Z:20260101T100000\r\nEND:VEVENT\r\n" +
		"BEGIN:VEVENT\r\nUID:b\r\nDTSTART;TZID=Z:20260101T100000\r\nEND:VEVENT\r\n" +
		"END:VCALENDAR\r\n"
	objects, err := Split([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 2 {
		t.Fatalf("got %d objects", len(objects))
	}
	a, b := objects[0].Calendar, objects[1].Calendar
	a.Prop("PRODID").Params["X"][0] = "changed"
	a.Prop("PRODID").Value = "changed"
	a.Comps("VTIMEZONE")[0].Prop("X-A").Value = "changed"
	a.Comps("VTIMEZONE")[0].Props = append(a.Comps("VTIMEZONE")[0].Props, Property{Name: "X-B", Value: "2"})
	a.Comps("VEVENT")[0].Prop("DTSTART").Params["TZID"][0] = "changed"
	if strings.Contains(string(b.Encode()), "changed") || strings.Contains(string(b.Encode()), "X-B") {
		t.Errorf("object b changed with a:\n%s", b.Encode())
	}
}

// Split must take a file with more lines than Parse accepts, since the
// caller bounds the size of the file in bytes.
func TestSplitManyLines(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("BEGIN:VCALENDAR\nVERSION:2.0\nPRODID:x\n")
	const n = 40000
	for i := range n {
		fmt.Fprintf(&sb, "BEGIN:VEVENT\nUID:%d\nDTSTART:20260101T000000Z\nEND:VEVENT\n", i)
	}
	sb.WriteString("END:VCALENDAR\n")
	data := []byte(sb.String())
	if _, err := Parse(data); err == nil {
		t.Fatal("Parse accepted the file; raise n")
	}
	objects, err := Split(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != n {
		t.Errorf("got %d objects", len(objects))
	}
}
