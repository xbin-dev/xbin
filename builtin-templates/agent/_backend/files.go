// files.go — the model-facing surface of the session files (files_store.go):
// file_write, file_read, file_edit, file_list, and render_html, which shows an
// .html file to the human in the tile's render pane.
//
// None of these is a sideEffect() tool. They mutate only this run's private
// sqlite rows — no world mutation, no egress, no reach into other components —
// so the approval gate in loop.go correctly never fires for them, and they are
// offered in BOTH capability lanes without widening either.
package main

import (
	"context"
	"fmt"
	"strings"
)

// fileToolSpecs describes the file tools. With the REPL on, the descriptions
// also point at it: files are where its reusable code lives, and it is how a
// chart gets drawn.
func fileToolSpecs(cfg Config) []toolSpec {
	boolProp := func(desc string) map[string]any {
		return map[string]any{"type": "boolean", "description": desc}
	}
	replHint, svgHint := "", ""
	if cfg.feature("repl") {
		replHint = " They are the durable half of the sandbox: put reusable functions here instead of re-pasting them, then js_run the file."
		svgHint = " (which you can generate with js_eval)"
	}
	return []toolSpec{
		{Type: "function", Function: funcDef{
			Name:        "file_write",
			Description: "Create or overwrite a session file — JavaScript, JSON, HTML, anything. Files belong to this run and survive across turns and backend restarts." + replHint,
			Parameters: obj([]string{"path", "content"}, map[string]any{
				"path":    strProp("file key, e.g. 'helpers.js', 'report.html', 'data/rows.json' (letters, digits, . _ - / ; max 128 chars)"),
				"content": strProp("the full file contents (max 64 KiB)"),
			}),
		}},
		{Type: "function", Function: funcDef{
			Name:        "file_read",
			Description: "Read a session file. Returns the whole file by default; pass offset/limit to read a line range from a file too large to return at once. Read before file_edit so your old_string matches exactly.",
			Parameters: obj([]string{"path"}, map[string]any{
				"path":   strProp("file key"),
				"offset": map[string]any{"type": "integer", "description": "1-based first line to return (default 1)"},
				"limit":  map[string]any{"type": "integer", "description": "how many lines to return (default: to the end)"},
			}),
		}},
		{Type: "function", Function: funcDef{
			Name:        "file_edit",
			Description: "Replace an exact string in a session file. old_string must appear EXACTLY ONCE — include surrounding lines to disambiguate — unless replace_all is true.",
			Parameters: obj([]string{"path", "old_string", "new_string"}, map[string]any{
				"path":        strProp("file key"),
				"old_string":  strProp("exact text to replace, including indentation"),
				"new_string":  strProp("replacement text (empty string deletes it)"),
				"replace_all": boolProp("replace every occurrence instead of requiring exactly one"),
			}),
		}},
		{Type: "function", Function: funcDef{
			Name:        "file_list",
			Description: "List this run's session files with their sizes and versions.",
			Parameters:  obj(nil, map[string]any{}),
		}},
		{Type: "function", Function: funcDef{
			Name: "render_html",
			Description: "Shows the human a STATIC snapshot of an HTML file — not a browser: scripts never run and external loads are blocked, so it cannot verify JavaScript; use browser_check for that, or preview_port for a live page. " +
				"The file is a session .html file, shown in the tile's preview pane: inline your CSS, and draw charts as inline SVG" + svgHint + " rather than using a chart library (no external images, stylesheets or fonts load). " +
				"Use this whenever a table, report, diagram or comparison would read better than prose.",
			Parameters: obj([]string{"path"}, map[string]any{
				"path": strProp("file key of the HTML file to show, e.g. 'report.html'"),
			}),
		}},
	}
}

var fileToolNames = map[string]bool{
	"file_write": true, "file_read": true, "file_edit": true, "file_list": true,
	"render_html": true, "file_view": true,
}

// runFileTool dispatches the file tools. Called from runTool.
func (ag *Agent) runFileTool(ctx context.Context, run *Run, cfg Config, name string, args map[string]any) (string, error) {
	switch name {
	case "file_write":
		path, _ := args["path"].(string)
		content, _ := args["content"].(string)
		f, err := ag.db.replPutFile(run.ID, path, content, 0)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("wrote %s (%s, v%d)\n\n%s",
			f.Path, humanBytes(f.Bytes), f.Version, ag.db.replFileIndex(run.ID)), nil

	case "file_read":
		path, err := normReplPath(str(args["path"]))
		if err != nil {
			return "", err
		}
		f, err := ag.db.replFile(run.ID, path)
		if err != nil {
			return "", err
		}
		if f.Binary {
			// Never paste binary into the transcript: it would ride every
			// later call until compaction, and it is meaningless as text.
			hint := "it cannot be read as text"
			if visionMimes[f.Mime] {
				hint = "use file_view to look at it"
			}
			return fmt.Sprintf("%s is a binary attachment (%s, %s) — %s.", path, f.Mime, humanBytes(f.Bytes), hint), nil
		}
		return sliceLines(f.Content, toInt(args["offset"]), toInt(args["limit"])), nil

	case "file_view":
		return ag.toolFileView(run, cfg, args)

	case "file_edit":
		return ag.fileEdit(run.ID, args)

	case "file_list":
		return ag.db.replFileIndex(run.ID), nil

	case "render_html":
		return ag.renderHTML(run.ID, args)
	}
	return "", fmt.Errorf("unknown file tool %q", name)
}

func str(v any) string { s, _ := v.(string); return s }

// fileEdit is file_edit: an exact-string replacement, refusing to guess.
func (ag *Agent) fileEdit(runID int64, args map[string]any) (string, error) {
	path, err := normReplPath(str(args["path"]))
	if err != nil {
		return "", err
	}
	oldS, newS := str(args["old_string"]), str(args["new_string"])
	if err := checkEdit(oldS, newS, "file_write"); err != nil {
		return "", err
	}
	f, err := ag.db.replFile(runID, path)
	if err != nil {
		return "", err
	}
	if f.Binary {
		return "", fmt.Errorf("%s is a binary attachment (%s) and cannot be edited as text", path, f.Mime)
	}
	all, _ := args["replace_all"].(bool)
	updated, n, err := applyEdit(f.Content, oldS, newS, all, path, "file_read")
	if err != nil {
		return "", err
	}
	nf, err := ag.db.replPutFile(runID, path, updated, 0)
	if err != nil {
		return "", err
	}
	what := "1 replacement"
	if all && n > 1 {
		what = fmt.Sprintf("%d replacements", n)
	}
	return fmt.Sprintf("edited %s (%s, %s, v%d)\n\n%s",
		nf.Path, what, humanBytes(nf.Bytes), nf.Version, ag.db.replFileIndex(runID)), nil
}

// checkEdit refuses an edit that can't mean anything; writeTool is the tool
// that replaces a whole file.
func checkEdit(oldS, newS, writeTool string) error {
	if oldS == "" {
		return fmt.Errorf("old_string is required (use %s to create or replace a whole file)", writeTool)
	}
	if oldS == newS {
		return fmt.Errorf("old_string and new_string are identical — nothing to do")
	}
	return nil
}

// applyEdit is an exact-string replacement, refusing to guess: oldS must
// appear exactly once in content, or all replaces every one. n is how many
// it found. readTool is the tool the model re-reads the file with (file_edit
// and the sandbox's edit share this).
func applyEdit(content, oldS, newS string, all bool, path, readTool string) (updated string, n int, err error) {
	n = strings.Count(content, oldS)
	switch {
	case n == 0:
		hint := ""
		// A near-miss is almost always whitespace, so point at it rather than
		// letting the model guess: it can then re-read and retry precisely.
		if squeeze := strings.Join(strings.Fields(oldS), " "); squeeze != "" &&
			strings.Contains(strings.Join(strings.Fields(content), " "), squeeze) {
			hint = " — the text is present but the whitespace differs; " + readTool + " it and copy exactly"
		}
		return "", 0, fmt.Errorf("old_string not found in %s%s", path, hint)
	case n > 1 && !all:
		return "", n, fmt.Errorf("old_string appears %d times in %s — add surrounding lines to make it unique, or set replace_all", n, path)
	}
	if all {
		return strings.ReplaceAll(content, oldS, newS), n, nil
	}
	return strings.Replace(content, oldS, newS, 1), n, nil
}

// renderHTML journals a render step; the tile picks it up from the run's step
// list on its next poll. Only metadata is journaled — the HTML itself is
// fetched on demand, because steps ride the frontend's 1.5s poll.
func (ag *Agent) renderHTML(runID int64, args map[string]any) (string, error) {
	path, err := normReplPath(str(args["path"]))
	if err != nil {
		return "", err
	}
	f, err := ag.db.replFile(runID, path)
	if err != nil {
		return "", err
	}
	if !strings.HasSuffix(strings.ToLower(path), ".html") && !strings.HasSuffix(strings.ToLower(path), ".htm") {
		return "", fmt.Errorf("render_html needs an .html file; %q is not one (file_write it as .html first)", path)
	}
	ag.db.journal(runID, "render", map[string]any{
		"path": f.Path, "version": f.Version, "bytes": f.Bytes,
	})
	return fmt.Sprintf("rendered %s (%s, v%d) — shown to the human in the tile's preview pane as a static snapshot: "+
		"no scripts ran and every external load (script, image, stylesheet, font) was blocked, so this says nothing about whether its JavaScript works — "+
		"browser_check runs it in a real browser, preview_port shows a live page.",
		f.Path, humanBytes(f.Bytes), f.Version), nil
}
