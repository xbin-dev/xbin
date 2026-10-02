//go:build linux && xbinmeasure

package measure

// tiles_test.go — the tiles the measurements save into: a team handbook
// (a static tile) and an orders API (a Go backend), written as a person or
// an agent would write them.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/test/xbindtest"
)

const (
	handbookTile = "apps/handbook"
	ordersTile   = "apps/orders"
)

// handbookPages are the handbook's pages: file → title, lead.
var handbookPages = [][3]string{
	{"onboarding.html", "Your first week", "Accounts, laptop setup, who to meet and the three things to ship before Friday."},
	{"expenses.html", "Expenses", "What you can spend without asking, how to file a receipt, and when you get paid back."},
	{"on-call.html", "On-call", "The rota, the escalation ladder, what a page means, and how to hand over at the end of a shift."},
	{"security.html", "Security basics", "Passwords, hardware keys, laptops on the road, and how to report something odd."},
	{"travel.html", "Travel", "Booking, per diems, visas and the airport-lounge rule."},
	{"benefits.html", "Benefits", "Health cover, the learning budget, parental leave and the gym stipend."},
	{"holidays.html", "Time off", "Holidays, sick days, the company-wide quiet weeks and how to ask for leave."},
	{"equipment.html", "Equipment", "What you get on day one, how to order more, and what to do with old kit."},
	{"remote.html", "Working remotely", "Core hours, the coworking allowance and how we keep meetings short."},
	{"code-review.html", "Code review", "Small changes, fast turnarounds, and what a reviewer is asked to check."},
	{"releases.html", "Releases", "The weekly train, hotfixes, feature flags and who presses the button."},
	{"customers.html", "Talking to customers", "Support hours, the tone we use and how to escalate a bug report."},
}

const handbookCSS = `:root { --ink: #1d2433; --muted: #5b6475; --accent: #2f6fde; --paper: #fbfaf7; --line: #e6e2d9; }
* { box-sizing: border-box; }
body { margin: 0; font: 16px/1.55 system-ui, -apple-system, "Segoe UI", sans-serif; color: var(--ink); background: var(--paper); }
header { display: flex; align-items: center; gap: 12px; padding: 18px 28px; border-bottom: 1px solid var(--line); }
header h1 { font-size: 20px; margin: 0; }
header .updated { margin-left: auto; color: var(--muted); font-size: 13px; }
main { display: grid; grid-template-columns: 240px 1fr; gap: 28px; padding: 24px 28px; max-width: 1100px; }
nav a { display: block; padding: 6px 10px; border-radius: 6px; color: var(--ink); text-decoration: none; }
nav a:hover { background: #efece5; }
.hero h2 { font-size: 30px; margin: 0 0 6px; letter-spacing: -0.01em; }
.hero p { color: var(--muted); margin: 0 0 18px; }
.cards { display: grid; grid-template-columns: repeat(auto-fill, minmax(220px, 1fr)); gap: 14px; }
.card { background: white; border: 1px solid var(--line); border-radius: 10px; padding: 14px 16px; }
.card h3 { margin: 0 0 4px; font-size: 16px; }
.card p { margin: 0; color: var(--muted); font-size: 14px; }
`

const handbookJS = `// filter the handbook's cards as you type
const q = document.querySelector('#q');
q?.addEventListener('input', () => {
  const v = q.value.trim().toLowerCase();
  for (const c of document.querySelectorAll('.card')) c.hidden = v && !c.textContent.toLowerCase().includes(v);
});
`

const handbookLogo = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32"><rect width="32" height="32" rx="7" fill="#2f6fde"/><path d="M9 9h9a5 5 0 0 1 0 10h-5v4H9z" fill="#fff"/></svg>
`

// handbookIndex is the handbook's front page; headline and stamp are what a
// save changes, and probe (when not "") is a script the browser measurement
// adds (the page itself reports when it was first painted).
func handbookIndex(headline, stamp, probe string) string {
	var b strings.Builder
	b.WriteString(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Team handbook</title>
<link rel="stylesheet" href="css/handbook.css">
` + probe + `</head>
<body>
<header><img src="img/logo.svg" width="28" height="28" alt=""><h1>Team handbook</h1><span class="updated">` + stamp + `</span></header>
<main>
<nav>
`)
	for _, p := range handbookPages {
		fmt.Fprintf(&b, "  <a href=\"pages/%s\">%s</a>\n", p[0], p[1])
	}
	b.WriteString(`</nav>
<section>
<div class="hero"><h2 id="headline">` + headline + `</h2>
<p>Everything about how we work, in one place. Search, or pick a topic.</p>
<input id="q" placeholder="Search the handbook" style="width:100%;padding:8px 10px;border:1px solid #e6e2d9;border-radius:8px;margin-bottom:16px"></div>
<div class="cards">
`)
	for _, p := range handbookPages {
		fmt.Fprintf(&b, "  <a class=\"card\" href=\"pages/%s\"><h3>%s</h3><p>%s</p></a>\n", p[0], p[1], p[2])
	}
	b.WriteString(`</div>
</section>
</main>
<script type="module" src="js/handbook.js"></script>
</body>
</html>
`)
	return b.String()
}

// handbookFiles is the whole handbook tile.
func handbookFiles(headline, stamp, probe string) map[string]string {
	files := map[string]string{
		"xbin.json":         `{"runtime": "static"}` + "\n",
		"index.html":        handbookIndex(headline, stamp, probe),
		"css/handbook.css":  handbookCSS,
		"js/handbook.js":    handbookJS,
		"img/logo.svg":      handbookLogo,
		"README.md":         "# Team handbook\n\nHow we work. Edit a page and save: it is live for everyone at once.\n",
		"pages/_layout.css": "article { max-width: 720px; margin: 32px auto; padding: 0 20px; }\n",
	}
	for _, p := range handbookPages {
		var body strings.Builder
		for i := 0; i < 6; i++ {
			fmt.Fprintf(&body, "<h2>%s, part %d</h2>\n<p>%s %s</p>\n", p[1], i+1, p[2], strings.Repeat("Ask in #people-ops if anything here is unclear; the page owner answers within a day. ", 3))
		}
		files["pages/"+p[0]] = "<!doctype html><html lang=\"en\"><head><meta charset=\"utf-8\"><title>" + p[1] +
			"</title><link rel=\"stylesheet\" href=\"../css/handbook.css\"><link rel=\"stylesheet\" href=\"_layout.css\"></head><body><article><h1>" +
			p[1] + "</h1>\n" + body.String() + "</article></body></html>\n"
	}
	return files
}

// writeHandbook writes the handbook (manifest last) and waits for xbind to
// register it.
func writeHandbook(t testing.TB, d *xbindtest.Daemon, probe string) {
	t.Helper()
	files := handbookFiles("Welcome to Northwind", "Updated just now", probe)
	manifest := files["xbin.json"]
	delete(files, "xbin.json")
	if err := d.WriteFiles(handbookTile, files); err != nil {
		t.Fatal(err)
	}
	d.WriteTile(t, handbookTile, map[string]string{"xbin.json": manifest})
}

// ordersSource is the orders API's backend: a release marker (what a save
// changes) and an in-memory order book.
func ordersSource(release string) string {
	return `package main

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// release names this build: the orders API answers with it.
const release = "` + release + `"

type order struct {
	ID      int       ` + "`json:\"id\"`" + `
	Item    string    ` + "`json:\"item\"`" + `
	Qty     int       ` + "`json:\"qty\"`" + `
	Created time.Time ` + "`json:\"created\"`" + `
}

func main() {
	var mu sync.Mutex
	now := time.Now()
	book := []order{
		{1, "Espresso beans, 1 kg", 4, now},
		{2, "Oat milk, 12 x 1 l", 2, now},
		{3, "Paper cups, 500", 1, now},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /orders", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n := len(book)
		recent := append([]order(nil), book[max(0, n-3):]...)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"release": release, "count": n, "recent": recent})
	})
	mux.HandleFunc("POST /orders", func(w http.ResponseWriter, r *http.Request) {
		var o order
		if err := json.NewDecoder(r.Body).Decode(&o); err != nil || o.Item == "" {
			http.Error(w, "an order needs an item", http.StatusBadRequest)
			return
		}
		mu.Lock()
		o.ID, o.Created = len(book)+1, time.Now()
		book = append(book, o)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"release": release, "order": o})
	})
	xbin.Serve(mux)
}
`
}

// writeOrders writes the orders tile (manifest last) and waits for xbind to
// register it; the first request after waits for its first build.
func writeOrders(t testing.TB, d *xbindtest.Daemon, release string) {
	t.Helper()
	files := map[string]string{
		"go.mod":          "module orders\n\ngo 1.24\n\nrequire github.com/xbin-dev/xbin/sdk v0.0.0\n",
		"backend/main.go": ordersSource(release),
		"index.html":      "<!doctype html><title>Orders</title><h1>Orders</h1>\n",
		"API.md":          "# Orders\n\nGET /orders — the latest orders. POST /orders {item, qty} — place one.\n",
	}
	if err := d.WriteFiles(ordersTile, files); err != nil {
		t.Fatal(err)
	}
	d.WriteTile(t, ordersTile, map[string]string{"xbin.json": `{"runtime": "go"}` + "\n"})
}
