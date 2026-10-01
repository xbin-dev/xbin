package main

// A person's partition's backups (docs/partitions.md §Backups; POST
// /api/xbin/partitions/restore, GET /api/xbin/partitions/backups):
//
//	bx backups <tile> --partition [--user <id>] [--partition-id u-…]
//	bx restore <tile> --partition [--user <id>] [--version v] [--partition-id u-…]
//	           [--to <id>] [--dry-run] [--yes] [--json]
//
// Your own partition from your own session (bx with your login), anyone's
// as an admin (--user); a tile's terminal can't. A restore first checks the
// archive (a dry run), then asks you to type "<tile> user:<id>" — --yes
// types it. --partition-id names an earlier holder's partition (the id
// deleted and recreated since): an admin restores it into the person's
// partition only with --to <id>, the same id again. Against an xbind
// without people's partition archives both exit 6.

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

// partitionBackupFlags are the flags of both commands.
type partitionBackupFlags struct {
	tile, user, pid, version, to string
	dry, yes, asJSON             bool
}

func parsePartitionBackup(cmd string, args []string, restore bool) (partitionBackupFlags, error) {
	var f partitionBackupFlags
	for i := 0; i < len(args); i++ {
		var err error
		switch a := args[i]; {
		case a == "--partition":
		case a == "--user":
			f.user, err = nextArg(args, &i)
		case a == "--partition-id":
			f.pid, err = nextArg(args, &i)
		case a == "--version" && restore:
			f.version, err = nextArg(args, &i)
		case a == "--to" && restore:
			f.to, err = nextArg(args, &i)
		case a == "--dry-run" && restore:
			f.dry = true
		case a == "--yes" && restore:
			f.yes = true
		case a == "--json":
			f.asJSON = true
		case isFlag(a):
			return f, unknownFlag(cmd+" --partition", a, false)
		case f.tile != "":
			return f, fmt.Errorf("usage: bx %s <tile> --partition [--user <id>] …", cmd)
		default:
			f.tile = strings.Trim(a, "/")
		}
		if err != nil {
			return f, err
		}
	}
	if f.tile == "" {
		return f, fmt.Errorf("usage: bx %s <tile> --partition [--user <id>] …", cmd)
	}
	return f, nil
}

// hasFlag reports whether args carry flag.
func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

func cmdBackupsPartition(args []string) error {
	f, err := parsePartitionBackup("backups", args, false)
	if err != nil {
		return err
	}
	q := url.Values{"tile": {f.tile}}
	if f.user != "" {
		q.Set("user", f.user)
	}
	if f.pid != "" {
		q.Set("partitionId", f.pid)
	}
	out, err := partitionBackupCall("GET", "/api/xbin/partitions/backups?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	if f.asJSON {
		return json.NewEncoder(os.Stdout).Encode(out)
	}
	vs, _ := out["versions"].([]any)
	fmt.Printf("%s's %s partition (%v)\n", f.tile, out["partition"], out["partitionId"])
	if len(vs) == 0 {
		fmt.Println("no backups")
		return nil
	}
	for _, v := range vs {
		row, _ := v.(map[string]any)
		fmt.Printf("%-32v %v\t%v bytes\n", row["version"], row["time"], row["size"])
	}
	return nil
}

func cmdRestorePartition(args []string) error {
	f, err := parsePartitionBackup("restore", args, true)
	if err != nil {
		return err
	}
	body := map[string]any{"tile": f.tile, "dryRun": true}
	for k, v := range map[string]string{"user": f.user, "partitionId": f.pid, "version": f.version, "to": f.to} {
		if v != "" {
			body[k] = v
		}
	}
	check, err := partitionBackupCall("POST", "/api/xbin/partitions/restore", body)
	if err != nil {
		return err
	}
	if f.dry {
		if f.asJSON {
			return json.NewEncoder(os.Stdout).Encode(check)
		}
		fmt.Printf("Restoring %s's %v partition from version %v replaces its data, vault and registrations (dry run: nothing changed).\n", f.tile, check["partition"], check["version"])
		return nil
	}
	want := fmt.Sprintf("%s %v", f.tile, check["partition"])
	delete(body, "dryRun")
	body["version"] = check["version"] // the version checked, not a newer one
	if f.yes {
		body["confirm"] = want
	} else {
		fmt.Fprintf(os.Stderr, "Restoring replaces %s's %v partition — its data, vault and registrations — with version %v.\nType %q to restore: ",
			f.tile, check["partition"], check["version"], want)
		if !confirmLine(want) {
			return errors.New("not confirmed: nothing was restored")
		}
		body["confirm"] = want
	}
	out, err := partitionBackupCall("POST", "/api/xbin/partitions/restore", body)
	if err != nil {
		return err
	}
	if f.asJSON {
		return json.NewEncoder(os.Stdout).Encode(out)
	}
	fmt.Printf("restored %s's %v partition from version %v\n", f.tile, out["partition"], out["version"])
	if s, _ := out["vaultSkipped"].(string); s != "" {
		fmt.Fprintln(os.Stderr, "bx: "+s)
	}
	return nil
}

// partitionBackupsAnswer is POST /backup's partitions: what the backup of a
// partitioned tile did with its people's partitions.
type partitionBackupsAnswer struct {
	Archived int      `json:"archived"`
	Failed   []string `json:"failed"`
	Skipped  string   `json:"skipped"`
}

// report prints it: a person's partition not backed up fails bx backup
// (exit 1, the tile's own archive written all the same); a plaintext-vault
// workspace's skipped partitions are a warning.
func (p *partitionBackupsAnswer) report(tile string) error {
	if p == nil {
		return nil
	}
	if p.Archived > 0 {
		fmt.Printf("  and %d people's partitions, each in an archive of its own\n", p.Archived)
	}
	if p.Skipped != "" {
		fmt.Fprintln(os.Stderr, "bx: warning: "+p.Skipped)
	}
	for _, f := range p.Failed {
		fmt.Fprintln(os.Stderr, "bx: a person's partition isn't backed up: "+f)
	}
	if len(p.Failed) > 0 {
		return fmt.Errorf("%d people's partitions of %s aren't backed up (the tile's own archive is)", len(p.Failed), tile)
	}
	return nil
}

// predatesConfirm explains the 400 of an xbind older than bx restore
// --confirm: it refuses the body's unknown field, naming the body it knows.
func predatesConfirm(err error, confirm string) error {
	if confirm != "" && strings.Contains(err.Error(), "need {component, version?, file?}") {
		return fmt.Errorf("%w — this xbind predates bx restore --confirm (it has no partition mode switch to confirm): run the restore without it", err)
	}
	return err
}

// partitionBackupCall calls one of the routes; an xbind without them exits
// 6 (compat rule 8: a mux 404 carries no JSON error).
func partitionBackupCall(method, path string, body any) (map[string]any, error) {
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
		fmt.Fprintln(os.Stderr, "bx: this xbind has no people's partition archives (no "+strings.SplitN(path, "?", 2)[0]+"); upgrade xbind")
		os.Exit(exitNoPartitions)
	case resp.StatusCode >= 400:
		msg := fmt.Sprint(out["error"])
		if out["error"] == nil {
			msg = strings.TrimSpace(string(b))
		}
		return out, fmt.Errorf("%s (%s)", msg, resp.Status)
	}
	return out, nil
}
