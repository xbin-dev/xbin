// mcp_content.go — what an MCP tool's result holds besides text. A tool
// returns a file as an embedded resource ({type:"resource", resource:{uri,
// mimeType, blob}}), an image or an audio block ({data, mimeType}), or names
// one with a resource_link; mcpCall used to keep only the text blocks and
// drop the rest without a word (and, with no text at all, hand the model the
// raw JSON, base64 and all).
//
// Now, in a session with files, every block that carries bytes is saved as a
// session file under mcp/<server>/ — putFileData: the same bytes at the same
// name write nothing, different bytes become its next version — and the
// model reads one line per block after the server's own text: the path, the
// type, the size, the sha256 and how to read it. The bytes never reach the
// model's context. A block over a limit, or whose base64 doesn't decode, is
// named with why it wasn't kept; the rest of the result still arrives. A
// session without files (and the workspace agent, ws_tools_mcp.go) gets the
// same lines saying what was returned and that it can't be kept.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

var (
	mcpBlockMax    = maxBinaryFileBytes // one block's bytes (the session file cap)
	mcpResultMax   = 16 << 20           // all the bytes one result keeps
	mcpResultFiles = 16                 // files one result keeps
	mcpCallReadMax = 24 << 20           // what is read of a tools/call answer: mcpResultMax as base64, and room
)

const (
	mcpInlineText  = 4 << 10 // an embedded text resource this small is shown, not saved
	mcpInlineTotal = 8 << 10 // all of one result's inline text (a tool result is capped at maxToolResult)
	// mcpShownLead lists the images a result shows a vision model (attach.go,
	// shownImages): only ones this very call saved (withImages checks).
	mcpShownLead = "images (shown to you after these tool results): "
)

// mcpBlock is one content block of a tools/call result. It decodes
// leniently: a field of an unexpected type reads as empty, never failing the
// whole result.
type mcpBlock struct {
	Type, Text, Data, MimeType string
	URI, Name, Title           string
	Size                       int64
	Resource                   *mcpResource
}

type mcpResource struct {
	URI, MimeType, Name, Title string
	Text, Blob                 *string
}

func (b *mcpBlock) UnmarshalJSON(raw []byte) error {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		b.Type = "(unreadable)"
		return nil
	}
	s := func(m map[string]json.RawMessage, k string) string {
		var v string
		_ = json.Unmarshal(m[k], &v)
		return v
	}
	b.Type, b.Text, b.Data, b.MimeType = s(m, "type"), s(m, "text"), s(m, "data"), s(m, "mimeType")
	b.URI, b.Name, b.Title = s(m, "uri"), s(m, "name"), s(m, "title")
	var n float64
	if json.Unmarshal(m["size"], &n) == nil && n > 0 {
		b.Size = int64(n)
	}
	var rm map[string]json.RawMessage
	if r, ok := m["resource"]; ok && json.Unmarshal(r, &rm) == nil {
		res := &mcpResource{URI: s(rm, "uri"), MimeType: s(rm, "mimeType"), Name: s(rm, "name"), Title: s(rm, "title")}
		for k, dst := range map[string]**string{"text": &res.Text, "blob": &res.Blob} {
			var v string
			if raw, ok := rm[k]; ok && json.Unmarshal(raw, &v) == nil {
				*dst = &v
			}
		}
		b.Resource = res
	}
	return nil
}

// mcpResultText is a tools/call result as the model reads it: the text
// blocks joined, as before, then a line for each other block.
func (ag *Agent) mcpResultText(ctx context.Context, run *Run, cfg Config, server, tool string, res json.RawMessage) string {
	var r struct {
		Content    []mcpBlock      `json:"content"`
		Structured json.RawMessage `json:"structuredContent"`
	}
	if json.Unmarshal(res, &r) != nil || len(r.Content) == 0 {
		return string(res) // nothing in content to read: as it came
	}
	var texts []string
	var other []mcpBlock
	for _, b := range r.Content {
		if b.Type == "text" {
			texts = append(texts, b.Text)
		} else {
			other = append(other, b)
		}
	}
	body := strings.TrimSpace(strings.Join(texts, "\n"))
	if len(other) == 0 {
		return body
	}
	if len(texts) == 0 && len(r.Structured) > 0 && string(r.Structured) != "null" {
		body = "structuredContent: " + clip(string(r.Structured), 4<<10)
	}
	var foot string
	cls := classOf(cfg)
	if run != nil && cls.has(tsFiles) && cfg.feature("files") {
		k := &mcpKeeper{ag: ag, run: run, cfg: cfg, server: server, tool: tool, used: map[string]string{},
			repl: cls.has(tsRepl) && cfg.feature("repl")}
		foot = k.keep(ctx, other)
	} else {
		foot = mcpOtherBlocks(other, "this conversation can't keep files (its session files are off), so it was not saved")
	}
	if body == "" {
		return foot
	}
	return capTo(body, maxToolResult-len(foot)-256) + "\n\n" + foot
}

// mcpFilesHint is file_list's sentence on MCP tools' files, where the
// class can call MCP tools.
func mcpFilesHint(cfg Config) string {
	if !classOf(cfg).has(tsInternal) {
		return ""
	}
	return " A file an MCP tool returns (a PDF, an image) is saved here under mcp/<server>/ — the tool's result names its path."
}

// mcpOtherBlocks says what a result returned besides text, where nothing
// can be kept: one line each.
func mcpOtherBlocks(blocks []mcpBlock, cantKeep string) string {
	lines := []string{fmt.Sprintf("[xbin: the tool also returned %s that %s not text:]", nOf(len(blocks), "block"), isAre(len(blocks)))}
	inline := 0
	for _, b := range blocks {
		lines = append(lines, mcpNoKeepLine(b, cantKeep, &inline))
	}
	return strings.Join(lines, "\n")
}

// mcpNoKeepLine is one block, unsaved.
func mcpNoKeepLine(b mcpBlock, cantKeep string, inline *int) string {
	switch b.Type {
	case "resource_link":
		return mcpLinkLine(b)
	case "image", "audio", "resource":
	default:
		return fmt.Sprintf("- a %q block this harness doesn't read — not kept", mcpPlain(b.Type, 40))
	}
	if b.Resource != nil && b.Resource.Text != nil && b.Resource.Blob == nil {
		t := *b.Resource.Text
		if line, ok := mcpInlineLine(b, t, inline); ok {
			return line
		}
		line := fmt.Sprintf("- %s was returned; %s", mcpDesc(b, len(t)), cantKeep)
		if n := min(mcpInlineText, mcpInlineTotal-*inline); n >= 256 { // what the budget left of it
			*inline += n
			line += fmt.Sprintf(". Its first %s:\n%s", mcpSize(n), strings.ToValidUTF8(clip(t, n), ""))
		}
		return line
	}
	b64, ok := mcpB64(b)
	if !ok {
		return fmt.Sprintf("- %s with no bytes in it", mcpDesc(b, -1))
	}
	return fmt.Sprintf("- %s was returned; %s", mcpDesc(b, mcpB64Len(b64)), cantKeep)
}

// mcpInlineLine shows a small embedded text resource in place.
func mcpInlineLine(b mcpBlock, t string, inline *int) (string, bool) {
	if len(t) > mcpInlineText || *inline+len(t) > mcpInlineTotal || !utf8.ValidString(t) {
		return "", false
	}
	*inline += len(t)
	return fmt.Sprintf("- %s, shown here:\n%s", mcpDesc(b, len(t)), t), true
}

func mcpLinkLine(b mcpBlock) string {
	var bits []string
	if n := mcpPlain(orStr(b.Title, b.Name), 120); n != "" {
		bits = append(bits, strconv.Quote(n))
	}
	if b.MimeType != "" {
		bits = append(bits, mcpPlain(b.MimeType, 60))
	}
	if b.Size > 0 {
		bits = append(bits, mcpSize(int(b.Size)))
	}
	line := "- a link to a resource, not fetched: " + orStr(mcpPlain(b.URI, 300), "(no uri)")
	if len(bits) > 0 {
		line += " (" + strings.Join(bits, ", ") + ")"
	}
	return line + " — this harness doesn't read MCP resources; to get its content, ask the server's tools for the file itself"
}

// mcpB64 is a byte-carrying block's base64.
func mcpB64(b mcpBlock) (string, bool) {
	switch {
	case b.Type == "image" || b.Type == "audio":
		return b.Data, b.Data != ""
	case b.Resource != nil && b.Resource.Blob != nil:
		return *b.Resource.Blob, true
	}
	return "", false
}

// mcpB64Len is how many bytes base64 holds (whitespace aside), before
// decoding it.
func mcpB64Len(s string) int {
	n := len(s) - strings.Count(s, "\n") - strings.Count(s, "\r")
	pad := 0
	for i := len(s) - 1; i >= 0 && pad < 2; i-- {
		if s[i] == '=' {
			pad++
		} else if s[i] != '\n' && s[i] != '\r' {
			break
		}
	}
	return max(0, (n-pad)*3/4)
}

func mcpDecode(s string) ([]byte, error) {
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.RawStdEncoding.DecodeString(strings.TrimRight(s, "=\r\n"))
}

// mcpDesc names a block for a line: "an application/pdf file
// "invoice.pdf" of 321.1 KiB (328,843 bytes)". n < 0: no size.
func mcpDesc(b mcpBlock, n int) string {
	kind, typ, name := "file", b.MimeType, ""
	switch {
	case b.Type == "image":
		kind = "image"
	case b.Type == "audio":
		kind = "audio clip"
	case b.Resource != nil:
		typ = b.Resource.MimeType
		name = orStr(b.Resource.Title, orStr(b.Resource.Name, b.Resource.URI))
		if b.Resource.Blob == nil {
			kind = "text resource"
		}
	}
	s := kind
	if typ = mcpPlain(typ, 60); typ != "" {
		s = typ + " " + kind
	}
	s = aOrAn(s) + " " + s
	if name = mcpPlain(name, 160); name != "" {
		s += " " + strconv.Quote(name)
	}
	if n >= 0 {
		s += " of " + mcpSize(n)
		if n >= 1024 {
			s += " (" + groupDigits(n) + " bytes)"
		}
	}
	return s
}

// --- keeping -------------------------------------------------------------------------

// mcpKeeper saves one result's blocks in the run's session files.
type mcpKeeper struct {
	ag           *Agent
	run          *Run
	cfg          Config
	server, tool string
	repl         bool
	files, total int
	inline       int
	used         map[string]string // path → sha256, this result's
	over         []string          // the types of blocks past the file count
	shown        []string
}

func (k *mcpKeeper) keep(ctx context.Context, blocks []mcpBlock) string {
	lines := []string{fmt.Sprintf("[xbin: the tool also returned %s that %s not text — what carries bytes is saved in your session files; the bytes are not shown here:]",
		nOf(len(blocks), "block"), isAre(len(blocks)))}
	for _, b := range blocks {
		if line := k.block(ctx, b); line != "" {
			lines = append(lines, line)
		}
	}
	if len(k.over) > 0 {
		lines = append(lines, fmt.Sprintf("- not saved: %d more %s (%s) — over the limit of %d files for one result; ask the tool for fewer at a time",
			len(k.over), strings.TrimPrefix(nOf(len(k.over), "block"), strconv.Itoa(len(k.over))+" "), mcpCounts(k.over), mcpResultFiles))
	}
	if len(k.shown) > 0 {
		lines = append(lines, mcpShownLead+strings.Join(k.shown, ", "))
	}
	return strings.Join(lines, "\n")
}

func (k *mcpKeeper) block(ctx context.Context, b mcpBlock) string {
	switch b.Type {
	case "resource_link":
		return mcpLinkLine(b)
	case "image", "audio", "resource":
	default:
		return mcpNoKeepLine(b, "", nil)
	}
	var data []byte
	declared := b.MimeType
	if b.Resource != nil {
		declared = b.Resource.MimeType
	}
	if b.Resource != nil && b.Resource.Text != nil && b.Resource.Blob == nil {
		t := *b.Resource.Text
		if line, ok := mcpInlineLine(b, t, &k.inline); ok {
			return line
		}
		if k.full(b) {
			return ""
		}
		if why := k.room(len(t)); why != "" {
			return fmt.Sprintf("- not saved: %s — %s", mcpDesc(b, len(t)), why)
		}
		data = []byte(t)
		if declared == "" {
			declared = "text/plain"
		}
	} else {
		b64, ok := mcpB64(b)
		if !ok {
			return fmt.Sprintf("- %s with no bytes in it", mcpDesc(b, -1))
		}
		if k.full(b) {
			return ""
		}
		if why := k.room(mcpB64Len(b64)); why != "" {
			return fmt.Sprintf("- not saved: %s — %s", mcpDesc(b, mcpB64Len(b64)), why)
		}
		var err error
		if data, err = mcpDecode(b64); err != nil {
			return fmt.Sprintf("- not saved: %s — its base64 doesn't decode", mcpDesc(b, -1))
		}
		if why := k.room(len(data)); why != "" { // whitespace counted wrong, say
			return fmt.Sprintf("- not saved: %s — %s", mcpDesc(b, len(data)), why)
		}
	}
	head := data
	if len(head) > 512 {
		head = head[:512]
	}
	sniffed := normalizeMime("", head)
	stored := mcpStoreMime(declared, sniffed)
	sum := shaHex(data)
	p := k.pathFor(b, stored, sum)
	if p == "" {
		return fmt.Sprintf("- the same bytes again as %s", k.sameAs(sum))
	}
	f, prev, unchanged, err := k.ag.putFileData(ctx, k.run.ID, p, data, stored, toolSource(ctx, "mcp:"+k.server+":"+k.tool))
	if err != nil {
		return fmt.Sprintf("- not saved: %s — storing it failed: %s", mcpDesc(b, len(data)), clip(err.Error(), 200))
	}
	k.files++
	k.total += len(data)
	k.used[f.Path] = sum
	line := fmt.Sprintf("- saved %s — %s, %s", f.Path, orStr(f.Mime, stored), mcpSize(len(data)))
	if len(data) >= 1024 {
		line += " (" + groupDigits(len(data)) + " bytes)"
	}
	line += ", sha256 " + sum
	if d := mcpPlain(mcpBareMime(declared), 60); d != "" && d != orStr(f.Mime, stored) {
		line += fmt.Sprintf(" (the server said %s; its bytes are %s)", d, sniffed)
	}
	switch {
	case unchanged:
		line += fmt.Sprintf("; unchanged — the same bytes as its v%d, nothing new written", f.Version)
	case prev != nil:
		line += fmt.Sprintf("; now v%d — v%d is kept (session:%s@%d)", f.Version, prev.Version, f.Path, prev.Version)
	}
	if k.repl {
		line += fmt.Sprintf("; read it with files.readBytes(%q) in js_eval", f.Path)
	} else {
		line += "; it is in your session files (file_info describes it)"
	}
	if k.cfg.feature("vision") && f.Binary && visionMimes[f.Mime] && len(data) <= maxInlineImageBytes && len(k.shown) < maxInlineImages {
		k.shown = append(k.shown, f.Path)
	}
	return line
}

// full counts a block past the result's file count (said once, at the end).
func (k *mcpKeeper) full(b mcpBlock) bool {
	if k.files < mcpResultFiles {
		return false
	}
	t := mcpPlain(b.MimeType, 60)
	if b.Resource != nil {
		t = mcpPlain(b.Resource.MimeType, 60)
	}
	k.over = append(k.over, orStr(t, b.Type))
	return true
}

// room says why n more bytes can't be kept, or "".
func (k *mcpKeeper) room(n int) string {
	switch {
	case n > mcpBlockMax:
		return fmt.Sprintf("over the %s limit for one file", mcpSize(mcpBlockMax))
	case k.total+n > mcpResultMax:
		return fmt.Sprintf("over the %s limit for one result's files (%s saved before it)", mcpSize(mcpResultMax), mcpSize(k.total))
	}
	return ""
}

func (k *mcpKeeper) sameAs(sum string) string {
	for p, s := range k.used {
		if s == sum {
			return p
		}
	}
	return "an earlier block"
}

// pathFor is where a block is saved: mcp/<server>/<name>, the name the
// resource's title or name, else its uri's last segments, else the tool's
// name and the bytes' hash; with the stored type's extension. "" when this
// result already saved the same bytes at that name. A different block of
// this result at the same name gets -2, -3, … — two blocks of one answer are
// two files, never one's versions.
func (k *mcpKeeper) pathFor(b mcpBlock, stored, sum string) string {
	name := ""
	if r := b.Resource; r != nil {
		name = mcpSafeName(orStr(r.Title, r.Name))
		if name == "" {
			name = mcpSafeName(mcpURIName(r.URI))
		}
	}
	fallback := orStr(mcpSafeName(k.tool), "file") + "-" + sum[:10]
	if name == "" {
		name = fallback
	}
	ext, known := mcpExts[stored]
	if !known {
		ext = ".bin"
	}
	if have := strings.ToLower(path.Ext(name)); have != ext && mcpExtAlias[have] != stored {
		name += ext
	}
	if len(name) > 72 { // keep the extension
		e := path.Ext(name)
		name = strings.TrimRight(name[:72-len(e)], "-.") + e
	}
	dir := "mcp/" + orStr(mcpSafeName(clipASCII(k.server, 40)), "server") + "/"
	stem, e := strings.TrimSuffix(name, path.Ext(name)), path.Ext(name)
	for i := 1; ; i++ {
		p := dir + name
		if i > 1 {
			p = fmt.Sprintf("%s%s-%d%s", dir, stem, i, e)
		}
		s, taken := k.used[p]
		switch {
		case !taken:
			if q, err := normReplPath(p); err == nil {
				return q
			}
			return dir + fallback + ext
		case s == sum:
			return ""
		}
	}
}

// mcpURIName is a name from a uri: its last segment when that has an
// extension, else its last three ("documents/3317/download" →
// documents-3317-download), percent-escapes undone once, scheme, query and
// fragment dropped.
func mcpURIName(u string) string {
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	if i := strings.Index(u, ":"); i >= 0 && i < 32 {
		u = u[i+1:]
	}
	if d, err := url.PathUnescape(u); err == nil {
		u = d
	}
	var segs []string
	for _, s := range strings.FieldsFunc(u, func(r rune) bool { return r == '/' || r == '\\' }) {
		if s != "." && s != ".." {
			segs = append(segs, s)
		}
	}
	if len(segs) == 0 {
		return ""
	}
	last := segs[len(segs)-1]
	if strings.Contains(strings.TrimLeft(last, "."), ".") {
		return last
	}
	return strings.Join(segs[max(0, len(segs)-3):], "-")
}

var (
	mcpUnsafe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)
	mcpDashes = regexp.MustCompile(`-{2,}`)
)

// mcpSafeName is s as one path segment the session store takes: ASCII
// letters, digits, . _ - (anything else, look-alikes too, becomes -), no
// leading dots or dashes, at most 72 bytes.
func mcpSafeName(s string) string {
	s = mcpUnsafe.ReplaceAllString(s, "-")
	s = mcpDashes.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-.")
	return strings.Trim(clipASCII(s, 72), "-.")
}

func clipASCII(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// mcpExts is the allow-list of types a saved block is labelled with, and
// their extensions; anything else is labelled as its bytes sniff and named
// .bin.
var mcpExts = map[string]string{
	"application/pdf": ".pdf", "image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif", "image/webp": ".webp",
	"image/svg+xml": ".svg", "audio/mpeg": ".mp3", "audio/wav": ".wav", "audio/wave": ".wav", "audio/x-wav": ".wav",
	"audio/ogg": ".ogg", "application/ogg": ".ogg", "audio/webm": ".weba", "audio/mp4": ".m4a", "audio/flac": ".flac",
	"text/plain": ".txt", "text/csv": ".csv", "text/markdown": ".md", "text/html": ".html", "application/json": ".json",
	"application/xml": ".xml", "text/xml": ".xml", "application/yaml": ".yaml", "application/zip": ".zip",
	"application/gzip": ".gz", "application/x-gzip": ".gz", "application/epub+zip": ".epub",
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document":   ".docx",
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         ".xlsx",
	"application/vnd.openxmlformats-officedocument.presentationml.presentation": ".pptx",
	"application/vnd.oasis.opendocument.text":                                   ".odt",
}

var mcpExtAlias = map[string]string{".jpeg": "image/jpeg", ".htm": "text/html", ".yml": "application/yaml", ".text": "text/plain"}

// mcpActive are the types a viewer may run or render as a page: a declared
// one is believed only when the bytes sniff as it.
var mcpActive = map[string]bool{"text/html": true, "image/svg+xml": true, "application/xml": true, "text/xml": true}

func mcpBareMime(m string) string {
	m = strings.ToLower(strings.TrimSpace(m))
	if i := strings.IndexByte(m, ';'); i >= 0 {
		m = strings.TrimSpace(m[:i])
	}
	return m
}

// mcpStoreMime is the type a block is saved as: what its bytes sniff as,
// unless the server's declared type is on the allow-list and agrees with
// them — the same type, or a finer name for what sniffing can only call
// generic (a zip that is a .docx, text that is CSV, audio it can't place).
// A declared type is never more than a label and an extension.
func mcpStoreMime(declared, sniffed string) string {
	d := mcpBareMime(declared)
	if _, ok := mcpExts[d]; !ok || d == sniffed {
		return sniffed
	}
	switch {
	case mcpActive[d]:
		return sniffed
	case sniffed == "application/zip" && (strings.Contains(d, "openxmlformats") || strings.Contains(d, "opendocument") || d == "application/epub+zip"):
		return d
	case sniffed == "text/plain" && isTextMime(d):
		return d
	case sniffed == "application/octet-stream" && (strings.HasPrefix(d, "audio/") || d == "application/gzip"):
		return d
	case (strings.HasPrefix(sniffed, "audio/") || sniffed == "application/ogg") && strings.HasPrefix(d, "audio/"):
		return d
	}
	return sniffed
}

// --- words -------------------------------------------------------------------------------

// mcpPlain is a server's string as one plain line: no control or invisible
// characters, clipped.
func mcpPlain(s string, n int) string {
	s = invisibles.ReplaceAllString(s, "")
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return ' '
		}
		return r
	}, s)
	return clip(strings.TrimSpace(s), n)
}

func mcpSize(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

func groupDigits(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func aOrAn(s string) string {
	if s != "" && strings.ContainsRune("aeiouAEIOU", rune(s[0])) {
		return "an"
	}
	return "a"
}

func nOf(n int, w string) string {
	if n == 1 {
		return "1 " + w
	}
	return fmt.Sprintf("%d %ss", n, w)
}

func isAre(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}

func mcpCounts(types []string) string {
	c := map[string]int{}
	for _, t := range types {
		c[orStr(t, "block")]++
	}
	var out []string
	for t, n := range c {
		out = append(out, fmt.Sprintf("%d %s", n, t))
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}
