package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const defaultReadLimit = 2000

func ReadFile() *Tool {
	return &Tool{
		Name: "read_file",
		Description: "Read a text file. Returns the lines with line numbers (1-based). " +
			"Use this to look at a file before you edit it. For big files, use offset and limit to read one part.",
		Schema: `{
  "type": "object",
  "properties": {
    "path":   {"type": "string",  "description": "File path, relative to the working directory or absolute."},
    "offset": {"type": "integer", "description": "First line to read (1-based). Default 1."},
    "limit":  {"type": "integer", "description": "How many lines to read. Default 2000."}
  },
  "required": ["path"]
}`,
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var a struct {
				Path   string `json:"path"`
				Offset int    `json:"offset"`
				Limit  int    `json:"limit"`
			}
			if err := decode(raw, &a); err != nil {
				return "", err
			}
			if a.Path == "" {
				return "", errors.New("path is required")
			}
			offset := max(a.Offset, 1)
			limit := a.Limit
			if limit <= 0 {
				limit = defaultReadLimit
			}

			f, err := os.Open(a.Path)
			if err != nil {
				return "", err
			}
			defer f.Close()

			var b strings.Builder
			sc := bufio.NewScanner(f)
			sc.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
			n, shown := 0, 0
			for sc.Scan() {
				n++
				if n < offset || shown >= limit {
					continue
				}
				fmt.Fprintf(&b, "%6d\t%s\n", n, sc.Text())
				shown++
			}
			if err := sc.Err(); err != nil {
				return "", err
			}
			if n == 0 {
				return "(empty file)", nil
			}
			if shown == 0 {
				return "", fmt.Errorf("offset %d is past the end of the file (%d lines)", offset, n)
			}
			if shown < n {
				fmt.Fprintf(&b, "\n[showed lines %d-%d of %d]", offset, offset+shown-1, n)
			}
			return truncate(b.String()), nil
		},
	}
}

const maxListEntries = 1000

// skipDirs are never listed: they are big and almost never useful to the model.
var skipDirs = map[string]bool{".git": true, "node_modules": true, ".venv": true}

func ListFiles() *Tool {
	return &Tool{
		Name: "list_files",
		Description: "List files and directories under a path, recursively. Directories end with '/'. " +
			"Skips .git and node_modules. Use this to learn the layout of a project.",
		Schema: `{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "Directory to list. Default is the working directory."}
  }
}`,
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var a struct {
				Path string `json:"path"`
			}
			if len(raw) > 0 {
				if err := decode(raw, &a); err != nil {
					return "", err
				}
			}
			root := a.Path
			if root == "" {
				root = "."
			}

			var out []string
			cut := false
			err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if p == root {
					return nil
				}
				if d.IsDir() && skipDirs[d.Name()] {
					return filepath.SkipDir
				}
				if len(out) >= maxListEntries {
					cut = true
					return filepath.SkipAll
				}
				rel, _ := filepath.Rel(root, p)
				if d.IsDir() {
					rel += "/"
				}
				out = append(out, rel)
				return nil
			})
			if err != nil {
				return "", err
			}
			if len(out) == 0 {
				return "(empty directory)", nil
			}
			s := strings.Join(out, "\n")
			if cut {
				s += fmt.Sprintf("\n\n[stopped after %d entries; list a subdirectory to see more]", maxListEntries)
			}
			return truncate(s), nil
		},
	}
}

func EditFile() *Tool {
	return &Tool{
		Name: "edit_file",
		Description: "Edit a file by replacing text. old_str must match the file content exactly, " +
			"including whitespace, and must appear exactly once. Include enough lines around the change to make it unique. " +
			"To create a new file, pass an empty old_str and the full content as new_str.",
		Schema: `{
  "type": "object",
  "properties": {
    "path":    {"type": "string", "description": "File path."},
    "old_str": {"type": "string", "description": "Exact text to replace. Empty to create a new file."},
    "new_str": {"type": "string", "description": "Text to put in its place."}
  },
  "required": ["path", "old_str", "new_str"]
}`,
		NeedsApproval: true,
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var a struct {
				Path   string `json:"path"`
				OldStr string `json:"old_str"`
				NewStr string `json:"new_str"`
			}
			if err := decode(raw, &a); err != nil {
				return "", err
			}
			if a.Path == "" {
				return "", errors.New("path is required")
			}

			if a.OldStr == "" {
				if _, err := os.Stat(a.Path); err == nil {
					return "", fmt.Errorf("%s already exists; pass old_str to edit it", a.Path)
				}
				if err := os.MkdirAll(filepath.Dir(a.Path), 0o755); err != nil {
					return "", err
				}
				if err := os.WriteFile(a.Path, []byte(a.NewStr), 0o644); err != nil {
					return "", err
				}
				return fmt.Sprintf("created %s (%d bytes)", a.Path, len(a.NewStr)), nil
			}

			data, err := os.ReadFile(a.Path)
			if err != nil {
				return "", err
			}
			content := string(data)
			switch n := strings.Count(content, a.OldStr); n {
			case 0:
				return "", errors.New("old_str not found in file; read the file again and copy the text exactly")
			case 1:
			default:
				return "", fmt.Errorf("old_str appears %d times; add more surrounding lines to make it unique", n)
			}
			info, err := os.Stat(a.Path)
			if err != nil {
				return "", err
			}
			content = strings.Replace(content, a.OldStr, a.NewStr, 1)
			if err := os.WriteFile(a.Path, []byte(content), info.Mode().Perm()); err != nil {
				return "", err
			}
			return "edited " + a.Path, nil
		},
	}
}
