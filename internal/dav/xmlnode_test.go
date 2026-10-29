// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package dav

import (
	"encoding/xml"
	"errors"
	"strings"
	"testing"
	"time"
)

const sampleDoc = `<?xml version="1.0" encoding="UTF-8"?>
<!-- a comment -->
<?pi ignored?>
<D:propfind xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:prop>
    <D:displayname/>
    <C:calendar-data x="1" C:y="2" xml:lang="en">  hi &amp; <![CDATA[<there>]]> </C:calendar-data>
    <plain xmlns="urn:example">text</plain>
    <bare xmlns="">no ns</bare>
  </D:prop>
  <D:prop/>
</D:propfind>`

func TestParseXMLDocument(t *testing.T) {
	root, err := ParseXML([]byte(sampleDoc))
	if err != nil {
		t.Fatal(err)
	}
	if root.Name != N(NSDAV, "propfind") {
		t.Fatalf("root = %v", root.Name)
	}
	if len(root.Attrs) != 0 {
		t.Errorf("xmlns declarations leaked into Attrs: %v", root.Attrs)
	}
	if got := len(root.Kids(PropEl)); got != 2 {
		t.Fatalf("Kids(prop) = %d", got)
	}
	prop := root.Child(PropEl)
	if prop == nil || prop.Child(DisplayName) == nil {
		t.Fatal("missing displayname")
	}
	if prop.Text != "" {
		t.Errorf("prop text = %q", prop.Text)
	}
	cd := prop.Child(CalendarData)
	if cd == nil {
		t.Fatal("missing calendar-data")
	}
	if cd.Text != "  hi & <there> " {
		t.Errorf("leaf text = %q, want untrimmed", cd.Text)
	}
	if cd.Attr("x") != "1" || cd.Attr("y") != "2" || cd.Attr("lang") != "en" || cd.Attr("z") != "" {
		t.Errorf("attrs = %v", cd.Attrs)
	}
	if cd.Attrs[xml.Name{Space: NSCalDAV, Local: "y"}] != "2" {
		t.Errorf("namespaced attr key wrong: %v", cd.Attrs)
	}
	if cd.Attrs[xml.Name{Local: "x"}] != "1" {
		t.Errorf("unprefixed attr must have no namespace: %v", cd.Attrs)
	}
	if p := prop.Child(N("urn:example", "plain")); p == nil || p.Text != "text" {
		t.Errorf("default namespace element: %v", p)
	}
	if b := prop.Child(N("", "bare")); b == nil || b.Text != "no ns" {
		t.Errorf("no-namespace element: %v", b)
	}
	if prop.Child(Href) != nil {
		t.Error("Child of absent name must be nil")
	}
}

func TestParseXMLTrimsTextWithChildren(t *testing.T) {
	root, err := ParseXML([]byte("<a>\n  x <b/> y\n</a>"))
	if err != nil {
		t.Fatal(err)
	}
	if root.Text != "x  y" {
		t.Errorf("text = %q", root.Text)
	}
}

func TestParseXMLRejects(t *testing.T) {
	cases := map[string]string{
		"empty":         "",
		"whitespace":    " \n ",
		"not xml":       "hello",
		"unclosed":      "<a><b></a>",
		"unclosed root": "<a>",
		"two roots":     "<a/><b/>",
		"trailing text": "<a/>junk",
		"mismatch":      "<a></b>",
		"unbound":       "<x:a/>",
		"unbound attr":  `<a x:b="1"/>`,
		"doctype":       "<!DOCTYPE a><a/>",
		"entity":        `<!DOCTYPE a [<!ENTITY e "boom">]><a>&e;</a>`,
		"undefined ent": "<a>&e;</a>",
		"charset":       `<?xml version="1.0" encoding="ISO-8859-1"?><a/>`,
		"second decl":   `<?xml version="1.0"?><a><?xml version="1.0"?></a>`,
		"late decl":     ` <?xml version="1.0"?><a/>`,
		"decl after pi": `<?p?><?xml version="1.0"?><a/>`,
		"dup attr":      `<a b="1" b="2"/>`,
		"dup collide":   `<a xmlns:x="u" xmlns:y="u" x:b="1" y:b="2"/>`,
		"dup xmlns":     `<a xmlns:x="u" xmlns:x="v"/>`,
		"dup default":   `<a xmlns="u" xmlns="v"/>`,
		"unbind prefix": `<a xmlns:x=""/>`,
		"rebind xml":    `<a xmlns:xml="urn:x"/>`,
		"bind xmlns":    `<a xmlns:xmlns="urn:x"/>`,
		"empty prefix":  "<:a/>",
		"empty local":   `<x:/>`,
		"attr empty":    `<a x:="1" xmlns:x="u"/>`,
		"attr no pfx":   `<a :b="1"/>`,
		"xmlns empty":   `<a xmlns:="u"/>`,
		"double BOM":    "\xef\xbb\xbf\xef\xbb\xbf<a/>",
		"BOM mid":       "<a>\xef\xbb\xbf</a><!---->\xef\xbb\xbf",
		"BOM before ws": " \xef\xbb\xbf<a/>",
		"charset16":     `<?xml version="1.0" encoding="UTF-16"?><a/>`,
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			n, err := ParseXML([]byte(in))
			if !errors.Is(err, ErrXML) || n != nil {
				t.Fatalf("got %v, %v", n, err)
			}
			if strings.Contains(err.Error(), "boom") {
				t.Errorf("error echoes input: %v", err)
			}
		})
	}
}

func TestParseXMLAcceptsCharsetLabels(t *testing.T) {
	for _, l := range []string{"utf-8", "UTF-8", "us-ascii"} {
		if _, err := ParseXML([]byte(`<?xml version="1.0" encoding="` + l + `"?><a/>`)); err != nil {
			t.Errorf("%s: %v", l, err)
		}
	}
}

func TestParseXMLBOM(t *testing.T) {
	for _, in := range []string{"\xef\xbb\xbf<a/>", "\xef\xbb\xbf<?xml version=\"1.0\" encoding=\"UTF-8\"?><a/>"} {
		n, err := ParseXML([]byte(in))
		if err != nil || n.Name.Local != "a" {
			t.Errorf("%q: %v %v", in, n, err)
		}
	}
}

func TestParseXMLAttrDeterministic(t *testing.T) {
	for i := 0; i < 200; i++ {
		n, err := ParseXML([]byte(`<a xmlns:x="u" xmlns:y="t" y:b="3" x:b="1" b="2"/>`))
		if err != nil || n.Attr("b") != "2" {
			t.Fatalf("no-namespace attribute must win: %v %v", n, err)
		}
		n, err = ParseXML([]byte(`<a xmlns:x="u" xmlns:y="t" y:b="3" x:b="1"/>`))
		if err != nil || n.Attr("b") != "3" {
			t.Fatalf("smallest namespace must win: %v %v", n, err)
		}
	}
}

func TestParseXMLXMLNamespacePrefix(t *testing.T) {
	if _, err := ParseXML([]byte(`<a xmlns:xml="http://www.w3.org/XML/1998/namespace"/>`)); err != nil {
		t.Error(err)
	}
}

func TestParseXMLIgnoresPI(t *testing.T) {
	root, err := ParseXML([]byte("<?xml-stylesheet href='x'?><a><?p q?><b/></a><!-- c -->\n"))
	if err != nil || len(root.Children) != 1 {
		t.Fatalf("%v %v", root, err)
	}
}

func TestParseXMLSizeLimit(t *testing.T) {
	pad := func(n int) []byte {
		head, tail := "<a>", "</a>"
		return []byte(head + strings.Repeat("x", n-len(head)-len(tail)) + tail)
	}
	if _, err := ParseXML(pad(MaxXMLBytes)); err != nil {
		t.Fatalf("MaxXMLBytes: %v", err)
	}
	if _, err := ParseXML(pad(MaxXMLBytes + 1)); !errors.Is(err, ErrXML) {
		t.Fatalf("MaxXMLBytes+1: %v", err)
	}
}

func nested(n int) []byte {
	return []byte(strings.Repeat("<a>", n) + strings.Repeat("</a>", n))
}

func TestParseXMLDepth(t *testing.T) {
	if _, err := ParseXML(nested(MaxXMLDepth)); err != nil {
		t.Fatalf("depth %d: %v", MaxXMLDepth, err)
	}
	if _, err := ParseXML(nested(MaxXMLDepth + 1)); !errors.Is(err, ErrXML) {
		t.Fatalf("depth %d: %v", MaxXMLDepth+1, err)
	}
}

func TestParseXMLDeepNestingIsFast(t *testing.T) {
	data := []byte(strings.Repeat("<a>", 100000))
	start := time.Now()
	_, err := ParseXML(data)
	if !errors.Is(err, ErrXML) {
		t.Fatalf("err = %v", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("took %v", d)
	}
}

func TestParseXMLElementCount(t *testing.T) {
	in := "<a>" + strings.Repeat("<b/>", maxXMLElements) + "</a>"
	if len(in) > MaxXMLBytes {
		t.Skip("limit not reachable within the byte bound")
	}
	if _, err := ParseXML([]byte(in)); !errors.Is(err, ErrXML) {
		t.Fatalf("err = %v", err)
	}
}

func TestParseXMLAttributeCount(t *testing.T) {
	in := "<a" + strings.Repeat(" b='' ", 150000) + "/>"
	if len(in) > MaxXMLBytes {
		t.Fatal("input exceeds byte bound; test is not exercising the attribute cap")
	}
	if _, err := ParseXML([]byte(in)); !errors.Is(err, ErrXML) {
		t.Fatalf("err = %v", err)
	}
}

func TestNameHelpers(t *testing.T) {
	if Multistatus != (xml.Name{Space: "DAV:", Local: "multistatus"}) {
		t.Error(Multistatus)
	}
	if CRFilter == CalFilter || CRTextMatch == CalTextMatch {
		t.Error("carddav and caldav names must differ")
	}
}

func FuzzParseXML(f *testing.F) {
	for _, s := range []string{
		sampleDoc, "", "<a/>", "<a><b></a>", "<!DOCTYPE a [<!ENTITY e 'x'>]><a>&e;</a>",
		"<x:a/>", "<a/><b/>", "<?xml version='1.0' encoding='latin1'?><a/>",
		string(nested(70)), `<a xmlns:x="u" x:y="1" xmlns="d"><b/></a>`, "\xff\xfe<a/>",
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		n, err := ParseXML(data)
		if err != nil {
			if !errors.Is(err, ErrXML) || n != nil {
				t.Fatalf("bad error result: %v %v", n, err)
			}
			return
		}
		if n == nil || n.Name.Local == "" {
			t.Fatalf("nil or nameless root: %v", n)
		}
	})
}
