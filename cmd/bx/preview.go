package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// preview.go — bx preview <tile> | --screen <name>: the page as the caller
// sees it, rendered by xbind's preview browser (POST /api/xbin/preview,
// docs/protocol/preview.md, D234). In a tile's terminal it renders that tile
// for the terminal's person; with a person's own credential (XBIN_TOKEN = an
// app sign-in's bearer), any tile or screen they see. The root token is
// nobody's view. (bx preview --native <tile> is the native runtime's
// reference picture: native.go.)

const previewUsage = `  bx preview [<tile>] [--viewport WxH] [--out f.png] [--text]   the tile as you see it: a picture + a text rendering
     bx preview --screen <name|id> [--tile <tile>] …  a screen of the shell   --shell: the tile on its screen
     --wait <ms>  --full (whole page)  --json (the whole answer)  --status (can this xbind render?)
`

func cmdPreviewPage(args []string) error {
	body := map[string]any{}
	out, text, asJSON := "", false, false
	for i := 0; i < len(args); i++ {
		next := func() (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("bx preview: %s needs a value", args[i])
			}
			i++
			return args[i], nil
		}
		var err error
		var v string
		switch a := args[i]; a {
		case "--screen", "--tile", "--at":
			if v, err = next(); err == nil {
				body[strings.TrimPrefix(a, "--")] = v
			}
		case "--viewport", "--size":
			if v, err = next(); err == nil {
				w, h, ok := strings.Cut(strings.ToLower(v), "x")
				wn, e1 := strconv.Atoi(w)
				hn, e2 := strconv.Atoi(h)
				if !ok || e1 != nil || e2 != nil {
					return fmt.Errorf("bx preview: --viewport wants WxH, e.g. 390x844")
				}
				body["viewport"] = map[string]int{"width": wn, "height": hn}
			}
		case "--wait":
			if v, err = next(); err == nil {
				n, e := strconv.Atoi(v)
				if e != nil {
					return fmt.Errorf("bx preview: --wait wants milliseconds")
				}
				body["wait"] = n
			}
		case "--full":
			body["fullPage"] = true
		case "--shell":
			body["shell"] = true
		case "--text":
			text = true
		case "--json":
			asJSON = true
		case "--status":
			var st map[string]any
			if err := apiJSON("GET", "/api/xbin/preview", nil, &st); err != nil {
				return err
			}
			b, _ := json.MarshalIndent(st, "", "  ")
			fmt.Println(string(b))
			return nil
		case "--out", "-o":
			out, err = next()
		default:
			if strings.HasPrefix(a, "-") {
				return fmt.Errorf("bx preview: unknown flag %s\n%s", a, previewUsage)
			}
			body["tile"] = strings.Trim(a, "/")
		}
		if err != nil {
			return err
		}
	}
	if body["tile"] == nil && body["screen"] == nil {
		if c := os.Getenv("XBIN_COMPONENT"); c != "" { // a tile's terminal: that tile
			body["tile"] = c
		} else {
			return fmt.Errorf("bx preview: which tile? (bx preview <tile>, or --screen <name>)")
		}
	}
	var raw json.RawMessage
	apiTimeout = 3 * time.Minute // a render takes up to xbind's own limit (45 s by default)
	if err := apiJSON("POST", "/api/xbin/preview", body, &raw); err != nil {
		return err
	}
	var res struct {
		URL, Title, Snapshot, Notice, Browser, Ready string
		PNG                                          string `json:"png"`
		Width, Height                                int
		Frames                                       []struct{ URL, Snapshot string }
		Console, PageErrors, RequestsFailed          []map[string]any
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return fmt.Errorf("bx preview: the answer is unreadable: %v", err)
	}
	png, err := base64.StdEncoding.DecodeString(res.PNG)
	if err != nil {
		return fmt.Errorf("bx preview: the picture is unreadable: %v", err)
	}
	if out == "" {
		name, _ := body["tile"].(string)
		if name == "" {
			name, _ = body["screen"].(string)
		}
		out = filepath.Join(os.TempDir(), "bx-preview-"+strings.NewReplacer("/", "-", " ", "-").Replace(name)+".png")
	}
	if err := os.WriteFile(out, png, 0o644); err != nil {
		return err
	}
	if asJSON {
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		m["png"] = out // the picture is in the file
		b, _ := json.MarshalIndent(m, "", "  ")
		fmt.Println(string(b))
		return nil
	}
	fmt.Printf("%s  %dx%d  %s\n", out, res.Width, res.Height, res.URL)
	fmt.Printf("%d console errors/warnings, %d page errors, %d failed requests (%s)\n", len(res.Console), len(res.PageErrors), len(res.RequestsFailed), res.Browser)
	if text {
		fmt.Println("# " + res.Notice)
		fmt.Println(res.Snapshot)
		for _, f := range res.Frames {
			fmt.Printf("--- frame %s\n%s\n", f.URL, f.Snapshot)
		}
	}
	return nil
}
