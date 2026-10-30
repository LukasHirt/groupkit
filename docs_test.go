// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package groupkit_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var (
	claimLine = regexp.MustCompile(`^<!-- claim: (\w+) -->$`)
	// limitToken matches the numeric limits that must carry a claim marker.
	limitToken = regexp.MustCompile(`\b(10 MiB|1 MiB|64 levels)\b`)
)

// markdownFiles returns README.md and the Markdown files in docs/.
func markdownFiles(t *testing.T) []string {
	t.Helper()
	pages, err := filepath.Glob(filepath.Join("docs", "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	return append([]string{"README.md"}, pages...)
}

// moduleTests lists the names of every test, benchmark and fuzz target in
// the module.
func moduleTests(t *testing.T) map[string]bool {
	t.Helper()
	out, err := exec.Command("go", "test", "-list", ".*", "./...").Output()
	if err != nil {
		t.Fatalf("go test -list: %v", err)
	}
	names := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Test") || strings.HasPrefix(line, "Fuzz") {
			names[line] = true
		}
	}
	return names
}

// TestDocsClaims checks that every claim marker in the documentation names a
// test that exists, and that every numeric limit the documentation names has a
// marker on the same line or the previous non-empty line.
func TestDocsClaims(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the go tool")
	}
	tests := moduleTests(t)
	if len(tests) == 0 {
		t.Fatal("no tests found in the module")
	}
	markers := 0
	for _, file := range markdownFiles(t) {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		prevIsClaim := false
		inFence := false
		for i, line := range strings.Split(string(data), "\n") {
			where := fmt.Sprintf("%s:%d", file, i+1)
			if m := claimLine.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
				markers++
				if !tests[m[1]] {
					t.Errorf("%s: claim names %s, which is not a test in this module", where, m[1])
				}
				prevIsClaim = true
				continue
			}
			if strings.HasPrefix(strings.TrimSpace(line), "```") {
				inFence = !inFence
			}
			if strings.TrimSpace(line) == "" {
				continue // blank lines do not reset the previous marker
			}
			if !inFence && limitToken.MatchString(line) && !prevIsClaim && !strings.Contains(line, "<!-- claim:") {
				t.Errorf("%s: limit %q has no claim marker on the same or the previous line", where, limitToken.FindString(line))
			}
			prevIsClaim = false
		}
	}
	if markers == 0 {
		t.Error("the documentation has no claim markers")
	}
}
