// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package ical

import (
	"errors"
	"strings"
	"testing"
)

const nested = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//x//y//EN\r\n" +
	"BEGIN:VTIMEZONE\r\nTZID:Europe/Prague\r\n" +
	"BEGIN:STANDARD\r\nTZOFFSETFROM:+0200\r\nTZOFFSETTO:+0100\r\nEND:STANDARD\r\n" +
	"BEGIN:DAYLIGHT\r\nTZOFFSETFROM:+0100\r\nTZOFFSETTO:+0200\r\nEND:DAYLIGHT\r\n" +
	"END:VTIMEZONE\r\n" +
	"BEGIN:VEVENT\r\nUID:abc\r\nDTSTART;TZID=Europe/Prague:20260101T100000\r\n" +
	"SUMMARY:Long sum\r\n mary\r\n" +
	"ATTENDEE;ROLE=REQ-PARTICIPANT;ROLE=CHAIR:mailto:a@example.org\r\n" +
	"BEGIN:VALARM\r\nACTION:DISPLAY\r\nTRIGGER:-PT15M\r\nEND:VALARM\r\n" +
	"END:VEVENT\r\nEND:VCALENDAR\r\n"

func TestParseNesting(t *testing.T) {
	for _, in := range []string{nested, strings.ReplaceAll(nested, "\r\n", "\n")} {
		cal, err := Parse([]byte(in))
		if err != nil {
			t.Fatal(err)
		}
		if cal.Name != "VCALENDAR" || len(cal.Children) != 2 {
			t.Fatalf("root = %s with %d children", cal.Name, len(cal.Children))
		}
		tz := cal.Comps("vtimezone")
		if len(tz) != 1 || len(tz[0].Children) != 2 ||
			tz[0].Children[0].Name != "STANDARD" || tz[0].Children[1].Name != "DAYLIGHT" {
			t.Fatalf("timezone tree wrong: %+v", tz)
		}
		ev := cal.Comps("VEVENT")
		if len(ev) != 1 {
			t.Fatal("want one VEVENT")
		}
		if got := ev[0].Prop("summary").Value; got != "Long summary" {
			t.Errorf("SUMMARY = %q", got)
		}
		if got := ev[0].Prop("DTSTART").Params["TZID"]; len(got) != 1 || got[0] != "Europe/Prague" {
			t.Errorf("TZID = %v", got)
		}
		if got := ev[0].Prop("ATTENDEE").Params["ROLE"]; len(got) != 2 {
			t.Errorf("merged ROLE = %v", got)
		}
		if al := ev[0].Comps("VALARM"); len(al) != 1 || al[0].Prop("TRIGGER").Value != "-PT15M" {
			t.Errorf("alarm wrong: %+v", al)
		}
		if ev[0].Prop("NOPE") != nil || len(ev[0].PropAll("nope")) != 0 {
			t.Error("missing property should be nil/empty")
		}
		if cal.Prop("VERSION").Value != "2.0" {
			t.Error("VERSION")
		}
	}
}

func wrap(body string) string {
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//x//y\r\n" + body + "END:VCALENDAR\r\n"
}

func TestValidateOK(t *testing.T) {
	cases := map[string]string{
		"event": wrap("BEGIN:VEVENT\r\nUID:u1\r\nEND:VEVENT\r\n"),
		"override": wrap("BEGIN:VEVENT\r\nUID:u1\r\nRRULE:FREQ=DAILY\r\nEND:VEVENT\r\n" +
			"BEGIN:VEVENT\r\nUID:u1\r\nRECURRENCE-ID:20260102T100000Z\r\nEND:VEVENT\r\n"),
		"with timezone": wrap("BEGIN:VTIMEZONE\r\nTZID:X\r\nEND:VTIMEZONE\r\nBEGIN:VEVENT\r\nUID:u1\r\nEND:VEVENT\r\n"),
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			info, err := Validate([]byte(in), []string{"VEVENT", "VTODO"})
			if err != nil {
				t.Fatal(err)
			}
			if info != (Info{UID: "u1", Component: "VEVENT"}) {
				t.Errorf("info = %+v", info)
			}
		})
	}
}

func TestValidateErrors(t *testing.T) {
	ev := "BEGIN:VEVENT\r\nUID:u1\r\nEND:VEVENT\r\n"
	hdr := "BEGIN:VCALENDAR\r\n"
	cases := []struct {
		name string
		in   string
		want error
	}{
		{"empty", "", ErrInvalid},
		{"no begin", "VERSION:2.0\r\n", ErrInvalid},
		{"missing end", hdr + "VERSION:2.0\r\nPRODID:x\r\n" + ev, ErrInvalid},
		{"mismatched end", hdr + "VERSION:2.0\r\nPRODID:x\r\nBEGIN:VEVENT\r\nUID:u\r\nEND:VTODO\r\nEND:VCALENDAR\r\n", ErrInvalid},
		{"missing component end", hdr + "VERSION:2.0\r\nPRODID:x\r\nBEGIN:VEVENT\r\nUID:u\r\nEND:VCALENDAR\r\n", ErrInvalid},
		{"no version", hdr + "PRODID:x\r\n" + ev + "END:VCALENDAR\r\n", ErrInvalid},
		{"bad version", hdr + "VERSION:1.0\r\nPRODID:x\r\n" + ev + "END:VCALENDAR\r\n", ErrInvalid},
		{"no prodid", hdr + "VERSION:2.0\r\n" + ev + "END:VCALENDAR\r\n", ErrInvalid},
		{"method", hdr + "VERSION:2.0\r\nPRODID:x\r\nMETHOD:REQUEST\r\n" + ev + "END:VCALENDAR\r\n", ErrInvalid},
		{"no uid", wrap("BEGIN:VEVENT\r\nSUMMARY:x\r\nEND:VEVENT\r\n"), ErrUID},
		{"empty uid", wrap("BEGIN:VEVENT\r\nUID:\r\nEND:VEVENT\r\n"), ErrUID},
		{"empty uid later", wrap(ev + "BEGIN:VEVENT\r\nUID:\r\nEND:VEVENT\r\n"), ErrUID},
		{"different uids", wrap(ev + "BEGIN:VEVENT\r\nUID:u2\r\nEND:VEVENT\r\n"), ErrUID},
		{"mixed types", wrap(ev + "BEGIN:VTODO\r\nUID:u1\r\nEND:VTODO\r\n"), ErrInvalid},
		{"not allowed", wrap("BEGIN:VTODO\r\nUID:u1\r\nEND:VTODO\r\n"), ErrUnsupportedComponent},
		{"no component", wrap(""), ErrInvalid},
		{"only timezone", wrap("BEGIN:VTIMEZONE\r\nTZID:X\r\nEND:VTIMEZONE\r\n"), ErrInvalid},
		{"trailing content", wrap(ev) + "BEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n", ErrInvalid},
		{"trailing property", wrap(ev) + "X:y\r\n", ErrInvalid},
		{"syntax", "garbage without colon\r\n", ErrInvalid},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Validate([]byte(c.in), []string{"VEVENT"})
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
		})
	}
}

func TestValidateTrailingBlankLines(t *testing.T) {
	in := wrap("BEGIN:VEVENT\r\nUID:u1\r\nEND:VEVENT\r\n") + "\r\n\r\n"
	if _, err := Validate([]byte(in), []string{"VEVENT"}); err != nil {
		t.Fatal(err)
	}
}

func TestTextUnescape(t *testing.T) {
	p := Property{Name: "SUMMARY", Value: `a\, b\; c\\d\ne\Nf`}
	if got, want := p.Text(), "a, b; c\\d\ne\nf"; got != want {
		t.Errorf("Text() = %q, want %q", got, want)
	}
	if got := (Property{Value: "plain"}).Text(); got != "plain" {
		t.Errorf("Text() = %q", got)
	}
}

func FuzzParse(f *testing.F) {
	f.Add([]byte(nested))
	f.Add([]byte("BEGIN:VCALENDAR\nEND:VCALENDAR\n"))
	f.Add([]byte(""))
	f.Fuzz(func(t *testing.T, data []byte) {
		if cal, err := Parse(data); err == nil && cal.Name != "VCALENDAR" {
			t.Fatalf("root = %q", cal.Name)
		}
		_, _ = Validate(data, []string{"VEVENT"})
	})
}

func TestParseComponentNames(t *testing.T) {
	ev := "BEGIN:VEVENT\r\nUID:u1\r\nEND:VEVENT\r\n"
	bad := map[string]string{
		"empty begin":     wrap("BEGIN:\r\nEND:\r\n" + ev),
		"blank begin":     wrap("BEGIN: \r\nEND: \r\n" + ev),
		"nested calendar": wrap("BEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n" + ev),
		"deep calendar":   wrap("BEGIN:VEVENT\r\nUID:u1\r\nBEGIN:vcalendar\r\nEND:vcalendar\r\nEND:VEVENT\r\n"),
		"dotless i end":   wrap(ev + "BEGIN:I\r\nEND:ı\r\n"),
		"long s end":      wrap("BEGIN:VEVENT\r\nUID:u1\r\nBEGIN:STANDARD\r\nEND:ſTANDARD\r\nEND:VEVENT\r\n"),
	}
	for name, in := range bad {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(in)); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}

	// A lookalike name is kept as it is: it does not turn into an ASCII one.
	cal, err := Parse([]byte(wrap("BEGIN:vıevent\r\nUID:u1\r\nEND:vıevent\r\n")))
	if err != nil {
		t.Fatal(err)
	}
	if got := cal.Children[0].Name; got != "VıEVENT" {
		t.Errorf("name = %q", got)
	}
	if len(cal.Comps("VIEVENT")) != 0 || len(cal.Comps("VEVENT")) != 0 {
		t.Error("lookalike name matched an ASCII name")
	}
	in := wrap("BEGIN:vavaılabılıty\r\nUID:u1\r\nEND:vavaılabılıty\r\n")
	if _, err := Validate([]byte(in), []string{"VAVAILABILITY"}); !errors.Is(err, ErrUnsupportedComponent) {
		t.Errorf("lookalike allowed name: err = %v, want ErrUnsupportedComponent", err)
	}
	in = wrap("BEGIN:VTıMEZONE\r\nTZID:X\r\nEND:VTıMEZONE\r\n" + ev)
	if _, err := Validate([]byte(in), []string{"VEVENT"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("lookalike VTIMEZONE: err = %v, want ErrInvalid (mixed components)", err)
	}
}

func TestParseLowerCaseNames(t *testing.T) {
	in := "begin:vcalendar\r\nversion:2.0\r\nprodid:x\r\nbegin:vevent\r\nuid:u1\r\n" +
		"begin:valarm\r\nend:VALARM\r\nEnd:vEvent\r\nend:vcalendar\r\n"
	info, err := Validate([]byte(in), []string{"VEVENT"})
	if err != nil {
		t.Fatal(err)
	}
	if info != (Info{UID: "u1", Component: "VEVENT"}) {
		t.Errorf("info = %+v", info)
	}
	cal, err := Parse([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if ev := cal.Children[0]; ev.Name != "VEVENT" || ev.Children[0].Name != "VALARM" {
		t.Errorf("names not upper-cased: %q, %q", ev.Name, ev.Children[0].Name)
	}
}

func TestParseDeepNesting(t *testing.T) {
	const depth = 99990 // just under the content line limit
	open := strings.Repeat("BEGIN:X\r\n", depth)
	in := "BEGIN:VCALENDAR\r\n" + open
	if _, err := Parse([]byte(in)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unclosed: err = %v, want ErrInvalid", err)
	}
	half := depth / 2
	in = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:x\r\n" + strings.Repeat("BEGIN:X\r\n", half) +
		strings.Repeat("END:X\r\n", half) + "END:VCALENDAR\r\n"
	if _, err := Validate([]byte(in), []string{"VEVENT"}); !errors.Is(err, ErrUnsupportedComponent) {
		t.Fatalf("balanced: err = %v, want ErrUnsupportedComponent", err)
	}
	in = "BEGIN:VCALENDAR\r\n" + strings.Repeat("BEGIN:X\r\n", 100000)
	if _, err := Parse([]byte(in)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("over the line limit: err = %v, want ErrInvalid", err)
	}
}

func TestValidateHeader(t *testing.T) {
	ev := "BEGIN:VEVENT\r\nUID:u1\r\nEND:VEVENT\r\n"
	cal := func(hdr string) []byte {
		return []byte("BEGIN:VCALENDAR\r\n" + hdr + ev + "END:VCALENDAR\r\n")
	}
	for name, hdr := range map[string]string{
		"version with trailing space": "VERSION:2.0 \r\nPRODID:x\r\n",
		"empty prodid":                "VERSION:2.0\r\nPRODID:\r\n",
	} {
		if _, err := Validate(cal(hdr), []string{"VEVENT"}); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := Validate(cal("VERSION:2.0\r\nPRODID:x\r\nMETHOD:PUBLISH\r\n"), []string{"VEVENT"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("METHOD:PUBLISH: err = %v, want ErrInvalid", err)
	}
	second := wrap("BEGIN:VEVENT\r\nSUMMARY:no uid\r\nEND:VEVENT\r\n" + ev)
	if _, err := Validate([]byte(second), []string{"VEVENT"}); !errors.Is(err, ErrUID) {
		t.Errorf("first component without UID: err = %v, want ErrUID", err)
	}
}

func TestTextEdgeCases(t *testing.T) {
	for in, want := range map[string]string{
		`Žluťoučký kůň\, 日本\;語\n🎉`: "Žluťoučký kůň, 日本;語\n🎉",
		`trailing\`: `trailing\`,
		`\`:         `\`,
		`a\\`:       `a\`,
		`\é`:        "é",
	} {
		if got := (Property{Value: in}).Text(); got != want {
			t.Errorf("Text(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidateDefaultsToVEVENT(t *testing.T) {
	event := wrap("BEGIN:VEVENT\r\nUID:u1\r\nEND:VEVENT\r\n")
	todo := wrap("BEGIN:VTODO\r\nUID:u1\r\nEND:VTODO\r\n")
	for name, allowed := range map[string][]string{"nil": nil, "empty": {}} {
		if _, err := Validate([]byte(event), allowed); err != nil {
			t.Errorf("%s: VEVENT: %v", name, err)
		}
		if _, err := Validate([]byte(todo), allowed); !errors.Is(err, ErrUnsupportedComponent) {
			t.Errorf("%s: VTODO: err = %v, want ErrUnsupportedComponent", name, err)
		}
	}
}
