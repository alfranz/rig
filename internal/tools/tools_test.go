package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func call(t *testing.T, tool *Tool, args string) (string, error) {
	t.Helper()
	return tool.Run(context.Background(), []byte(args))
}

func TestDefaultSchemasAreValid(t *testing.T) {
	r := Default() // panics on invalid JSON
	if got := len(r.Definitions()); got != 5 {
		t.Fatalf("got %d tools", got)
	}
	if _, err := r.Subset([]string{"read_file", "nope"}); err == nil {
		t.Fatal("want error for unknown tool")
	}
}

func TestEditFileFlow(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "a.txt")
	tool := EditFile()

	if _, err := call(t, tool, `{"path":"`+p+`","old_str":"","new_str":"one\ntwo\ntwo\n"}`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := call(t, tool, `{"path":"`+p+`","old_str":"","new_str":"x"}`); err == nil {
		t.Fatal("create over existing file: want error")
	}
	if _, err := call(t, tool, `{"path":"`+p+`","old_str":"two","new_str":"2"}`); err == nil || !strings.Contains(err.Error(), "2 times") {
		t.Fatalf("ambiguous edit: err = %v", err)
	}
	if _, err := call(t, tool, `{"path":"`+p+`","old_str":"missing","new_str":"2"}`); err == nil {
		t.Fatal("missing old_str: want error")
	}
	if _, err := call(t, tool, `{"path":"`+p+`","old_str":"one\n","new_str":"1\n"}`); err != nil {
		t.Fatalf("edit: %v", err)
	}
	data, _ := os.ReadFile(p)
	if string(data) != "1\ntwo\ntwo\n" {
		t.Fatalf("content = %q", data)
	}

	out, err := call(t, ReadFile(), `{"path":"`+p+`","offset":2,"limit":1}`)
	if err != nil || !strings.Contains(out, "     2\ttwo") || !strings.Contains(out, "[showed lines 2-2 of 3]") {
		t.Fatalf("read_file = %q, %v", out, err)
	}
}

func TestBadArguments(t *testing.T) {
	if _, err := call(t, ReadFile(), `{"path":`); err == nil || !strings.Contains(err.Error(), "invalid arguments JSON") {
		t.Fatalf("err = %v", err)
	}
}

func TestBashAndSearch(t *testing.T) {
	out, err := call(t, Bash(), `{"command":"echo hi; exit 3"}`)
	if err != nil || !strings.Contains(out, "hi") || !strings.Contains(out, "[exit code 3]") {
		t.Fatalf("bash = %q, %v", out, err)
	}

	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "x.go"), []byte("package x\nfunc Hello() {}\n"), 0o644)
	out, err = call(t, CodeSearch(), `{"pattern":"func Hel+o","path":"`+dir+`"}`)
	if err != nil || !strings.Contains(out, "x.go:2:func Hello") {
		t.Fatalf("code_search = %q, %v", out, err)
	}
	out, err = call(t, CodeSearch(), `{"pattern":"nothing_here_xyz","path":"`+dir+`"}`)
	if err != nil || out != "no matches" {
		t.Fatalf("code_search no match = %q, %v", out, err)
	}

	out, err = call(t, ListFiles(), `{"path":"`+dir+`"}`)
	if err != nil || out != "x.go" {
		t.Fatalf("list_files = %q, %v", out, err)
	}
}
