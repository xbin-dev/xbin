package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// preview.go — bx preview <tile> | --screen <id>: the page as the caller
// sees it, rendered by xbind's preview browser (POST /api/xbin/preview).
// SPIKE (the service is behind XBIN_PREVIEW_SPIKE=1; usage and docs/bx.md
// follow with it). In a tile's terminal it renders that tile for the
// terminal's person; with a person's own credential, any page they see.
//
//	bx preview <tile> [--size WxH] [--wait ms] [--full] [--out f.png] [--text]
//	bx preview --screen <id> [--tile <tile>] …
func cmdPreviewPage(args []string) error {
	body := map[string]any{}
	out, text := "", false
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
		case "--size":
			if v, err = next(); err == nil {
				w, h, ok := strings.Cut(v, "x")
				wn, e1 := strconv.Atoi(w)
				hn, e2 := strconv.Atoi(h)
				if !ok || e1 != nil || e2 != nil {
					return fmt.Errorf("bx preview: --size wants WxH, e.g. 390x844")
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
		case "--text":
			text = true
		case "--out", "-o":
			out, err = next()
		default:
			if strings.HasPrefix(a, "-") {
				return fmt.Errorf("bx preview: unknown flag %s", a)
			}
			body["tile"] = strings.Trim(a, "/")
		}
		if err != nil {
			return err
		}
	}
	if body["tile"] == nil && body["screen"] == nil {
		if c := os.Getenv("XBIN_COMPONENT"); c != "" {
			body["tile"] = c
		} else {
			return fmt.Errorf("bx preview: which tile? (bx preview <tile>, or --screen <id>)")
		}
	}
	var res struct {
		URL, Title, Snapshot string
		PNG                  string `json:"png"`
		Width, Height        int
		Frames               []struct{ URL, Snapshot string }
		Console              []map[string]any
		PageErrors           []map[string]any `json:"page_errors"`
		RequestsFailed       []map[string]any `json:"requests_failed"`
	}
	if err := apiJSON("POST", "/api/xbin/preview", body, &res); err != nil {
		return err
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
		out = filepath.Join(os.TempDir(), "bx-preview-"+strings.ReplaceAll(name, "/", "-")+".png")
	}
	if err := os.WriteFile(out, png, 0o644); err != nil {
		return err
	}
	fmt.Printf("%s  %dx%d  %s\n", out, res.Width, res.Height, res.URL)
	fmt.Printf("%d console errors/warnings, %d page errors, %d failed requests\n", len(res.Console), len(res.PageErrors), len(res.RequestsFailed))
	if text {
		fmt.Println(res.Snapshot)
		for _, f := range res.Frames {
			fmt.Printf("--- frame %s\n%s\n", f.URL, f.Snapshot)
		}
	}
	return nil
}
