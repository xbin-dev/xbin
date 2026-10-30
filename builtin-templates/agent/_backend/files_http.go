// files_http.go — session files as downloads (D207): one file as an
// attachment (GET /runs/{id}/dl/<path…>, the route a report's file links
// reach — path-shaped, so an xbind path ticket for runs/{id}/dl covers it),
// and several as one zip (GET /runs/{id}/zip?path=…), streamed: one file's
// bytes in memory at a time, never the whole selection. Both have /raw's
// access: a viewer of the run.
package main

import (
	"archive/zip"
	"fmt"
	"log"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// attachment is a Content-Disposition that downloads as name.
func attachment(name string) string {
	if d := mime.FormatMediaType("attachment", map[string]string{"filename": name}); d != "" {
		return d
	}
	return "attachment"
}

// downloadHeaders: what every download answers with — never sniffed, never
// cached, never shown in place.
func downloadHeaders(w http.ResponseWriter, ctype, name string) {
	h := w.Header()
	h.Set("Content-Type", ctype)
	h.Set("Content-Disposition", attachment(name))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
}

// handleDownload serves a session file as an attachment named by its base
// name: GET /runs/{id}/dl/<path…>.
func handleDownload(w http.ResponseWriter, r *http.Request) {
	p, err := normReplPath(r.PathValue("path"))
	if err != nil {
		xbin.WriteError(w, 400, err.Error())
		return
	}
	f, err := agent.db.replFile(pathID(r), p)
	if err != nil {
		xbin.WriteError(w, 404, err.Error())
		return
	}
	data, err := agent.fileBytes(r.Context(), f)
	if err != nil {
		xbin.WriteError(w, 502, err.Error())
		return
	}
	downloadHeaders(w, orStr(f.Mime, "text/plain; charset=utf-8"), path.Base(f.Path))
	_, _ = w.Write(data)
}

// stored: content a deflate pass would not shrink (images, archives and the
// Office formats, which are zips) — stored as is.
func stored(m string) bool {
	switch {
	case strings.HasPrefix(m, "image/") && m != "image/svg+xml", strings.HasPrefix(m, "video/"), strings.HasPrefix(m, "audio/"):
		return true
	}
	switch m {
	case "application/zip", "application/gzip", "application/x-gzip", "application/x-7z-compressed", "application/pdf",
		mimeDocx, mimeXlsx, mimePptx:
		return true
	}
	return false
}

// zipName: the archive's file name — ?name= (one segment, .zip added), else
// run-<id>-files.zip.
func zipName(id int64, asked string) string {
	n := sanitizeUploadName(path.Base(strings.TrimSpace(asked)))
	if asked == "" || n == "upload" {
		return fmt.Sprintf("run-%d-files.zip", id)
	}
	if !strings.HasSuffix(strings.ToLower(n), ".zip") {
		n += ".zip"
	}
	return n
}

// handleZip streams the session files named by ?path= (repeated; none: all
// of them) as one zip, each under its key. Every path is checked before the
// first byte goes out (400 a bad one, 404 a missing one); a file that fails
// to read after that aborts the response, so a broken archive never looks
// like a whole one.
func handleZip(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	q := r.URL.Query()
	var files []*ReplFile
	if asked := q["path"]; len(asked) > 0 {
		if len(asked) > maxReplFiles {
			xbin.WriteError(w, 400, fmt.Sprintf("at most %d paths", maxReplFiles))
			return
		}
		seen := map[string]bool{}
		for _, a := range asked {
			p, err := normReplPath(a)
			if err != nil {
				xbin.WriteError(w, 400, err.Error())
				return
			}
			if seen[p] {
				continue
			}
			seen[p] = true
			f, err := agent.db.replFile(id, p)
			if err != nil {
				xbin.WriteError(w, 404, err.Error())
				return
			}
			f.Content = "" // read again as the archive is written: one file in memory at a time
			files = append(files, f)
		}
	} else {
		all, err := agent.db.replFiles(id)
		if err != nil {
			xbin.WriteError(w, 500, err.Error())
			return
		}
		files = all
	}
	if len(files) == 0 {
		xbin.WriteError(w, 404, "no session files")
		return
	}
	downloadHeaders(w, "application/zip", zipName(id, q.Get("name")))
	zw := zip.NewWriter(w)
	for _, f := range files {
		var data []byte
		var err error
		if f.Binary {
			data, err = agent.blobs.Get(r.Context(), f.Blob) // not through the cache: a zip is read once
		} else {
			var full *ReplFile
			if full, err = agent.db.replFile(id, f.Path); err == nil {
				data = []byte(full.Content)
			}
		}
		if err != nil {
			log.Printf("agent: zip of run %d: %s: %v (response aborted)", id, f.Path, err)
			panic(http.ErrAbortHandler)
		}
		method := zip.Deflate
		if stored(f.Mime) {
			method = zip.Store
		}
		fw, err := zw.CreateHeader(&zip.FileHeader{Name: f.Path, Method: method, Modified: time.Unix(f.Updated, 0).UTC()})
		if err == nil {
			_, err = fw.Write(data)
		}
		if err != nil { // the client went away
			return
		}
	}
	_ = zw.Close()
}
