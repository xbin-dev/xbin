package main

// bx partition — partitioned tiles (docs/partitions.md, docs/bx.md). switch
// and keep decide a tile's partition mode switch request (POST
// /api/xbin/partitions/mode): a tile manager's act, from bx with the root
// token or a login — a tile's terminal can't decide. The request's R and Q
// come from the tile's /components row, so a request that changed meanwhile
// is refused (409) rather than decided blind. switch first shows what it
// deletes and keeps (a dry run), then asks for the tile's path (on stderr,
// so --json's stdout stays JSON). Against an xbind without partitions both
// exit 6 — one whose row has no partition at all is asked through the route
// itself, so an xbind older than partitioned tiles is told apart from a tile
// that asks for none.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

const partitionUsage = `  bx partition switch <tile> [--dry-run] [--confirm <tile>] [--yes] [--json]
                                        delete all the tile's data and take the
                                        partition mode its code asks for
  bx partition keep <tile> [--json]     keep the current partition mode (deletes nothing)
`

const exitNoPartitions = 6 // this xbind has no partitions

// errNoPartitions: this xbind has no partitioned tiles (no POST
// /api/xbin/partitions/mode); cmdPartition exits 6 with it.
var errNoPartitions = errors.New("this xbind has no partitioned tiles (no POST /api/xbin/partitions/mode); upgrade xbind")

func init() { moreCmds["partition"] = cmdPartition }

// partSpec is a partition mode on the wire: {user, global}; nil is
// unpartitioned.
type partSpec struct {
	User   bool `json:"user"`
	Global bool `json:"global"`
}

func (s *partSpec) String() string {
	switch {
	case s == nil || !s.User && !s.Global:
		return "unpartitioned"
	case s.User && s.Global:
		return "user + global"
	case s.User:
		return "user"
	}
	return "global"
}

// wire is s as the mode route takes it: null for unpartitioned.
func (s *partSpec) wire() *partSpec {
	if s == nil || !s.User && !s.Global {
		return nil
	}
	return s
}

func cmdPartition(args []string) error {
	err := partitionCmd(args)
	if errors.Is(err, errNoPartitions) {
		fmt.Fprintln(os.Stderr, "bx:", err)
		os.Exit(exitNoPartitions)
	}
	return err
}

func partitionCmd(args []string) error {
	if len(args) == 0 {
		return errors.New("usage:\n" + partitionUsage + partitionConsentUsage + partitionOpsUsage)
	}
	switch args[0] {
	case "mail": // partitionmail.go
		return partitionMail(args[1:])
	case "switch", "keep":
		return partitionDecide(args[0], args[1:])
	case "consent": // partitionconsent.go
		return partitionConsent(args[1:])
	case "ledger":
		return partitionLedger(args[1:])
	}
	if f, ok := partitionOpCmds[args[0]]; ok { // partitionops.go
		return f(args[1:])
	}
	return fmt.Errorf("bx partition: unknown subcommand %q (ls, switch, keep, stop, reset, purge, limits, share-log, credential, consent, ledger)", args[0])
}

// partitionRow is the partition part of a /components row.
type partitionRow struct {
	Partition *struct {
		State   string `json:"state"`
		User    bool   `json:"user"`
		Global  bool   `json:"global"`
		Request *struct {
			partSpec
			Declined bool `json:"declined"`
		} `json:"request"`
	} `json:"partition"`
	PartitionError string `json:"partitionError"`
}

// partitionRequest reads tile's request R → Q from its /components row
// (GET /components/<tile> answers {component: row, apiDoc}).
func partitionRequest(tile string) (from, to *partSpec, declined bool, err error) {
	var one struct {
		Component partitionRow `json:"component"`
	}
	resp, err := api("GET", "/api/xbin/components/"+escapePath(tile), nil)
	if err != nil {
		return nil, nil, false, err
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, nil, false, fmt.Errorf("%s: %s", tile, strings.TrimSpace(string(b)))
	}
	if err := json.Unmarshal(b, &one); err != nil {
		return nil, nil, false, err
	}
	p := one.Component.Partition
	switch {
	case p == nil:
		// no partition on the row: a tile that asks for none — or an xbind
		// older than partitioned tiles, whose rows have none at all. The route
		// tells them apart: a mux 404 exits 6, the route itself says why.
		if _, err := partitionPost(map[string]any{"tile": tile, "act": "keep", "from": nil, "to": nil, "dryRun": true}); err != nil {
			return nil, nil, false, err
		}
		return nil, nil, false, fmt.Errorf("%s has no partition mode switch request (it doesn't ask for a partition mode)", tile)
	case p.State == "invalid":
		return nil, nil, false, fmt.Errorf("%s's partition request is invalid, so there is nothing to decide: %s", tile, one.Component.PartitionError)
	case p.Request == nil:
		return nil, nil, false, fmt.Errorf("%s has no partition mode switch request (it runs %s)", tile, (&partSpec{p.User, p.Global}).String())
	}
	return &partSpec{p.User, p.Global}, &partSpec{p.Request.User, p.Request.Global}, p.Request.Declined, nil
}

func partitionDecide(act string, args []string) error {
	var tile string
	dry, confirm, yes, asJSON := new(bool), new(string), new(bool), new(bool)
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--dry-run" && act == "switch":
			*dry = true
		case a == "--yes" && act == "switch":
			*yes = true
		case a == "--json":
			*asJSON = true
		case a == "--confirm" && act == "switch":
			v, err := nextArg(args, &i)
			if err != nil {
				return err
			}
			*confirm = v
		case isFlag(a):
			return unknownFlag("partition "+act, a, false)
		case tile != "":
			return errors.New("usage:\n" + partitionUsage)
		default:
			tile = strings.Trim(a, "/")
		}
	}
	if tile == "" {
		return errors.New("usage:\n" + partitionUsage)
	}
	from, to, declined, err := partitionRequest(tile)
	if err != nil {
		return err
	}
	body := map[string]any{"tile": tile, "act": act, "from": from.wire(), "to": to.wire()}
	if act == "keep" {
		if declined {
			return fmt.Errorf("%s already keeps its current mode (%s; its code asks for %s)", tile, from, to)
		}
		out, err := partitionPost(body)
		if err != nil {
			return err
		}
		if *asJSON {
			return json.NewEncoder(os.Stdout).Encode(out)
		}
		fmt.Printf("%s keeps its partition mode (%s) and runs again; nothing was deleted.\n", tile, from)
		fmt.Printf("Its code still asks for %s: bx partition switch %s takes it (--dry-run shows what that deletes).\n", to, tile)
		return nil
	}
	body["dryRun"] = true
	preview, err := partitionPost(body)
	if err != nil {
		return err
	}
	delete(body, "dryRun")
	if *dry {
		if *asJSON {
			return json.NewEncoder(os.Stdout).Encode(preview)
		}
		printSwitch(os.Stdout, tile, from, to, preview, true)
		return nil
	}
	if !*asJSON {
		printSwitch(os.Stdout, tile, from, to, preview, true)
	}
	if *confirm == "" {
		fmt.Fprintf(os.Stderr, "Type the tile's path (%s) to delete its data and switch: ", tile)
		if !confirmLine(tile) {
			return errors.New("not confirmed: nothing was deleted")
		}
		*confirm = tile
	}
	body["confirm"], body["yes"] = *confirm, *yes
	out, err := partitionPost(body)
	if err != nil {
		return err
	}
	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(out)
	}
	printSwitch(os.Stdout, tile, from, to, out, false)
	return nil
}

// partitionPost is POST /partitions/mode; an xbind without it answers
// errNoPartitions.
func partitionPost(body map[string]any) (map[string]any, error) {
	resp, err := api("POST", "/api/xbin/partitions/mode", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	switch {
	case (resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed) && out["error"] == nil:
		// Go's mux, not a refusal: the route is missing (compat rule 8)
		return nil, errNoPartitions
	case resp.StatusCode >= 400:
		msg := fmt.Sprint(out["error"])
		if out["error"] == nil {
			msg = strings.TrimSpace(string(b))
		}
		if m, ok := out["managers"].([]any); ok && len(m) > 0 {
			msg += " (bx partition switch … --yes)"
		}
		return out, fmt.Errorf("%s (%s)", msg, resp.Status)
	}
	return out, nil
}

// printSwitch prints a switch's answer, or its dry run's.
func printSwitch(w io.Writer, tile string, from, to *partSpec, out map[string]any, dry bool) {
	wiped, _ := out["wiped"].(map[string]any)
	n := func(k string) int64 { f, _ := wiped[k].(float64); return int64(f) }
	head := "Deleted"
	if dry {
		fmt.Fprintf(w, "Switching %s's partition mode (%s → %s) deletes %s.\n", tile, from, to, out["deletes"])
		head = "It deletes"
	} else {
		fmt.Fprintf(w, "%s now runs as %s; people's partitions start empty on first use.\n", tile, to)
	}
	fmt.Fprintf(w, "  %s: %d data namespace(s), %d person's partition(s), %d vault key(s), %d registration(s), %s; %d backup key(s) erased.\n",
		head, n("namespaces"), n("partitions"), n("vaultKeys"), n("registrations"), humanBytesBx(n("bytes")), n("subkeys"))
	if keeps, ok := out["keeps"].([]any); ok && dry {
		fmt.Fprintln(w, "  It keeps:")
		for _, k := range keeps {
			fmt.Fprintf(w, "    - %v\n", k)
		}
	}
	if m, ok := out["managers"].([]any); ok && len(m) > 0 {
		fmt.Fprintf(w, "  Sandbox managers that don't keep people apart (their hello lacks \"partitions\"): %v — --yes goes on anyway.\n", m)
	}
	if p, ok := out["people"].(float64); ok && p > 0 && !dry {
		fmt.Fprintf(w, "  %d person(s) whose partition was deleted were told.\n", int(p))
	}
	if a, ok := out["archiver"].(string); ok && a != "" {
		fmt.Fprintf(w, "  Archiver: %s\n", a)
	}
	if e, ok := out["eraseError"].(string); ok && e != "" {
		fmt.Fprintf(w, "  The backup keys are erased, but not every key file is removed yet: %s\n", e)
	}
}

// escapePath escapes each segment of a tile path for a URL.
func escapePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}
