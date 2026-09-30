package main

// bx partition ls|stop|reset|purge|limits|share-log|credential — operating
// people's partitions (docs/partitions.md §Operating people's partitions,
// docs/bx.md). They call GET /api/xbin/partitions and its acts as the
// person bx signs in as (or the root token); a tile's terminal gets the
// tile-level answer only. Against an xbind without the route they exit 6.

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
)

const partitionOpsUsage = `  bx partition ls [<tile>] [--json]     partitioned tiles, or one tile's partitions (yours;
                                        every person's metadata for admins)
  bx partition stop <tile> [--user <id>]
                                        stop a partition's instance (yours by default); data stays
  bx partition reset <tile> [--user <id>] --yes
                                        delete a partition's data (yours, or anyone's for admins)
  bx partition purge [<tile>] [--partition <id>]
                                        delete orphaned partitions now (admin)
  bx partition limits [<tile>] [--max-running n] [--partition-bytes n]
                                        running caps and per-partition byte ceilings
  bx partition share-log <tile> [--days n] [--stop]
                                        share your partition's backend log with its managers and admins
  bx partition credential <id> allow|refuse
                                        answer a credential an admin made for you
`

// partitionOpCmds are the subcommands of this file.
var partitionOpCmds = map[string]func([]string) error{
	"ls": partitionLs, "stop": partitionStop, "reset": partitionReset, "purge": partitionPurge,
	"limits": partitionLimits, "share-log": partitionShareLog, "credential": partitionCredential,
}

// me is the person bx acts as: the whoami answer's user id ("" for the root
// token).
func me() string {
	var who struct {
		Kind string `json:"kind"`
		ID   string `json:"id"`
	}
	if err := apiJSON("GET", "/api/xbin/whoami", nil, &who); err != nil || who.Kind != "user" {
		return ""
	}
	return who.ID
}

// opsFlags parses args: positional words, --json, and the value flags named.
func opsFlags(cmd string, args []string, values ...string) (pos []string, vals map[string]string, asJSON, yes bool, err error) {
	vals = map[string]string{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--json":
			asJSON = true
		case a == "--yes":
			yes = true
		case a == "--stop":
			vals["stop"] = "1"
		case strings.HasPrefix(a, "--") && containsStr(values, strings.TrimPrefix(a, "--")):
			v, e := nextArg(args, &i)
			if e != nil {
				return nil, nil, false, false, e
			}
			vals[strings.TrimPrefix(a, "--")] = v
		case isFlag(a):
			return nil, nil, false, false, unknownFlag("partition "+cmd, a, false)
		default:
			pos = append(pos, strings.Trim(a, "/"))
		}
	}
	return pos, vals, asJSON, yes, nil
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func partitionLs(args []string) error {
	pos, _, asJSON, _, err := opsFlags("ls", args)
	if err != nil {
		return err
	}
	q := ""
	if len(pos) > 0 {
		q = "?tile=" + url.QueryEscape(pos[0])
	}
	out, err := partitionAPI("GET", "/api/xbin/partitions"+q, nil)
	if err != nil {
		return err
	}
	if asJSON {
		return asJSONOut(out)
	}
	if len(pos) == 0 {
		tiles, _ := out["tiles"].([]any)
		if len(tiles) == 0 {
			fmt.Println("no partitioned tiles")
		}
		for _, t := range tiles {
			row := t.(map[string]any)
			line := fmt.Sprintf("%-28s %s", row["tile"], row["state"])
			if m, ok := row["mine"].(map[string]any); ok {
				line += fmt.Sprintf("  yours: %v, %s", m["state"], humanBytesBx(int64(fnum(m["bytes"]))))
			}
			if tot, ok := row["totals"].(map[string]any); ok {
				line += fmt.Sprintf("  people %d, running %d", int(fnum(tot["people"])), int(fnum(tot["running"])))
			}
			fmt.Println(line)
		}
		printHeld(out)
		return nil
	}
	fmt.Printf("%s: %s", out["tile"], out["state"])
	if out["request"] != nil {
		fmt.Printf(" (a mode switch request is open; bx partition switch|keep %s)", out["tile"])
	}
	fmt.Println()
	if tot, ok := out["totals"].(map[string]any); ok {
		fmt.Printf("  people %d, running %d, %s, cron jobs %d, bus subscriptions %d\n", int(fnum(tot["people"])), int(fnum(tot["running"])),
			humanBytesBx(int64(fnum(tot["bytes"]))), int(fnum(tot["cron"])), int(fnum(tot["bus"])))
	}
	rows, _ := out["partitions"].([]any)
	for _, r := range rows {
		row := r.(map[string]any)
		state := fmt.Sprint(row["state"])
		if row["running"] == true {
			state += ", running"
		}
		line := fmt.Sprintf("  %-20s %s, %s", row["partition"], state, humanBytesBx(int64(fnum(row["bytes"]))))
		if ls, ok := row["logShare"].(map[string]any); ok {
			line += fmt.Sprintf(", log shared until %v", ls["until"])
		}
		if row["crashLoop"] == true {
			line += ", crash loop"
		}
		fmt.Println(line)
	}
	if orph, ok := out["orphans"].([]any); ok && len(orph) > 0 {
		fmt.Printf("  %d orphaned partition(s) — bx partition purge %s\n", len(orph), out["tile"])
	}
	if tr, ok := out["trust"].(map[string]any); ok {
		for _, w := range asStrings(tr["warnings"]) {
			fmt.Println("  ! " + w)
		}
	}
	return nil
}

// printHeld prints the credentials waiting for the person, if any.
func printHeld(out map[string]any) {
	held, _ := out["credentials"].([]any)
	for _, h := range held {
		c := h.(map[string]any)
		fmt.Printf("! a %v for your account by %v waits for you (until %v): bx partition credential %v allow|refuse\n", c["kind"], c["by"], c["until"], c["id"])
	}
}

func asStrings(v any) []string {
	var out []string
	list, _ := v.([]any)
	for _, s := range list {
		out = append(out, fmt.Sprint(s))
	}
	return out
}

func fnum(v any) float64 { f, _ := v.(float64); return f }

// partitionOf is --user's partition key, or the caller's own.
func partitionOf(vals map[string]string) (string, error) {
	if u := vals["user"]; u != "" {
		return "user:" + u, nil
	}
	if id := me(); id != "" {
		return "user:" + id, nil
	}
	return "", errors.New("name the person (--user <id>): the root token has no partition")
}

func partitionStop(args []string) error {
	pos, vals, asJSON, _, err := opsFlags("stop", args, "user")
	if err != nil || len(pos) != 1 {
		return cmpErrBx(err, errors.New("usage:\n"+partitionOpsUsage))
	}
	part, err := partitionOf(vals)
	if err != nil {
		return err
	}
	out, err := partitionAPI("POST", "/api/xbin/partitions/stop", map[string]any{"tile": pos[0], "partition": part})
	if err != nil {
		return err
	}
	if asJSON {
		return asJSONOut(out)
	}
	fmt.Printf("%s's partition %s stopped; its data stays and the next request starts it\n", pos[0], part)
	return nil
}

func partitionReset(args []string) error {
	pos, vals, asJSON, yes, err := opsFlags("reset", args, "user")
	if err != nil || len(pos) != 1 {
		return cmpErrBx(err, errors.New("usage:\n"+partitionOpsUsage))
	}
	part, err := partitionOf(vals)
	if err != nil {
		return err
	}
	confirm := pos[0] + " " + part
	if !yes {
		fmt.Fprintf(os.Stderr, "This deletes every piece of %s's data in %s. Type %q to go on: ", part, pos[0], confirm)
		if !confirmLine(confirm) {
			return errors.New("not confirmed: nothing was deleted")
		}
	}
	out, err := partitionAPI("POST", "/api/xbin/partitions/reset", map[string]any{"tile": pos[0], "partition": part, "confirm": confirm})
	if err != nil {
		return err
	}
	if asJSON {
		return asJSONOut(out)
	}
	d, _ := out["deleted"].(map[string]any)
	fmt.Printf("%s's partition %s deleted: %d namespace(s), %s, %d backup key(s) erased\n", pos[0], part,
		int(fnum(d["namespaces"])), humanBytesBx(int64(fnum(d["bytes"]))), int(fnum(d["subkeys"])))
	return nil
}

func partitionPurge(args []string) error {
	pos, vals, asJSON, _, err := opsFlags("purge", args, "partition")
	if err != nil || len(pos) > 1 {
		return cmpErrBx(err, errors.New("usage:\n"+partitionOpsUsage))
	}
	body := map[string]any{}
	if len(pos) == 1 {
		body["tile"] = pos[0]
	}
	if p := vals["partition"]; p != "" {
		body["partition"] = p
	}
	out, err := partitionAPI("POST", "/api/xbin/partitions/purge", body)
	if err != nil {
		return err
	}
	if asJSON {
		return asJSONOut(out)
	}
	purged, _ := out["purged"].([]any)
	if len(purged) == 0 {
		fmt.Println("no orphaned partitions")
	}
	for _, p := range purged {
		row := p.(map[string]any)
		if e, ok := row["error"].(string); ok && e != "" {
			fmt.Printf("  %v %v (%v's, %v): %s\n", row["tile"], row["partition"], row["user"], row["reason"], e)
			continue
		}
		fmt.Printf("  %v %v (%v's, %v): deleted\n", row["tile"], row["partition"], row["user"], row["reason"])
	}
	return nil
}

func partitionLimits(args []string) error {
	pos, vals, asJSON, _, err := opsFlags("limits", args, "max-running", "partition-bytes")
	if err != nil || len(pos) > 1 {
		return cmpErrBx(err, errors.New("usage:\n"+partitionOpsUsage))
	}
	body := map[string]any{}
	if len(pos) == 1 {
		body["tile"] = pos[0]
	}
	for flag, key := range map[string]string{"max-running": "maxRunning", "partition-bytes": "partitionBytes"} {
		if v, ok := vals[flag]; ok {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return fmt.Errorf("--%s: %v", flag, err)
			}
			body[key] = n
		}
	}
	if body["maxRunning"] == nil && body["partitionBytes"] == nil {
		if len(pos) == 0 {
			return errors.New("usage:\n" + partitionOpsUsage)
		}
		out, err := partitionAPI("GET", "/api/xbin/partitions?tile="+url.QueryEscape(pos[0]), nil)
		if err != nil {
			return err
		}
		if asJSON {
			return asJSONOut(map[string]any{"tile": pos[0], "limits": out["limits"]})
		}
		l, _ := out["limits"].(map[string]any)
		fmt.Printf("%s: at most %d partitions running; %s each\n", pos[0], int(fnum(l["maxRunning"])), bytesOrNone(fnum(l["partitionBytes"])))
		return nil
	}
	out, err := partitionAPI("POST", "/api/xbin/partitions/limits", body)
	if err != nil {
		return err
	}
	if asJSON {
		return asJSONOut(out)
	}
	keys := make([]string, 0, len(out))
	for k := range out {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Printf("%s: %v\n", k, out[k])
	}
	return nil
}

func bytesOrNone(n float64) string {
	if n <= 0 {
		return "no per-partition ceiling"
	}
	return humanBytesBx(int64(n))
}

func partitionShareLog(args []string) error {
	pos, vals, asJSON, _, err := opsFlags("share-log", args, "days")
	if err != nil || len(pos) != 1 {
		return cmpErrBx(err, errors.New("usage:\n"+partitionOpsUsage))
	}
	body := map[string]any{"tile": pos[0]}
	method := "POST"
	if vals["stop"] != "" {
		method = "DELETE"
	} else if d := vals["days"]; d != "" {
		n, err := strconv.Atoi(d)
		if err != nil {
			return fmt.Errorf("--days: %v", err)
		}
		body["days"] = n
	}
	out, err := partitionAPI(method, "/api/xbin/partitions/share-log", body)
	if err != nil {
		return err
	}
	if asJSON {
		return asJSONOut(out)
	}
	if method == "DELETE" {
		fmt.Printf("your partition log of %s is no longer shared\n", pos[0])
		return nil
	}
	fmt.Printf("your partition log of %s is shared with its managers and admins until %v\n", pos[0], out["until"])
	return nil
}

func partitionCredential(args []string) error {
	pos, _, asJSON, _, err := opsFlags("credential", args)
	if err != nil || len(pos) != 2 || pos[1] != "allow" && pos[1] != "refuse" {
		return cmpErrBx(err, errors.New("usage:\n"+partitionOpsUsage))
	}
	out, err := partitionAPI("POST", "/api/xbin/partitions/credential-confirm", map[string]any{"id": pos[0], "allow": pos[1] == "allow"})
	if err != nil {
		return err
	}
	if asJSON {
		return asJSONOut(out)
	}
	fmt.Printf("the %v was %v\n", out["kind"], out["decision"])
	return nil
}

// cmpErrBx is err, or alt when err is nil.
func cmpErrBx(err, alt error) error {
	if err != nil {
		return err
	}
	return alt
}
