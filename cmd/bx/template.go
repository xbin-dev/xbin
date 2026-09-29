package main

import (
	"fmt"
	"strings"
)

// cmdTemplate manages template components (plans/templates.md):
//
//	bx template ls                          list templates (builtin + workspace)
//	bx template new <source> [as <path>] [--no-partition]
//	                                        instantiate one into a named copy
//	bx template updates                     instances behind their builtin template
//
// --no-partition: the copy doesn't start in the template's partition mode
// (docs/partitions.md; PD-35's opt-out) — it runs one backend for everyone.
func cmdTemplate(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: bx template ls | new <source> [as <path>] [--no-partition] | updates")
	}
	switch args[0] {
	case "ls":
		var tpls []struct {
			ID               string   `json:"id"`
			Source           string   `json:"source"`
			Title            string   `json:"title"`
			Description      string   `json:"description"`
			DefaultName      string   `json:"defaultName"`
			Partition        []string `json:"partition"`
			PartitionSkipped string   `json:"partitionSkipped"`
		}
		if err := apiJSON("GET", "/api/xbin/templates", nil, &tpls); err != nil {
			return err
		}
		if len(tpls) == 0 {
			fmt.Println("(no templates)")
			return nil
		}
		for _, t := range tpls {
			mode := ""
			if len(t.Partition) > 0 {
				mode = "  [partitioned: " + strings.Join(t.Partition, "+")
				if t.PartitionSkipped != "" {
					mode += ", " + t.PartitionSkipped
				}
				mode += "]"
			}
			fmt.Printf("%-10s %-20s %s%s\n", t.Source, t.ID, t.Title, mode)
		}
		fmt.Println("\ninstantiate: bx template new <source> [as <path>] [--no-partition]")
		return nil

	case "updates":
		var out struct {
			Instances []struct {
				Path     string `json:"path"`
				Template string `json:"template"`
				Head     string `json:"head"`
				Legacy   bool   `json:"legacy"`
			} `json:"instances"`
		}
		if err := apiJSON("GET", "/api/xbin/templates/updates", nil, &out); err != nil {
			return err
		}
		if len(out.Instances) == 0 {
			fmt.Println("every template instance is up to date with its template")
			return nil
		}
		for _, i := range out.Instances {
			note := ""
			if i.Legacy {
				note = "  (pre-seeding instance: first merge needs --allow-unrelated-histories)"
			}
			fmt.Printf("%-24s behind template %q (%s)%s\n", i.Path, i.Template, i.Head, note)
		}
		fmt.Println("\napply in the instance's terminal (you pick what to adopt — it's a fork):\n  git fetch template && git merge template/main    # or cherry-pick")
		return nil

	case "new":
		// --no-partition goes wherever it stands; everything else parses by
		// position exactly as before it existed (compat: never break users).
		noPartition := false
		rest := []string{}
		for _, a := range args {
			if a == "--no-partition" {
				noPartition = true
			} else {
				rest = append(rest, a)
			}
		}
		args = rest
		if len(args) < 2 {
			return fmt.Errorf("usage: bx template new <source> [as <path>] [--no-partition]")
		}
		source := args[1]
		path := ""
		if len(args) >= 4 && args[2] == "as" {
			path = args[3]
		} else if len(args) == 3 {
			path = args[2]
		}
		var out struct {
			Path          string `json:"path"`
			PendingGrants []struct {
				From, Target, Role string
			} `json:"pendingGrants"`
			Partition        []string `json:"partition"`
			PartitionSkipped string   `json:"partitionSkipped"`
		}
		body := map[string]any{"source": source}
		if path != "" {
			body["path"] = path
		}
		if noPartition {
			body["partition"] = false
		}
		if err := apiJSON("POST", "/api/xbin/templates/new", body, &out); err != nil {
			if noPartition && strings.Contains(err.Error(), "need {source, path?, owner?}") {
				return fmt.Errorf("%w — this xbind predates partitioned tiles (docs/partitions.md), so its instances never start partitioned: run it without --no-partition", err)
			}
			return err
		}
		fmt.Printf("created %s\nframe it:  <bx-frame src=%q></bx-frame>\n", out.Path, out.Path)
		switch {
		case len(out.Partition) > 0:
			fmt.Printf("partitioned (%s): each person gets their own backend and data (docs/partitions.md); --no-partition opts out\n", strings.Join(out.Partition, "+"))
		case out.PartitionSkipped == "needs --isolate":
			fmt.Println("not partitioned: the template keeps each person's data apart, which needs xbind --isolate")
		}
		if len(out.PendingGrants) > 0 {
			fmt.Println("\nthis component needs grants (approve them, or it 403s):")
			for _, g := range out.PendingGrants {
				fmt.Printf("  bx grant %s %s:%s\n", g.From, g.Target, g.Role)
			}
		}
		return nil
	}
	return fmt.Errorf("unknown: bx template %s", strings.Join(args, " "))
}
