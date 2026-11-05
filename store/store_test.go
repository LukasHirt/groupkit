// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateName(t *testing.T) {
	// Names are chosen by clients, so anything that is a sane path segment is
	// accepted. A store that needs a different on-disk form encodes names itself.
	valid := []string{"event.ics", "a", "Příklad čeština.vcf", "x y", ".hidden", "a:b", `a\b`, "12345@example.com.ics", strings.Repeat("a", 255)}
	for _, name := range valid {
		if err := ValidateName(name); err != nil {
			t.Errorf("ValidateName(%q) = %v, want nil", name, err)
		}
	}

	invalid := map[string]string{
		"empty":          "",
		"dot":            ".",
		"dot dot":        "..",
		"slash":          "a/b",
		"nul":            "a\x00b",
		"control":        "a\nb",
		"invalid utf-8":  "a\xffb",
		"too long":       strings.Repeat("a", 256),
		"trailing slash": "dir/",
	}
	for desc, name := range invalid {
		err := ValidateName(name)
		if !errors.Is(err, ErrInvalidName) {
			t.Errorf("%s: ValidateName(%q) = %v, want ErrInvalidName", desc, name, err)
		}
	}
}

func TestValidateOwner(t *testing.T) {
	// Owners are opaque identifiers such as an OIDC subject, so almost anything
	// printable is fine, including characters that are invalid in object names.
	valid := []string{"alice", "Vs9saZ-k3RAkNbpg@H8CwklAJQcKYVOsVNNKwI61JKymq9", "a/b", ".hidden", "a b"}
	for _, owner := range valid {
		if err := ValidateOwner(owner); err != nil {
			t.Errorf("ValidateOwner(%q) = %v, want nil", owner, err)
		}
	}
	for _, owner := range []string{"", "a\x00b", "a\nb", "a\xffb", strings.Repeat("a", 1025)} {
		if err := ValidateOwner(owner); !errors.Is(err, ErrInvalidName) {
			t.Errorf("ValidateOwner(%q) = %v, want ErrInvalidName", owner, err)
		}
	}
}

func TestETag(t *testing.T) {
	a := ETag([]byte("BEGIN:VCARD\r\nEND:VCARD\r\n"))
	if a != ETag([]byte("BEGIN:VCARD\r\nEND:VCARD\r\n")) {
		t.Error("ETag is not deterministic")
	}
	if a == ETag([]byte("BEGIN:VCARD\r\nFN:x\r\nEND:VCARD\r\n")) {
		t.Error("different content produced the same ETag")
	}
	if len(a) != 32 || strings.ContainsAny(a, "\"/ ") {
		t.Errorf("ETag %q should be 32 hex characters without quotes", a)
	}
	if ETag(nil) == "" {
		t.Error("ETag of empty content must not be empty")
	}
}

func TestAccessLevels(t *testing.T) {
	// The zero value is full access, so a store that knows nothing about
	// callers grants everything without setting the field.
	var zero Access
	if zero != AccessFull {
		t.Errorf("zero Access = %v, want AccessFull", zero)
	}
	cases := []struct {
		level          Access
		objects, props bool
		name           string
	}{
		{AccessFull, true, true, "full"},
		{AccessObjects, true, false, "objects"},
		{AccessRead, false, false, "read"},
	}
	for _, c := range cases {
		if got := c.level.AllowsObjectWrites(); got != c.objects {
			t.Errorf("%v.AllowsObjectWrites() = %v, want %v", c.level, got, c.objects)
		}
		if got := c.level.AllowsPropertyWrites(); got != c.props {
			t.Errorf("%v.AllowsPropertyWrites() = %v, want %v", c.level, got, c.props)
		}
		if got := c.level.String(); got != c.name {
			t.Errorf("%d.String() = %q, want %q", int(c.level), got, c.name)
		}
	}
	// An unknown level is treated as the most restrictive one and prints its number.
	odd := Access(42)
	if odd.AllowsObjectWrites() || odd.AllowsPropertyWrites() {
		t.Error("an unknown level allows writes")
	}
	if got := odd.String(); got != "access(42)" {
		t.Errorf("Access(42).String() = %q", got)
	}
}

func TestConflictErrorUnwraps(t *testing.T) {
	err := error(&ConflictError{UID: "u1", Name: "other.ics"})
	if !errors.Is(err, ErrUIDConflict) {
		t.Error("ConflictError should match ErrUIDConflict")
	}
	var ce *ConflictError
	if !errors.As(err, &ce) || ce.Name != "other.ics" {
		t.Error("errors.As should recover the ConflictError")
	}
}
