// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package filestore

import (
	"bytes"
	"errors"
	"reflect"
	"testing"
)

func TestRecordRoundTrip(t *testing.T) {
	for _, r := range []record{
		{Rev: 12, Name: "we\"ird\nname ü.ics", ETag: "e1", UID: "u1", Size: 1084, MTime: 1793300000000000000},
		{Rev: 13, Name: "gone\"\n.ics", Deleted: true},
	} {
		line := encodeRecord(r)
		if !bytes.HasSuffix(line, []byte("\n")) || bytes.Count(line, []byte("\n")) != 1 {
			t.Fatalf("not exactly one line: %q", line)
		}
		recs, n, err := parseLog(line)
		if err != nil || n != len(line) || len(recs) != 1 || !reflect.DeepEqual(recs[0], r) {
			t.Fatalf("round trip of %+v: got %+v, %d, %v", r, recs, n, err)
		}
	}
	if got := string(encodeRecord(record{Rev: 1, Name: "a", Deleted: true})); got != "{\"r\":1,\"n\":\"a\",\"d\":true}\n" {
		t.Fatalf("zero fields not omitted: %q", got)
	}
}

func TestParseLogTornTail(t *testing.T) {
	a := encodeRecord(record{Rev: 1, Name: "a", ETag: "x", Size: 1})
	b := encodeRecord(record{Rev: 2, Name: "b", ETag: "y", Size: 2})
	c := encodeRecord(record{Rev: 3, Name: "c", ETag: "z", Size: 3})
	complete := append(append([]byte{}, a...), b...)

	for name, tail := range map[string][]byte{
		"cut mid-JSON":        c[:len(c)/2],
		"no trailing newline": c[:len(c)-1],
	} {
		data := append(append([]byte{}, complete...), tail...)
		recs, n, err := parseLog(data)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(recs) != 2 || recs[0].Name != "a" || recs[1].Name != "b" {
			t.Fatalf("%s: records %+v", name, recs)
		}
		if n != len(complete) {
			t.Fatalf("%s: validLen %d, want %d", name, n, len(complete))
		}
	}
}

func TestParseLogCorruptMiddle(t *testing.T) {
	data := append([]byte("{\"r\":1,\"n\":\n"), encodeRecord(record{Rev: 2, Name: "b"})...)
	if _, _, err := parseLog(data); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("got %v, want ErrCorrupt", err)
	}
}

func TestParseLogEmpty(t *testing.T) {
	for _, data := range [][]byte{nil, {}} {
		recs, n, err := parseLog(data)
		if err != nil || len(recs) != 0 || n != 0 {
			t.Fatalf("got %v, %d, %v", recs, n, err)
		}
	}
}

func TestParseLogRevisionsMustIncrease(t *testing.T) {
	for _, revs := range [][2]uint64{{2, 2}, {3, 2}} {
		data := append(encodeRecord(record{Rev: revs[0], Name: "a"}), encodeRecord(record{Rev: revs[1], Name: "b"})...)
		if _, _, err := parseLog(data); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("revs %v: got %v, want ErrCorrupt", revs, err)
		}
	}
}
