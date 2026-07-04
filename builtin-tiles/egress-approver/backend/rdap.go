package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// rdapLookup fetches a compact whois-style summary for an IP from rdap.org (the
// RDAP bootstrap redirector — the modern, JSON, HTTP replacement for port-43
// whois). It runs from *this* tile's own egress (forward-gated traffic can't
// reach it, but our own can), so the owner can see who an address belongs to
// before deciding. Best-effort: a failure just yields {error}.
func rdapLookup(ip string) map[string]any {
	c := &http.Client{Timeout: 6 * time.Second}
	req, _ := http.NewRequest("GET", "https://rdap.org/ip/"+ip, nil)
	req.Header.Set("Accept", "application/rdap+json")
	resp, err := c.Do(req)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return map[string]any{"error": fmt.Sprintf("rdap %s", resp.Status)}
	}
	var d struct {
		Handle       string `json:"handle"`
		Name         string `json:"name"`
		Country      string `json:"country"`
		Type         string `json:"type"`
		StartAddress string `json:"startAddress"`
		EndAddress   string `json:"endAddress"`
		CIDR0        []struct {
			V4Prefix string `json:"v4prefix"`
			V6Prefix string `json:"v6prefix"`
			Length   int    `json:"length"`
		} `json:"cidr0_cidrs"`
		Entities []struct {
			Roles      []string `json:"roles"`
			VCardArray []any    `json:"vcardArray"`
		} `json:"entities"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return map[string]any{"error": "malformed rdap"}
	}

	cidr := ""
	if len(d.CIDR0) > 0 {
		p := d.CIDR0[0].V4Prefix
		if p == "" {
			p = d.CIDR0[0].V6Prefix
		}
		cidr = fmt.Sprintf("%s/%d", p, d.CIDR0[0].Length)
	} else if d.StartAddress != "" {
		cidr = d.StartAddress + " – " + d.EndAddress
	}

	out := map[string]any{
		"handle": d.Handle, "name": d.Name, "country": d.Country,
		"type": d.Type, "cidr": cidr,
	}
	if org := firstOrg(d.Entities); org != "" {
		out["org"] = org
	}
	return out
}

// firstOrg digs the display name ("fn") out of the first entity's jCard — the
// vcardArray is ["vcard", [ [name, params, type, value], … ]].
func firstOrg(entities []struct {
	Roles      []string `json:"roles"`
	VCardArray []any    `json:"vcardArray"`
}) string {
	for _, e := range entities {
		if len(e.VCardArray) != 2 {
			continue
		}
		props, ok := e.VCardArray[1].([]any)
		if !ok {
			continue
		}
		for _, p := range props {
			f, ok := p.([]any)
			if !ok || len(f) != 4 {
				continue
			}
			if name, _ := f[0].(string); name == "fn" {
				if v, ok := f[3].(string); ok && v != "" {
					return v
				}
			}
		}
	}
	return ""
}
