// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package groupkit_test

import (
	"context"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// firstGoBlockUnder returns the first fenced Go code block after the heading
// with the given text and before the next heading of the same or a higher
// level.
func firstGoBlockUnder(t *testing.T, md, heading string) string {
	t.Helper()
	lines := strings.Split(md, "\n")
	level := 0
	inSection, inBlock := false, false
	var block []string
	for _, line := range lines {
		if inBlock {
			if strings.HasPrefix(line, "```") {
				return strings.Join(block, "\n") + "\n"
			}
			block = append(block, line)
			continue
		}
		if strings.HasPrefix(line, "#") {
			n := len(line) - len(strings.TrimLeft(line, "#"))
			text := strings.TrimSpace(line[n:])
			switch {
			case text == heading:
				inSection, level = true, n
			case inSection && n <= level:
				return ""
			}
			continue
		}
		if inSection && strings.TrimSpace(line) == "```go" {
			inBlock = true
		}
	}
	return ""
}

func snippetEnv() []string {
	return append(os.Environ(), "GOFLAGS=-mod=mod", "GOPROXY=off", "GOWORK=off")
}

// snippetModule writes src as main.go into a temporary module that reuses the
// dependency versions of the working tree and replaces groupkit with the
// working tree, so nothing is downloaded.
func snippetModule(t *testing.T, src string) string {
	t.Helper()
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	gomod, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatal(err)
	}
	gosum, err := os.ReadFile("go.sum")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	mod := strings.Replace(string(gomod), "module github.com/LukasHirt/groupkit", "module readmecheck", 1)
	mod += "\nrequire github.com/LukasHirt/groupkit v0.0.0\n\nreplace github.com/LukasHirt/groupkit => " + root + "\n"
	for name, data := range map[string]string{"go.mod": mod, "go.sum": string(gosum), "main.go": src} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestREADMESnippetCompiles(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the go tool")
	}
	readme, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	src := firstGoBlockUnder(t, string(readme), "Mounting it")
	if src == "" {
		t.Fatal(`README.md has no Go code block under the heading "Mounting it"`)
	}
	if !strings.HasPrefix(src, "package main") {
		t.Fatal("the first Go block under \"Mounting it\" must be a complete main package")
	}

	dir := snippetModule(t, src)
	cmd := exec.Command("go", "vet", "./...")
	cmd.Dir = dir
	cmd.Env = snippetEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the README snippet does not compile: %v\n%s\n--- snippet ---\n%s", err, out, src)
	}
}

// TestREADMESnippetChallenges builds the snippet, runs it on a free port and
// checks the answers an embedding program would give: a request without
// credentials gets a 401 with a challenge even for a calendar URL, and a user
// cannot reach another user's URL.
func TestREADMESnippetChallenges(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs a program")
	}
	readme, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	src := firstGoBlockUnder(t, string(readme), "Mounting it")
	const listen = "127.0.0.1:8080"
	if !strings.Contains(src, listen) {
		t.Fatalf("the snippet does not listen on %s", listen)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	dir := snippetModule(t, strings.Replace(src, listen, addr, 1))

	build := exec.Command("go", "build", "-o", "snippet", ".")
	build.Dir = dir
	build.Env = snippetEnv()
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	ctx, cancel := context.WithCancel(context.Background())
	run := exec.CommandContext(ctx, filepath.Join(dir, "snippet"))
	run.Dir = dir
	if err := run.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); _ = run.Wait() })

	do := func(user, target string) *http.Response {
		t.Helper()
		var resp *http.Response
		for range 100 {
			req, _ := http.NewRequest("PROPFIND", "http://"+addr+target, nil)
			if user != "" {
				req.SetBasicAuth(user, "x")
			}
			resp, err = http.DefaultClient.Do(req)
			if err == nil {
				_ = resp.Body.Close()
				return resp
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("the snippet did not answer: %v", err)
		return nil
	}
	if resp := do("", "/dav/calendars/alice/"); resp.StatusCode != http.StatusUnauthorized ||
		resp.Header.Get("WWW-Authenticate") == "" {
		t.Errorf("no credentials: status %d, challenge %q", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
	}
	if resp := do("bob", "/dav/calendars/alice/"); resp.StatusCode != http.StatusForbidden {
		t.Errorf("another user: status %d, want 403", resp.StatusCode)
	}
}
