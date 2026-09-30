package main

// bx partition consent|ledger — cross-tile partition edges
// (docs/partitions.md §Calls between partitioned tiles, docs/bx.md): a
// person's consent that one partitioned tile may use their data in another
// (asked only while the workspace policy partitionConsent is on — bx
// policies), and their partitions' egress ledger. Both are a person's own
// acts and reads (PersonOnly): from bx with a login or the root token's
// person, never from a tile's terminal. Against an xbind without them both
// exit 6. bx doctor lists the edges between partitioned tiles for review
// (admins), and bx grant says whose data an approval opens.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
)

const partitionConsentUsage = `  bx partition consent <from> <to> [--revoke]
                                        let partitioned tile <from> use your data in
                                        <to> (only while the workspace asks people
                                        first: bx policies); --revoke takes it back
  bx partition consent ls [--json]      your consents, and the edges you were asked about
  bx partition ledger [<tile>] [--days n] [--json]
                                        your partitions' egress ledger: counts, never contents
`

// partitionAPI calls a partitions route; an xbind without it (Go's mux 404
// or 405, no error body) answers errNoPartitions.
func partitionAPI(method, path string, body any) (map[string]any, error) {
	resp, err := api(method, path, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	switch {
	case (resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed) && out["error"] == nil:
		return nil, errNoPartitions
	case resp.StatusCode >= 400:
		msg := fmt.Sprint(out["error"])
		if out["error"] == nil {
			msg = strings.TrimSpace(string(b))
		}
		return out, fmt.Errorf("%s (%s)", msg, resp.Status)
	}
	return out, nil
}

// asJSONOut re-encodes a partitions answer on stdout.
func asJSONOut(out map[string]any) error { return json.NewEncoder(os.Stdout).Encode(out) }

func partitionConsent(args []string) error {
	revoke, asJSON := false, false
	var rest []string
	for _, a := range args {
		switch {
		case a == "--revoke":
			revoke = true
		case a == "--json":
			asJSON = true
		case isFlag(a):
			return unknownFlag("partition consent", a, false)
		default:
			rest = append(rest, strings.Trim(a, "/"))
		}
	}
	if len(rest) == 1 && rest[0] == "ls" && !revoke {
		out, err := partitionAPI("GET", "/api/xbin/partitions/consents", nil)
		if err != nil {
			return err
		}
		if asJSON {
			return asJSONOut(out)
		}
		printConsents(out)
		return nil
	}
	if len(rest) != 2 || rest[0] == rest[1] {
		return errors.New("usage:\n" + partitionConsentUsage)
	}
	method := "POST"
	if revoke {
		method = "DELETE"
	}
	out, err := partitionAPI(method, "/api/xbin/partitions/consents", map[string]string{"from": rest[0], "to": rest[1]})
	if err != nil {
		return err
	}
	if asJSON {
		return asJSONOut(out)
	}
	if revoke {
		fmt.Printf("%s can no longer use your data in %s; its instance of you was stopped.\n", rest[0], rest[1])
	} else {
		fmt.Printf("%s may now use your data in %s (bx partition consent %s %s --revoke takes it back).\n", rest[0], rest[1], rest[0], rest[1])
	}
	return nil
}

// printConsents prints GET /partitions/consents.
func printConsents(out map[string]any) {
	pol, _ := out["policy"].(map[string]any)
	if on, _ := pol["partitionConsent"].(bool); on {
		fmt.Println("This workspace asks you before a partitioned tile uses your data in another.")
	} else {
		fmt.Println("This workspace doesn't ask people first (partitionConsent is off): your consents are kept, unused.")
	}
	rows, _ := out["consents"].([]any)
	if len(rows) == 0 {
		fmt.Println("You haven't let any tile use your data in another.")
	}
	for _, r := range rows {
		m, _ := r.(map[string]any)
		fmt.Printf("  %-30s → %s   (since %v)\n", m["from"], m["to"], m["at"])
	}
	if asked, _ := out["asked"].([]any); len(asked) > 0 {
		fmt.Println("Asked for, not allowed (bx partition consent <from> <to>):")
		for _, r := range asked {
			m, _ := r.(map[string]any)
			fmt.Printf("  %-30s → %s\n", m["from"], m["to"])
		}
	}
}

func partitionLedger(args []string) error {
	q := url.Values{}
	asJSON := false
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--json":
			asJSON = true
		case a == "--days":
			v, err := nextArg(args, &i)
			if err != nil {
				return err
			}
			if n, err := strconv.Atoi(v); err != nil || n < 1 || n > 90 {
				return fmt.Errorf("--days: 1 to 90, not %q", v)
			}
			q.Set("days", v)
		case isFlag(a):
			return unknownFlag("partition ledger", a, false)
		case q.Get("tile") != "":
			return errors.New("usage:\n" + partitionConsentUsage)
		default:
			q.Set("tile", strings.Trim(a, "/"))
		}
	}
	path := "/api/xbin/partitions/ledger"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	out, err := partitionAPI("GET", path, nil)
	if err != nil {
		return err
	}
	if asJSON {
		return asJSONOut(out)
	}
	rows, _ := out["rows"].([]any)
	fmt.Printf("Your partitions' egress, the last %v days (counts, never contents):\n", out["days"])
	if len(rows) == 0 {
		fmt.Println("  nothing counted")
	}
	for _, r := range rows {
		m, _ := r.(map[string]any)
		fmt.Printf("  %v  %-24v %-8v → %-24v %v\n", m["day"], m["tile"], m["kind"], m["target"], m["count"])
	}
	for _, sec := range []struct{ key, head string }{{"totals", "The tile's totals, every person:"}, {"people", "Per person (admin):"}} {
		list, ok := out[sec.key].([]any)
		if !ok {
			continue
		}
		fmt.Println(sec.head)
		for _, r := range list {
			m, _ := r.(map[string]any)
			who := ""
			if u, ok := m["user"]; ok {
				who = fmt.Sprintf("%v in %v: ", u, m["tile"])
			}
			fmt.Printf("  %s%-8v → %-24v %v\n", who, m["kind"], m["target"], m["count"])
		}
	}
	return nil
}

// doctorPartitionEdges lists the edges between partitioned tiles for review
// (06 §7, AR-19): with partitionConsent off each is open to the calling
// tile's code for every person who can read the callee. Admin credentials
// only; silently skipped otherwise, and against an older xbind.
func doctorPartitionEdges(ok func(string, ...any)) {
	var out struct {
		Days   int `json:"days"`
		Policy struct {
			PartitionConsent bool `json:"partitionConsent"`
		} `json:"policy"`
		Edges []struct {
			From, To          string
			Granted           bool
			People, Consented int
			Calls             int64
		} `json:"edges"`
	}
	if apiJSON("GET", "/api/xbin/partitions/edges", nil, &out) != nil {
		return
	}
	granted := 0
	for _, e := range out.Edges {
		if e.Granted {
			granted++
		}
	}
	if granted == 0 {
		ok("no partitioned tile holds a grant on another's people's data")
		return
	}
	for _, e := range out.Edges {
		if !e.Granted {
			continue
		}
		who := "every person who can read " + e.To + " (partitionConsent is off — bx policies)"
		if out.Policy.PartitionConsent {
			who = fmt.Sprintf("each person who allows it (%d did)", e.Consented)
		}
		fmt.Printf("  · %s → %s: %s's code reaches the %s data of %s; used by %d person(s) in %d days\n",
			e.From, e.To, e.From, e.To, who, e.People, out.Days)
	}
}

// grantWarning is the approval warning GET /grants gives a pending request
// of from on target (a partitioned tile on another's people's data), or "".
func grantWarning(from, target, role string) string {
	var out struct {
		Pending []struct {
			From, Target, Role, Warning string
		} `json:"pending"`
	}
	if apiJSON("GET", "/api/xbin/grants", nil, &out) != nil {
		return ""
	}
	for _, p := range out.Pending {
		if p.From == from && p.Target == target && p.Role == role {
			return p.Warning
		}
	}
	return ""
}
