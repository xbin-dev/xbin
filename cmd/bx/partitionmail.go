package main

// bx partition mail ls|ack — partition mail from where it is read
// (docs/partitions.md §Partition mail, docs/bx.md; plans/partitions/06 §7):
// the inbox of the credential bx runs with — in a person's terminal on a
// partitioned tile, that person's partition's; with the global instance's
// backend token, global's. Nobody else reads an inbox (403: admins, the
// root token and other tiles included). Against an xbind without partition
// mail it exits 6.

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
	"text/tabwriter"
	"time"
)

const partitionMailUsage = `  bx partition mail ls [--after <id>] [--limit n] [--json]
                                        this partition's inbox, oldest first (in a
                                        person's terminal on a partitioned tile)
  bx partition mail ack <id>...         acknowledge items: they are gone
`

// errNoMailRoute: an xbind without partition mail. It is errNoPartitions
// for cmdPartition (exit 6), in its own words.
type errNoMailRoute struct{}

func (errNoMailRoute) Error() string {
	return "this xbind has no partition mail (partition-mail/1); upgrade xbind"
}

func (errNoMailRoute) Is(target error) bool { return target == errNoPartitions }

// mailAPI calls a partition mail route; Go's mux 404/405 (no error body)
// is errNoMailRoute.
func mailAPI(method, path string, body any) ([]byte, error) {
	resp, err := api(method, path, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	var e struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(b, &e)
	switch {
	case (resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed) && e.Error == "":
		return nil, errNoMailRoute{}
	case resp.StatusCode >= 400:
		msg := e.Error
		if msg == "" {
			msg = strings.TrimSpace(string(b))
		}
		return nil, fmt.Errorf("%s (%s)", msg, resp.Status)
	}
	return b, nil
}

func partitionMail(args []string) error {
	if len(args) == 0 {
		return errors.New("usage:\n" + partitionMailUsage)
	}
	switch args[0] {
	case "ls":
		return partitionMailLs(args[1:])
	case "ack":
		return partitionMailAck(args[1:])
	}
	return fmt.Errorf("bx partition mail: unknown subcommand %q (ls, ack)", args[0])
}

// mailLsItem is an item as GET /partitions/mail answers it.
type mailLsItem struct {
	ID      string          `json:"id"`
	From    string          `json:"from"`
	Topic   string          `json:"topic"`
	Data    json.RawMessage `json:"data"`
	At      time.Time       `json:"at"`
	Expires time.Time       `json:"expires"`
}

func partitionMailLs(args []string) error {
	q := url.Values{}
	asJSON := false
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--json":
			asJSON = true
		case (a == "--after" || a == "--limit") && i+1 < len(args):
			if a == "--limit" {
				if n, err := strconv.Atoi(args[i+1]); err != nil || n < 1 {
					return fmt.Errorf("bx partition mail ls: --limit wants a number, 1 to 1000")
				}
			}
			q.Set(strings.TrimPrefix(a, "--"), args[i+1])
			i++
		default:
			return errors.New("usage:\n" + partitionMailUsage)
		}
	}
	path := "/api/xbin/partitions/mail"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	b, err := mailAPI("GET", path, nil)
	if err != nil {
		return err
	}
	if asJSON {
		_, err := os.Stdout.Write(b)
		return err
	}
	var pg struct {
		Items []mailLsItem `json:"items"`
		More  bool         `json:"more"`
	}
	if err := json.Unmarshal(b, &pg); err != nil {
		return fmt.Errorf("bx partition mail ls: %w", err)
	}
	printMailLs(os.Stdout, pg.Items, pg.More)
	return nil
}

// printMailLs prints a page of the inbox: one row an item, its data cut short.
func printMailLs(w io.Writer, items []mailLsItem, more bool) {
	if len(items) == 0 {
		fmt.Fprintln(w, "No partition mail waiting.")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tFROM\tTOPIC\tAT\tEXPIRES\tDATA")
	for _, it := range items {
		data := strings.ReplaceAll(string(it.Data), "\n", " ")
		if len(data) > 60 {
			data = data[:57] + "..."
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", it.ID, it.From, it.Topic,
			it.At.Local().Format("2006-01-02 15:04"), it.Expires.Local().Format("2006-01-02 15:04"), data)
	}
	_ = tw.Flush()
	if more {
		fmt.Fprintf(w, "More wait: bx partition mail ls --after %s\n", items[len(items)-1].ID)
	}
}

func partitionMailAck(ids []string) error {
	if len(ids) == 0 {
		return errors.New("usage:\n" + partitionMailUsage)
	}
	if _, err := mailAPI("POST", "/api/xbin/partitions/mail/ack", map[string]any{"ids": ids}); err != nil {
		return err
	}
	fmt.Printf("Acknowledged %d item(s).\n", len(ids))
	return nil
}
