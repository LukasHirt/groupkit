// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package vcard

import (
	"errors"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

const card30 = "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Ada Lovelace\r\nN:Lovelace;Ada;;;\r\nUID:urn:uuid:1234\r\n" +
	"EMAIL;TYPE=work:ada@example.org\r\nitem1.TEL;TYPE=cell:+1 555 0100\r\n" +
	"NOTE:first part\r\n  second part\r\nEND:VCARD\r\n"

const card40 = "BEGIN:VCARD\nVERSION:4.0\nFN:Ada Lovelace\nN:Lovelace;Ada;;;\nUID:urn:uuid:1234\n" +
	"EMAIL;TYPE=work:ada@example.org\nitem1.TEL;TYPE=cell:+1 555 0100\n" +
	"NOTE:first part\n  second part\nEND:VCARD\n"

func TestParseValid(t *testing.T) {
	tests := []struct{ name, in, version string }{
		{"3.0 CRLF", card30, "3.0"},
		{"4.0 LF", card40, "4.0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := Parse([]byte(tt.in))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if c.Version != tt.version {
				t.Fatalf("Version = %q", c.Version)
			}
			if len(c.Props) != 6 {
				t.Fatalf("got %d props: %#v", len(c.Props), c.Props)
			}
			if got := c.UID(); got != "urn:uuid:1234" {
				t.Fatalf("UID = %q", got)
			}
			email := c.Find("email")[0]
			if want := map[string][]string{"TYPE": {"work"}}; !reflect.DeepEqual(email.Params, want) {
				t.Fatalf("params = %#v", email.Params)
			}
			tel := c.Find("TEL")[0]
			if tel.Group != "ITEM1" || tel.Name != "TEL" {
				t.Fatalf("tel = %#v", tel)
			}
			if got := c.Find("NOTE")[0].Raw; got != "first part second part" {
				t.Fatalf("note = %q", got)
			}
		})
	}
}

func TestMergeRepeatedParams(t *testing.T) {
	c, err := Parse([]byte("BEGIN:VCARD\nVERSION:4.0\nTEL;TYPE=work;TYPE=voice,pref:1\nEND:VCARD\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{"TYPE": {"work", "voice", "pref"}}
	if got := c.Props[0].Params; !reflect.DeepEqual(got, want) {
		t.Fatalf("params = %#v", got)
	}
}

func TestValidateReturnsUID(t *testing.T) {
	uid, err := Validate([]byte(card30))
	if err != nil || uid != "urn:uuid:1234" {
		t.Fatalf("uid = %q, err = %v", uid, err)
	}
	uid, err = Validate([]byte("BEGIN:VCARD\r\nVERSION:4.0\r\nFN:X\r\nEND:VCARD\r\n"))
	if err != nil || uid != "" {
		t.Fatalf("uid = %q, err = %v", uid, err)
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want error
	}{
		{"missing BEGIN", "VERSION:4.0\r\nFN:X\r\nEND:VCARD\r\n", ErrInvalid},
		{"missing END", "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:X\r\n", ErrInvalid},
		{"END before BEGIN", "END:VCARD\r\nBEGIN:VCARD\r\nVERSION:4.0\r\n", ErrInvalid},
		{"nested BEGIN", "BEGIN:VCARD\r\nVERSION:4.0\r\nBEGIN:VCARD\r\nEND:VCARD\r\n", ErrInvalid},
		{"nested other BEGIN", "BEGIN:VCARD\r\nVERSION:4.0\r\nBEGIN:X\r\nEND:X\r\nEND:VCARD\r\n", ErrInvalid},
		{
			"two cards",
			"BEGIN:VCARD\r\nVERSION:4.0\r\nEND:VCARD\r\nBEGIN:VCARD\r\nVERSION:4.0\r\nEND:VCARD\r\n",
			ErrMultipleCards,
		},
		{"no VERSION", "BEGIN:VCARD\r\nFN:X\r\nEND:VCARD\r\n", ErrInvalid},
		{"VERSION 2.1", "BEGIN:VCARD\r\nVERSION:2.1\r\nFN:X\r\nEND:VCARD\r\n", ErrUnsupportedVersion},
		{"VERSION 5.0", "BEGIN:VCARD\r\nVERSION:5.0\r\nFN:X\r\nEND:VCARD\r\n", ErrUnsupportedVersion},
		{"content after END", "BEGIN:VCARD\r\nVERSION:4.0\r\nEND:VCARD\r\nFN:X\r\n", ErrInvalid},
		{"empty", "", ErrInvalid},
		{"only blank lines", "\r\n\r\n", ErrInvalid},
		{"mismatched END", "BEGIN:VCARD\r\nVERSION:4.0\r\nEND:VCALENDAR\r\n", ErrInvalid},
		{"syntax error", "BEGIN:VCARD\r\nVERSION:4.0\r\nbroken\r\nEND:VCARD\r\n", ErrInvalid},
		{"repeated VERSION", "BEGIN:VCARD\r\nVERSION:4.0\r\nVERSION:4.0\r\nEND:VCARD\r\n", ErrInvalid},
		{"doubled END", "BEGIN:VCARD\r\nVERSION:4.0\r\nEND:VCARD\r\nEND:VCARD\r\n", ErrInvalid},
		{"only a grouped VERSION", "BEGIN:VCARD\r\nitem1.VERSION:4.0\r\nEND:VCARD\r\n", ErrInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := Parse([]byte(tt.in))
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			if c != nil {
				t.Fatalf("card = %#v, want nil", c)
			}
			if _, err := Validate([]byte(tt.in)); !errors.Is(err, tt.want) {
				t.Fatalf("Validate err = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestTextUnescape(t *testing.T) {
	p := Property{Raw: `a\, b\; c\nd\\e\Nf\xg\\`}
	if got, want := p.Text(), "a, b; c\nd\\e\nfxg\\"; got != want {
		t.Fatalf("Text = %q, want %q", got, want)
	}
}

func TestFind(t *testing.T) {
	c, err := Parse([]byte("BEGIN:VCARD\nVERSION:4.0\nEMAIL:a@example.org\nFN:X\nemail:b@example.org\nEND:VCARD\n"))
	if err != nil {
		t.Fatal(err)
	}
	got := c.Find("Email")
	if len(got) != 2 || got[0].Raw != "a@example.org" || got[1].Raw != "b@example.org" {
		t.Fatalf("Find = %#v", got)
	}
	if len(c.Find("TEL")) != 0 {
		t.Fatal("expected no TEL")
	}
}

func TestFoldedUTF8(t *testing.T) {
	c, err := Parse([]byte("BEGIN:VCARD\r\nVERSION:4.0\r\nFN:\xc5\r\n \xa1\r\nEND:VCARD\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Find("FN")[0].Text(); got != "š" {
		t.Fatalf("FN = %q", got)
	}
}

func wrap(lines ...string) []byte {
	data := "BEGIN:VCARD\r\nVERSION:3.0\r\n"
	for _, l := range lines {
		data += l + "\r\n"
	}
	return []byte(data + "END:VCARD\r\n")
}

func TestUID(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		want  string
	}{
		{"grouped UID ignored", []string{"item1.UID:grouped", "UID:real"}, "real"},
		{"only a grouped UID", []string{"item1.UID:grouped"}, ""},
		{"first UID wins", []string{"UID:first", "UID:second"}, "first"},
		{"escaped UID", []string{`UID:urn:uuid:a\,b\;c`}, "urn:uuid:a,b;c"},
		{"lower-case name", []string{"uid:x"}, "x"},
		{"no UID", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := Parse(wrap(tt.lines...))
			if err != nil {
				t.Fatal(err)
			}
			if got := c.UID(); got != tt.want {
				t.Fatalf("UID = %q, want %q", got, tt.want)
			}
			if uid, err := Validate(wrap(tt.lines...)); err != nil || uid != tt.want {
				t.Fatalf("Validate = %q, %v, want %q", uid, err, tt.want)
			}
		})
	}
}

// appleCard mixes lines as Apple Contacts, Thunderbird and other clients
// write them.
const appleCard = "BEGIN:VCARD\r\n" +
	"VERSION:3.0\r\n" +
	"N:Lovelace;Ada;;;\r\n" +
	"FN:Ada Lovelace\r\n" +
	"item1.EMAIL;type=INTERNET;type=pref:ada@example.org\r\n" +
	"item1.X-ABLabel:_$!<Work>!$_\r\n" +
	"TEL;TYPE=\"VOICE,CELL\":+420 123\r\n" +
	"ADR;TYPE=work:;;Main St\\, 1;Prague\\, CZ;;110 00;Czechia\r\n" +
	"PHOTO;ENCODING=b;TYPE=JPEG:/9j/4AAQSkZJRgABAQ\r\n" +
	" AAAQABAAD/2wBDAAgGBgcGBQgHBwcJCQgKDBQNDAsLDB\r\n" +
	" kSEw8UHRofHh0aHBwgJC4nICIsIxwcKDcpLDAxNDQ0Hy\r\n" +
	" c5PTgyPC4zNDL/\r\n" +
	"END:VCARD\r\n"

func TestRealClientInput(t *testing.T) {
	c, err := Parse([]byte(appleCard))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	email := c.Find("EMAIL")
	if len(email) != 1 || email[0].Group != "ITEM1" || email[0].Raw != "ada@example.org" {
		t.Fatalf("EMAIL = %#v", email)
	}
	if want := map[string][]string{"TYPE": {"INTERNET", "pref"}}; !reflect.DeepEqual(email[0].Params, want) {
		t.Fatalf("EMAIL params = %#v", email[0].Params)
	}
	label := c.Find("X-ABLABEL")
	if len(label) != 1 || label[0].Group != "ITEM1" || label[0].Text() != "_$!<Work>!$_" {
		t.Fatalf("X-ABLabel = %#v", label)
	}
	tel := c.Find("TEL")
	if len(tel) != 1 || tel[0].Raw != "+420 123" {
		t.Fatalf("TEL = %#v", tel)
	}
	// A quoted value is one value: the comma inside it does not split it.
	if want := map[string][]string{"TYPE": {"VOICE,CELL"}}; !reflect.DeepEqual(tel[0].Params, want) {
		t.Fatalf("TEL params = %#v", tel[0].Params)
	}
	if n := c.Find("N")[0]; n.Raw != "Lovelace;Ada;;;" || n.Text() != "Lovelace;Ada;;;" {
		t.Fatalf("N = %#v", n)
	}
	adr := c.Find("ADR")[0]
	if adr.Raw != `;;Main St\, 1;Prague\, CZ;;110 00;Czechia` {
		t.Fatalf("ADR raw = %q", adr.Raw)
	}
	if got := adr.Text(); got != ";;Main St, 1;Prague, CZ;;110 00;Czechia" {
		t.Fatalf("ADR text = %q", got)
	}
	photo := c.Find("PHOTO")[0]
	want := "/9j/4AAQSkZJRgABAQAAAQABAAD/2wBDAAgGBgcGBQgHBwcJCQgKDBQNDAsLDB" +
		"kSEw8UHRofHh0aHBwgJC4nICIsIxwcKDcpLDAxNDQ0Hyc5PTgyPC4zNDL/"
	if photo.Raw != want {
		t.Fatalf("PHOTO = %q", photo.Raw)
	}
	if !reflect.DeepEqual(photo.Params, map[string][]string{"ENCODING": {"b"}, "TYPE": {"JPEG"}}) {
		t.Fatalf("PHOTO params = %#v", photo.Params)
	}
}

func TestTextEdgeCases(t *testing.T) {
	tests := []struct{ raw, want string }{
		{`abc\`, `abc\`},
		{`\`, `\`},
		{`\é`, "é"},
		{`a\ü\,b`, "aü,b"},
		{`\\é`, `\é`},
		{`a\n\N`, "a\n\n"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := (Property{Raw: tt.raw}).Text(); got != tt.want {
			t.Errorf("Text(%q) = %q, want %q", tt.raw, got, tt.want)
		}
	}
}

func TestStateMachine(t *testing.T) {
	t.Run("lower-case markers", func(t *testing.T) {
		c, err := Parse([]byte("begin:vcard\r\nversion:4.0\r\nfn:X\r\nend:vcard\r\n"))
		if err != nil {
			t.Fatal(err)
		}
		if c.Version != "4.0" || len(c.Props) != 1 || c.Props[0].Name != "FN" {
			t.Fatalf("card = %#v", c)
		}
	})
	t.Run("grouped VERSION is an ordinary property", func(t *testing.T) {
		c, err := Parse([]byte("BEGIN:VCARD\r\nitem1.VERSION:2.1\r\nVERSION:4.0\r\nEND:VCARD\r\n"))
		if err != nil {
			t.Fatal(err)
		}
		if c.Version != "4.0" {
			t.Fatalf("Version = %q", c.Version)
		}
		if len(c.Props) != 1 || c.Props[0].Group != "ITEM1" || c.Props[0].Name != "VERSION" || c.Props[0].Raw != "2.1" {
			t.Fatalf("props = %#v", c.Props)
		}
	})
	t.Run("markers are not properties", func(t *testing.T) {
		c, err := Parse([]byte(card30))
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range c.Props {
			if p.Group == "" && (p.Name == "BEGIN" || p.Name == "END" || p.Name == "VERSION") {
				t.Fatalf("marker in Props: %#v", p)
			}
		}
	})
}

func TestMarkerValuesTrimmed(t *testing.T) {
	c, err := Parse([]byte("BEGIN: VCARD \r\nVERSION: 4.0 \r\nFN:X\r\nEND:VCARD\t\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Version != "4.0" || len(c.Props) != 1 {
		t.Fatalf("card = %#v", c)
	}
}

func TestGroupedMarkers(t *testing.T) {
	tests := []struct {
		name, in string
		want     error
	}{
		{"grouped BEGIN first", "item1.BEGIN:VCARD\r\nVERSION:4.0\r\nEND:VCARD\r\n", ErrInvalid},
		{"grouped BEGIN after END", "BEGIN:VCARD\r\nVERSION:4.0\r\nEND:VCARD\r\nitem1.BEGIN:VCARD\r\n", ErrInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.in))
			if !errors.Is(err, tt.want) || errors.Is(err, ErrMultipleCards) {
				t.Fatalf("err = %v, want %v only", err, tt.want)
			}
		})
	}
	// Inside a card, grouped BEGIN and END lines are ordinary properties.
	c, err := Parse([]byte("BEGIN:VCARD\r\nVERSION:4.0\r\nitem1.BEGIN:X\r\nitem1.END:VCARD\r\nEND:VCARD\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Props) != 2 || c.Props[0].Group != "ITEM1" || c.Props[1].Name != "END" {
		t.Fatalf("props = %#v", c.Props)
	}
}

// foldedPhoto returns a PHOTO line with n bytes of base64-like data, folded
// at 75 octets as clients write it.
func foldedPhoto(n int) string {
	data := strings.Repeat("QUJD", n/4+1)[:n]
	var b strings.Builder
	b.WriteString("PHOTO;ENCODING=b;TYPE=JPEG:")
	b.WriteString(data[:40])
	for rest := data[40:]; len(rest) > 0; {
		k := min(74, len(rest))
		b.WriteString("\r\n ")
		b.WriteString(rest[:k])
		rest = rest[k:]
	}
	return b.String()
}

func TestLargePhoto(t *testing.T) {
	const size = 3 << 19 // 1.5 MiB
	in := wrap("UID:big", foldedPhoto(size))

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	c, err := Parse(in)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := len(c.Find("PHOTO")[0].Raw); got != size {
		t.Fatalf("PHOTO is %d bytes, want %d", got, size)
	}
	// Unfolding grows one buffer and copies it once into the value, so the
	// allocations stay a small multiple of the input.
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 12*uint64(len(in)) {
		t.Fatalf("Parse allocated %d bytes for %d bytes of input", alloc, len(in))
	}
	if uid, err := Validate(in); err != nil || uid != "big" {
		t.Fatalf("Validate = %q, %v", uid, err)
	}
}

func TestLineOverTheLimit(t *testing.T) {
	in := wrap("PHOTO:" + strings.Repeat("A", 16<<20))
	_, err := Parse(in)
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "line longer than") {
		t.Fatalf("err = %v, want ErrInvalid for a long line", err)
	}
	if _, err := Validate(in); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Validate err = %v, want ErrInvalid", err)
	}
}

func TestErrorsTruncateValues(t *testing.T) {
	long := strings.Repeat("x", 900000)
	for _, in := range []string{
		"BEGIN:VCARD\r\nVERSION:4.0\r\nBEGIN:" + long + "\r\nEND:VCARD\r\n",
		"BEGIN:VCARD\r\nVERSION:4.0\r\nEND:" + long + "\r\n",
		"BEGIN:VCARD\r\nVERSION:" + long + "\r\nEND:VCARD\r\n",
		"BEGIN:VCARD\r\nVERSION:4.0\r\n" + long + "_:x\r\nEND:VCARD\r\n",
	} {
		_, err := Parse([]byte(in))
		if err == nil {
			t.Fatal("Parse accepted the card")
		}
		if n := len(err.Error()); n >= 300 {
			t.Fatalf("error is %d bytes long", n)
		}
	}
}
