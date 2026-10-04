// page.go — the page's own API (index.html, native.js): what this instance
// is and what the person looking may do here. Setup and policy are
// setup.go's and policy.go's; a person's sign-in is /scm/signin*.
package main

import "net/http"

// handlePage answers what the page shows first: the mode, whether the
// caller manages the tile (setup lives at global), the App's public
// settings, the caller's sign-in (in their partition) and the events'
// health. Never a secret.
func (s *srv) handlePage(w http.ResponseWriter, r *http.Request, c who) {
	pub := s.public()
	out := map[string]any{
		"mode":       s.mode,
		"self":       s.self,
		"manager":    s.manager(c),
		"configured": pub.Configured,
		"app":        map[string]any{"slug": pub.Slug, "installUrl": pub.InstallURL, "host": pub.Host, "deviceFlow": pub.DeviceFlow},
		"policy":     pub.Policy,
		"rerun":      pub.Rerun,
		"events":     eventsHealth(s),
	}
	if s.mode != modeUser {
		if a, _ := s.app(); a != nil {
			out["configured"] = true
			out["app"] = map[string]any{"slug": a.Slug, "installUrl": s.installURL(a), "host": a.Host, "deviceFlow": pub.DeviceFlow}
		}
	}
	if s.mode == modeUser {
		if id := s.signedIn(); id != nil {
			rec := s.personRecord()
			out["person"] = map[string]any{"login": id.Login, "id": id.ID, "expiresAt": rec.RefreshExpiresAt, "registered": rec.Registered}
		} else {
			out["person"] = nil
		}
	}
	writeGET(w, r, out)
}
