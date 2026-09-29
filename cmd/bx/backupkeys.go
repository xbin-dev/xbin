package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

// Sealed backups' keys and erase (plans/partitions/11-backup-encryption.md
// §3, §5; docs/overview/14-lifecycle.md §Sealed archives):
//
//	bx backup keys status                 how archives are sealed, and the export status
//	bx backup keys export > keys.xbk      the disaster-recovery key bundle (admin)
//	bx backup keys import keys.xbk        another workspace's keys; prompts for its vault passphrase
//	bx backup erase <tile> --data|--all [--yes]   crypto-erase a tile's backups
//
// `bx backup <component>` keeps backing up a component named keys or erase:
// these forms take more arguments.

const backupKeysUsage = "usage: bx backup keys status | export > keys.xbk | import keys.xbk"

func cmdBackupKeys(args []string) error {
	switch args[0] {
	case "status":
		return backupKeysStatus()
	case "export":
		if len(args) != 1 {
			return fmt.Errorf("%s", backupKeysUsage)
		}
		var bundle json.RawMessage
		if err := apiJSON("POST", "/api/xbin/backup-keys/export", map[string]any{}, &bundle); err != nil {
			return err
		}
		if _, err := os.Stdout.Write(append(bundle, '\n')); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "bx: exported the backup key bundle. Keep it with the vault passphrase — without the passphrase it opens nothing, "+
			"and without it a new machine can't restore sealed backups. Export again after erasing backups, and destroy older bundles: they still hold erased keys.")
		return nil
	case "import":
		if len(args) != 2 {
			return fmt.Errorf("%s", backupKeysUsage)
		}
		data, err := os.ReadFile(args[1])
		if err != nil {
			return err
		}
		var bundle json.RawMessage
		if err := json.Unmarshal(data, &bundle); err != nil {
			return fmt.Errorf("%s isn't a key bundle (bx backup keys export writes one): %w", args[1], err)
		}
		pass, err := readPassphrase("the exporting workspace's vault passphrase: ")
		if err != nil {
			return err
		}
		var out struct{ Imported, Present, Erased int }
		if err := apiJSON("POST", "/api/xbin/backup-keys/import", map[string]any{"bundle": bundle, "passphrase": pass}, &out); err != nil {
			return err
		}
		fmt.Printf("imported %d backup key(s) (%d already here), %d erasure(s)\n", out.Imported, out.Present, out.Erased)
		return nil
	}
	return fmt.Errorf("%s", backupKeysUsage)
}

func backupKeysStatus() error {
	var st struct {
		Mode              string `json:"mode"`
		Keys              int    `json:"keys"`
		Unexported        int    `json:"unexported"`
		LastExport        string `json:"lastExport"`
		Erased            int    `json:"erased"`
		ErasedSinceExport int    `json:"erasedSinceExport"`
	}
	if err := apiJSON("GET", "/api/xbin/backup-keys", nil, &st); err != nil {
		return err
	}
	switch st.Mode {
	case "plaintext":
		fmt.Println("archives: plain tars — no vault barrier, so backups aren't sealed (bx vault unseal sets one up)")
	case "vault-sealed":
		fmt.Println("archives: sealed — but the vault is sealed, so no backup runs until it is unsealed (bx vault unseal)")
	default:
		fmt.Println("archives: sealed under backup keys")
	}
	last := st.LastExport
	if last == "" {
		last = "never"
	}
	fmt.Printf("keys: %d (%d in no export yet) · last export: %s · erased: %d (%d since the last export)\n",
		st.Keys, st.Unexported, last, st.Erased, st.ErasedSinceExport)
	if st.Unexported > 0 {
		fmt.Println("export them: bx backup keys export > keys.xbk — restoring sealed backups on a new machine needs the bundle and the vault passphrase")
	}
	return nil
}

// doctorBackupKeys is bx doctor's line on the backup keys: plain archives
// without a vault barrier, keys no exported bundle holds. Skipped without
// admin credentials, and against an xbind without the route.
func doctorBackupKeys(warn, ok func(string, ...any)) {
	var st struct {
		Mode              string `json:"mode"`
		Keys, Unexported  int
		ErasedSinceExport int `json:"erasedSinceExport"`
		LastExport        string
	}
	if apiJSON("GET", "/api/xbin/backup-keys", nil, &st) != nil {
		return
	}
	switch {
	case st.Mode == "plaintext": // --insecure-vault / --no-auth: said, not a problem
		fmt.Println("  · backups are plain tars: no vault barrier, so nothing seals them (bx vault unseal sets one up)")
	case st.Unexported > 0:
		warn("%d backup key(s) aren't in any exported bundle — a new machine can't restore sealed backups without it: bx backup keys export > keys.xbk", st.Unexported)
	case st.ErasedSinceExport > 0:
		warn("%d backup key(s) were erased since the last key export: export again and destroy older bundles (they still hold them)", st.ErasedSinceExport)
	default:
		ok("backups are sealed; %d backup key(s), all in an exported bundle", st.Keys)
	}
}

// cmdBackupErase is bx backup erase <tile> --data|--all [--yes].
func cmdBackupErase(args []string) error {
	var tile, what string
	yes := false
	for _, a := range args {
		switch a {
		case "--data":
			what = "data"
		case "--all":
			what = "all"
		case "--yes", "-y":
			yes = true
		default:
			if isFlag(a) {
				return fmt.Errorf("unknown flag %s", a)
			}
			tile = a
		}
	}
	if tile == "" || what == "" {
		return fmt.Errorf("usage: bx backup erase <tile> --data|--all [--yes]")
	}
	if !yes {
		scope := "its data (main's and every deployment's) — its source stays restorable"
		if what == "all" {
			scope = "everything: source, terminal layer and data"
		}
		fmt.Fprintf(os.Stderr, "This crypto-erases %s's backups: %s, in every archive, for good.\nType the tile's path to confirm: ", tile, scope)
		if !confirmLine(tile) {
			return fmt.Errorf("not confirmed: nothing was erased")
		}
	}
	var out struct {
		Erased []struct {
			ID, Subject string
			Gen         int
		}
		Archiver string
	}
	if err := apiJSON("POST", "/api/xbin/backup/erase", map[string]string{"component": tile, "what": what}, &out); err != nil {
		return err
	}
	if len(out.Erased) == 0 {
		fmt.Printf("%s has no backup keys to erase (a workspace without a vault barrier writes plain archives: delete them at the archiver)\n", tile)
		return nil
	}
	for _, e := range out.Erased {
		fmt.Printf("erased %s (%s, generation %d)\n", e.ID, e.Subject, e.Gen)
	}
	if out.Archiver != "" {
		fmt.Println(out.Archiver)
	}
	fmt.Println("Export the key bundle again and destroy older ones: they still hold the erased keys (bx backup keys export).")
	return nil
}

// confirmLine reads one line (without echo trouble: it isn't secret) and
// reports whether it is want.
func confirmLine(want string) bool {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprintln(os.Stderr)
	}
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.TrimSpace(line) == want
}
