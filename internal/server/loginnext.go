package server

// loginnext.go — where a sign-in lands: GET /login?next=<path>. A signed-out
// browser sent to a deep-link page (the partitions page, /xbin/partitions:
// the pushes, refusals and docs send people there) signs in — by password or
// single sign-on — and lands back on it rather than on the workspace. next
// is safeNext's (webticket.go): a same-origin path, never the sign-in routes;
// a missing or refused one lands on /, as before.

import (
	"html"
	"net/http"
	"net/url"
)

// loginNextPaths are the pages whose signed-out redirect carries their path
// (authed: loginURLFor). Everything else goes to plain /login, as before.
var loginNextPaths = map[string]bool{PersonPagePath: true}

// loginURLFor is where authed sends a signed-out browser navigation to r.
func loginURLFor(r *http.Request) string {
	if !loginNextPaths[r.URL.Path] {
		return "/login"
	}
	if n, ok := safeNext(r.URL.RequestURI()); ok && n != "/" {
		return "/login?next=" + url.QueryEscape(n)
	}
	return "/login"
}

// loginNext is the landing path of a sign-in carrying raw ("/" for none or
// one safeNext refuses).
func loginNext(raw string) string {
	if n, ok := safeNext(raw); ok {
		return n
	}
	return "/"
}

// loginNextField is the login form's hidden next input for GET /login's
// ?next= ("" when it lands on /).
func loginNextField(r *http.Request) string {
	n := loginNext(r.URL.Query().Get("next"))
	if n == "/" {
		return ""
	}
	return `<input type="hidden" name="next" value="` + html.EscapeString(n) + `">`
}

// ssoStartURL is the login page's single sign-on link, carrying ?next=.
func ssoStartURL(r *http.Request) string {
	n := loginNext(r.URL.Query().Get("next"))
	if n == "/" {
		return "/login/sso"
	}
	return "/login/sso?next=" + url.QueryEscape(n)
}
