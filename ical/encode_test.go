// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package ical

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// canonical is what Encode writes for its own parse: CRLF endings, parameters
// in name order, quotes only where a value needs them, and no line over 75
// octets.
const canonical = "BEGIN:VCALENDAR\r\n" +
	"VERSION:2.0\r\n" +
	"PRODID:-//x//y//EN\r\n" +
	"BEGIN:VTIMEZONE\r\n" +
	"TZID:Europe/Prague\r\n" +
	"BEGIN:STANDARD\r\n" +
	"DTSTART:19701025T030000\r\n" +
	"TZOFFSETFROM:+0200\r\n" +
	"TZOFFSETTO:+0100\r\n" +
	"END:STANDARD\r\n" +
	"END:VTIMEZONE\r\n" +
	"BEGIN:VEVENT\r\n" +
	"UID:abc\r\n" +
	"DTSTART;TZID=Europe/Prague:20260101T100000\r\n" +
	"ATTENDEE;CN=\"Doe, John\";ROLE=REQ-PARTICIPANT,CHAIR:mailto:a@example.org\r\n" +
	"SUMMARY:Team\\, meeting\r\n" +
	"X-EMPTY:\r\n" +
	"BEGIN:VALARM\r\n" +
	"ACTION:DISPLAY\r\n" +
	"TRIGGER:-PT15M\r\n" +
	"END:VALARM\r\n" +
	"END:VEVENT\r\n" +
	"END:VCALENDAR\r\n"

func TestEncodeCanonical(t *testing.T) {
	cal, err := Parse([]byte(canonical))
	if err != nil {
		t.Fatal(err)
	}
	if got := cal.Encode(); string(got) != canonical {
		t.Errorf("Encode =\n%s\nwant\n%s", got, canonical)
	}
}

func TestEncodeNormalises(t *testing.T) {
	in := "begin:vcalendar\n" +
		"version:2.0\n" +
		"prodid:-//x//y//EN\n" +
		"BEGIN: VEVENT\n" +
		"uid:abc\n" +
		"ATTENDEE;ROLE=REQ-PARTICIPANT;cn=\"Doe, John\";ROLE=CHAIR:mailto:a@example.org\n" +
		"SUMMARY:Long\n" +
		"  summary\n" +
		"END:VEVENT\n" +
		"END:VCALENDAR\n"
	want := "BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"PRODID:-//x//y//EN\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:abc\r\n" +
		"ATTENDEE;CN=\"Doe, John\";ROLE=REQ-PARTICIPANT,CHAIR:mailto:a@example.org\r\n" +
		"SUMMARY:Long summary\r\n" +
		"END:VEVENT\r\n" +
		"END:VCALENDAR\r\n"
	cal, err := Parse([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if got := cal.Encode(); string(got) != want {
		t.Errorf("Encode =\n%s\nwant\n%s", got, want)
	}
}

func TestEncodeFolds(t *testing.T) {
	// 70 ASCII bytes, then multi-byte characters across the 75-octet mark.
	value := strings.Repeat("a", 70) + "ééé" + strings.Repeat("b", 80)
	c := &Component{Name: "VCALENDAR", Props: []Property{{Name: "DESCRIPTION", Value: value}}}
	out := c.Encode()
	lines := bytes.Split(bytes.TrimSuffix(out, []byte("\r\n")), []byte("\r\n"))
	for i, l := range lines {
		if len(l) > 75 {
			t.Errorf("line %d is %d octets: %q", i, len(l), l)
		}
		// Line 0 is BEGIN, line 1 starts DESCRIPTION, the last line is END.
		if i > 1 && i < len(lines)-1 && l[0] != ' ' {
			t.Errorf("line %d does not continue the fold: %q", i, l)
		}
		if !utf8.Valid(l) {
			t.Errorf("line %d splits a character: %q", i, l)
		}
	}
	back, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	if got := back.Prop("DESCRIPTION").Value; got != value {
		t.Errorf("unfolded value = %q", got)
	}
}

func TestEncodeFoldsInvalidUTF8(t *testing.T) {
	// A run of continuation bytes has no character boundary to back off to,
	// so the fold happens at the limit.
	value := strings.Repeat("\x80", 200)
	c := &Component{Name: "VCALENDAR", Props: []Property{{Name: "X-BIN", Value: value}}}
	out := c.Encode()
	for i, l := range bytes.Split(out, []byte("\r\n")) {
		if len(l) > 75 {
			t.Errorf("line %d is %d octets", i, len(l))
		}
	}
	back, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	if got := back.Prop("X-BIN").Value; got != value {
		t.Errorf("unfolded value differs")
	}
}

func TestEncodeParams(t *testing.T) {
	cases := []struct {
		name   string
		params map[string][]string
		want   string
	}{
		{"none", nil, "X:v\r\n"},
		{"plain", map[string][]string{"A": {"x"}}, "X;A=x:v\r\n"},
		{"sorted", map[string][]string{"B": {"2"}, "A": {"1"}}, "X;A=1;B=2:v\r\n"},
		{"multi", map[string][]string{"A": {"1", "2"}}, "X;A=1,2:v\r\n"},
		{"empty value", map[string][]string{"A": {""}}, "X;A=:v\r\n"},
		{"no values", map[string][]string{"A": nil}, "X;A=:v\r\n"},
		{"comma", map[string][]string{"A": {"a,b"}}, "X;A=\"a,b\":v\r\n"},
		{"semicolon", map[string][]string{"A": {"a;b"}}, "X;A=\"a;b\":v\r\n"},
		{"colon", map[string][]string{"A": {"a:b"}}, "X;A=\"a:b\":v\r\n"},
		{"quote dropped", map[string][]string{"A": {`say "hi"`}}, "X;A=say hi:v\r\n"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			c := &Component{Name: "VCALENDAR", Props: []Property{{Name: "X", Params: tt.params, Value: "v"}}}
			got := string(c.Encode())
			want := "BEGIN:VCALENDAR\r\n" + tt.want + "END:VCALENDAR\r\n"
			if got != want {
				t.Errorf("Encode = %q, want %q", got, want)
			}
		})
	}
}

func TestEncodeRoundTrip(t *testing.T) {
	for name, in := range map[string]string{
		"nested":    nested,
		"canonical": canonical,
	} {
		t.Run(name, func(t *testing.T) {
			checkRoundTrip(t, []byte(in))
		})
	}
}

// checkRoundTrip parses data, encodes it, parses the result and expects the
// same tree. A second encoding must equal the first.
func checkRoundTrip(t *testing.T, data []byte) {
	t.Helper()
	cal, err := Parse(data)
	if err != nil {
		t.Skip(err)
	}
	out := cal.Encode()
	back, err := Parse(out)
	if err != nil {
		t.Fatalf("encoded data does not parse: %v\n%s", err, out)
	}
	if !reflect.DeepEqual(cal, back) {
		t.Fatalf("round trip changed the tree\nin:\n%s\nout:\n%s\n%#v\n%#v", data, out, cal, back)
	}
	if again := back.Encode(); !bytes.Equal(out, again) {
		t.Fatalf("second encoding differs\n%s\n%s", out, again)
	}
}

func FuzzEncodeRoundTrip(f *testing.F) {
	f.Add([]byte(nested))
	f.Add([]byte(canonical))
	f.Add([]byte("BEGIN:VCALENDAR\r\nX;A=\"a,b\",c;B=:" + strings.Repeat("é", 60) + "\r\nEND:VCALENDAR\r\n"))
	f.Add([]byte("BEGIN:VCALENDAR\nX:\x80\x80\xff\nEND:VCALENDAR\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		checkRoundTrip(t, data)
	})
}
