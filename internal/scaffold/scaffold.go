// Package scaffold creates new components: manifest, view, backend skeleton
// for the chosen runtime, and the API.md contract when exposing. Shared by
// `bx new` and the component-creation API (POST /api/xbin/create) that
// tiles like tiles/manager build on. Never overwrites existing files.
package scaffold

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xbin-dev/xbin/internal/util"
)

type Options struct {
	Path    string // workspace-relative component path, e.g. "apps/thing"
	Runtime string // static (default) | go | node | python | cgi
	Expose  bool   // roles block + API.md skeleton
	Title   string // pretty name for the view; defaults to the path basename
}

// Create scaffolds the component under root. Returns the files it wrote.
func Create(root string, o Options) ([]string, error) {
	o.Path = strings.Trim(strings.TrimSpace(o.Path), "/")
	if o.Runtime == "" {
		o.Runtime = "static"
	}
	switch o.Runtime {
	case "static", "go", "node", "python", "cgi":
	default:
		return nil, fmt.Errorf("unknown runtime %q (static|go|node|python|cgi)", o.Runtime)
	}
	if !util.ComponentPathOK(o.Path) {
		return nil, fmt.Errorf("invalid component path %q (relative, no reserved names, no dot-dirs)", o.Path)
	}
	dir, _, err := util.SafeJoin(root, o.Path)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(dir, "xbin.json")); err == nil {
		return nil, fmt.Errorf("%s already has a xbin.json", o.Path)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	name := filepath.Base(o.Path)
	title := o.Title
	if strings.TrimSpace(title) == "" {
		title = name
	}

	var written []string
	write := func(rel, content string) error {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if _, err := os.Stat(p); err == nil {
			return nil // never overwrite
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		perm := os.FileMode(0o644)
		if rel == "backend/handler" {
			perm = 0o755 // cgi entry must be executable
		}
		if err := os.WriteFile(p, []byte(content), perm); err != nil {
			return err
		}
		written = append(written, o.Path+"/"+rel)
		return nil
	}

	var fields []string
	if o.Runtime != "static" {
		fields = append(fields, fmt.Sprintf("  \"runtime\": %q", o.Runtime))
	}
	if o.Expose {
		fields = append(fields, `  "expose": {
    "roles": {
      // Describe each role — descriptions show in the grants UI and bx api.
      "reader": "Read `+name+` data",
      "writer": "Modify `+name+` data"
    }
  }`)
	}
	manifest := "{}\n"
	if len(fields) > 0 {
		manifest = "{\n" + strings.Join(fields, ",\n") + "\n}\n"
	}
	if err := write("xbin.json", manifest); err != nil {
		return written, err
	}
	if err := write("index.html", fmt.Sprintf(indexTpl, htmlEscape(title))); err != nil {
		return written, err
	}

	switch o.Runtime {
	case "go":
		if err := write("go.mod", fmt.Sprintf(goModTpl, name)); err != nil {
			return written, err
		}
		if err := write("backend/main.go", goMainTpl); err != nil {
			return written, err
		}
	case "node":
		if err := write("backend/server.js", nodeTpl); err != nil {
			return written, err
		}
	case "python":
		if err := write("backend/server.py", pythonTpl); err != nil {
			return written, err
		}
	case "cgi":
		if err := write("backend/handler", cgiTpl); err != nil {
			return written, err
		}
	}
	if o.Expose {
		if err := write("API.md", fmt.Sprintf(apiMDTpl, o.Path, name)); err != nil {
			return written, err
		}
	}
	return written, nil
}

func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}
