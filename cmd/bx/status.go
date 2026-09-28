package main

// status.go — `bx status` (a tile's runtime metrics, or the admin's global
// view) and `bx logs` (a backend's log); with a deployment named, by
// --deployment or by $XBIN_DEPLOYMENT when the tile is $XBIN_COMPONENT
// (DR3), that deployment's, resolved through GET /deployments and checked
// against the answer's echo (11-contract §8, §9.3). Also what each
// deployment of a tile runs: `bx deployment ls` and its edges.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/util"
)

// cmdStatus prints one tile's runtime metrics (default: the terminal's own
// tile, via $XBIN_COMPONENT). `bx status <component>` targets another (admin,
// or self). `bx status --all` is the admin global view. Read-only.
func cmdStatus(args []string) error {
	args, dep, given, err := takeDeployment("status", args)
	if err != nil {
		return dcCommand(func([]string) error { return err })(nil)
	}
	all := false
	comp := os.Getenv("XBIN_COMPONENT")
	for _, a := range args {
		if a == "--all" {
			all = true
		} else if a != "" {
			comp = a
		}
	}
	if !given && !all { // <tile>+<name>: that deployment, asked with deployment= (D127j)
		if t, d := readRef(comp); d != "" {
			comp, dep, given = t, d, true
		}
	}
	if env := os.Getenv("XBIN_COMPONENT"); !given && !all && comp != "" && comp == env {
		dep = os.Getenv("XBIN_DEPLOYMENT") // a read command's default (DR3)
	}
	if given || dep != "" {
		return dcCommand(func([]string) error { return statusOf(comp, dep, all) })(nil)
	}
	// No tile context (a host shell, not a tile terminal) or --all → the admin
	// global view. In a tile terminal, $XBIN_COMPONENT scopes it to that tile.
	if all || comp == "" {
		var backends, status map[string]any
		if err := apiJSON("GET", "/api/xbin/backends", nil, &backends); err != nil {
			return err
		}
		_ = apiJSON("GET", "/api/xbin/status", nil, &status)
		b, _ := json.MarshalIndent(map[string]any{"backends": backends, "status": status}, "", "  ")
		fmt.Println(string(b))
		return nil
	}
	var st tileStatus
	if err := apiJSON("GET", "/api/xbin/tile-status?component="+url.QueryEscape(comp), nil, &st); err != nil {
		return err
	}
	printTileStatus(&st)
	printStatusDeployments(&st, comp, nil)
	return nil
}

// statusOf prints deployment dep's status: GET /tile-status with
// ?deployment=, after GET /deployments has resolved the tile and found dep.
// An answer that doesn't echo dep was answered for the primary by an xbind
// that doesn't know deployments, and is never shown as dep's.
func statusOf(comp, dep string, all bool) error {
	switch {
	case all:
		return usageError("status", "--deployment names one tile's deployment; --all is every tile's status")
	case comp == "":
		return usageError("status", "which tile? name it, or run bx in the tile's terminal")
	}
	if err := checkName("status", dep); err != nil {
		return err
	}
	st, err := resolveDeployment(comp, dep)
	if err != nil {
		return err
	}
	q := url.Values{"component": {st.Tile}}
	if st.Record || dep != st.primary() {
		q.Set("deployment", dep)
	}
	b, err := dcCall("GET", "/api/xbin/tile-status?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	var ts tileStatus
	if err := decodeAnswer(b, &ts); err != nil {
		return err
	}
	if q.Has("deployment") && !echoes(ts.Deployment, dep, st.primary()) {
		return noEcho("/tile-status", st, dep)
	}
	if dep != st.primary() {
		ts.Component = st.Tile + "+" + dep
	}
	printTileStatus(&ts)
	printStatusDeployments(&ts, st.Tile, st)
	return nil
}

// takeDeployment takes --deployment <name> (or --deployment=<name>) out of a
// lenient command line, leaving every other argument as it was.
func takeDeployment(cmd string, args []string) (rest []string, dep string, given bool, err error) {
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--deployment":
			if dep, err = nextArg(args, &i); err != nil {
				return nil, "", false, usageError(cmd, "%v", err)
			}
			given = true
		case strings.HasPrefix(a, "--deployment="):
			dep, given = strings.TrimPrefix(a, "--deployment="), true
		default:
			rest = append(rest, a)
		}
	}
	return rest, dep, given, nil
}

// resolveDeployment reads the tile's state (an xbind without tile
// deployments: exit 6) and checks dep is one of its deployments.
func resolveDeployment(tile, dep string) (*deployState, error) {
	st, _, err := getDeployState(tile)
	if err != nil {
		return nil, err
	}
	if st.View != "reader" && st.deployment(dep) == nil {
		return nil, &dcError{code: exitFailed, msg: fmt.Sprintf("%s has no deployment %q", st.Tile, dep)}
	}
	return st, nil
}

// echoes: an answer for dep echoed it, or answered for the primary, which is
// dep, without a marker (the role rule, 11-contract §0.2).
func echoes(got, dep, primary string) bool {
	return got == dep || got == "" && dep == primary
}

func noEcho(route string, st *deployState, dep string) error {
	return &dcError{code: exitNoDeployments, msg: fmt.Sprintf("this xbind's %s doesn't know deployments: it answered for the primary of %s, not %s; upgrade xbind", route, st.Tile, dep)}
}

// printStatusDeployments prints the deployments line of a tile with a
// record (10-ux §8.5), from the answer's deployments summary: where saves
// go, what the primary runs, and this terminal's target.
func printStatusDeployments(s *tileStatus, tile string, st *deployState) {
	d := s.Deployments
	if d == nil {
		return
	}
	p := firstOf(d.Primary, "main")
	pinned := ""
	var items []string
	for _, it := range d.Items {
		cp := it.checkpoint()
		if it.Name == p && cp != "" {
			pinned = p + " pinned to " + cp
		}
		item := it.Name + " " + it.State
		if it.Gen > 0 {
			item += fmt.Sprintf(" g%d", it.Gen)
		}
		items = append(items, item)
	}
	var line string
	switch {
	case d.LiveReload == "":
		line = "live reload paused"
		if st != nil && st.WorkTree != nil && st.WorkTree.Since != "" {
			line += fmt.Sprintf(" · %s changed since %s", nFiles(st.WorkTree.Changed), st.WorkTree.Since)
		} else if pinned != "" {
			line += " · " + pinned
		}
	case d.LiveReload == p:
		line = "live reload: " + p + " (primary)"
	default:
		line = "live reload: " + d.LiveReload
		if pinned != "" {
			line += " · " + pinned
		}
	}
	if os.Getenv("XBIN_COMPONENT") == tile {
		line += " · this terminal → " + firstOf(os.Getenv("XBIN_DEPLOYMENT"), p)
	}
	fmt.Printf("  deployments %s\n", line)
	if len(items) > 1 {
		fmt.Printf("              %s\n", strings.Join(items, " · "))
	}
}

type tileStatus struct {
	Component string `json:"component"`
	// A tile with a record (11-contract §8): the deployment reported, and
	// for admins and terminal tokens the summary of every deployment.
	Deployment  string `json:"deployment"`
	Deployments *struct {
		Primary    string   `json:"primary"`
		LiveReload string   `json:"liveReload"`
		Items      []tsItem `json:"items"`
	} `json:"deployments"`
	Backend *struct {
		State       string   `json:"state"`
		Gen         int      `json:"gen"`
		Restarts    int      `json:"restarts"`
		ActiveConns int      `json:"activeConns"`
		UptimeSec   int64    `json:"uptimeSec"`
		RSSKB       int64    `json:"rssKb"`
		FDs         int      `json:"fds"`
		Threads     int      `json:"threads"`
		CPUSec      float64  `json:"cpuSec"`
		Error       string   `json:"error"`
		Egress      []string `json:"egress"`
		Cgroup      *struct {
			MemCurrent  int64 `json:"memCurrent"`
			MemMax      int64 `json:"memMax"`
			PidsCurrent int64 `json:"pidsCurrent"`
		} `json:"cgroup"`
	} `json:"backend"`
	Net struct { // the effective network (D54)
		Ref    string   `json:"netRef"`
		Mode   string   `json:"net"`
		Rules  []string `json:"netRules"`
		Source string   `json:"netSource"`
		Note   string   `json:"netNote"`
	} `json:"net"`
	Disk struct {
		UsageBytes int64 `json:"usageBytes"`
		QuotaBytes int64 `json:"quotaBytes"`
		Blocked    bool  `json:"blocked"`
	} `json:"disk"`
	Alerts []struct {
		Level   string `json:"level"`
		Message string `json:"message"`
	} `json:"alerts"`
}

func printTileStatus(s *tileStatus) {
	fmt.Println(s.Component)
	if b := s.Backend; b != nil {
		up := ""
		if b.UptimeSec > 0 {
			up = " · up " + (time.Duration(b.UptimeSec) * time.Second).String()
		}
		rs := ""
		if b.Restarts > 0 {
			rs = fmt.Sprintf(" · %d restart(s)", b.Restarts)
		}
		fmt.Printf("  backend    %s · gen %d%s%s\n", b.State, b.Gen, up, rs)
		if b.Error != "" {
			fmt.Printf("  error      %s\n", b.Error)
		}
		mem := ""
		if b.Cgroup != nil {
			mem = fmt.Sprintf("   mem %s", humanBytesBx(b.Cgroup.MemCurrent))
			if b.Cgroup.MemMax > 0 {
				mem += " / " + humanBytesBx(b.Cgroup.MemMax)
			}
			mem += fmt.Sprintf("   pids %d", b.Cgroup.PidsCurrent)
		}
		fmt.Printf("  cpu        %.1fs%s   fds %d   conns %d\n", b.CPUSec, mem, b.FDs, b.ActiveConns)
		if len(b.Egress) > 0 {
			fmt.Printf("  egress     %s\n", strings.Join(b.Egress, ", "))
		}
	} else {
		fmt.Println("  backend    not running")
	}
	if n := s.Net; n.Ref != "" || n.Mode != "" {
		line := n.Ref
		if line == "" {
			line = "unbound"
		}
		if n.Source != "" {
			line += " → " + n.Source
		}
		if n.Mode != "" {
			line += " (" + n.Mode + ")"
		}
		fmt.Printf("  net        %s\n", line)
		if n.Note != "" {
			fmt.Printf("  ⚠ net      %s\n", n.Note)
		}
	}
	dq := ""
	if s.Disk.QuotaBytes > 0 {
		dq = " / " + humanBytesBx(s.Disk.QuotaBytes)
	}
	blk := ""
	if s.Disk.Blocked {
		blk = "  ⛔ writes blocked"
	}
	fmt.Printf("  disk       %s%s%s\n", humanBytesBx(s.Disk.UsageBytes), dq, blk)
	for _, a := range s.Alerts {
		icon := "⚡"
		if a.Level == "crit" {
			icon = "⚠"
		}
		fmt.Printf("  alert      %s %s\n", icon, a.Message)
	}
}

func humanBytesBx(n int64) string {
	const u = 1024
	if n < u {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(u), 0
	for x := n / u; x >= u; x /= u {
		div *= u
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(n)/float64(div), "KMGTPE"[exp])
}

func cmdLogs(args []string) error {
	args, dep, given, err := takeDeployment("logs", args)
	if err != nil {
		return dcCommand(func([]string) error { return err })(nil)
	}
	follow := false
	var comp string
	for _, a := range args {
		if a == "-f" {
			follow = true
		} else {
			comp = a
		}
	}
	if !given { // <tile>+<name>: that deployment, asked with deployment= (D127j)
		if t, d := readRef(comp); d != "" {
			comp, dep, given = t, d, true
		}
	}
	if env := os.Getenv("XBIN_COMPONENT"); given && comp == "" || !given && comp != "" && comp == env {
		comp, dep = firstOf(comp, env), firstOf(dep, os.Getenv("XBIN_DEPLOYMENT"))
	}
	if given || dep != "" {
		return dcCommand(func([]string) error { return logsOf(comp, dep, follow) })(nil)
	}
	if comp == "" {
		return fmt.Errorf("usage: bx logs [-f] <component>")
	}
	ws := workspaceRoot()
	if ws == "" {
		return fmt.Errorf("not inside a xbin workspace")
	}
	if fi, err := os.Stat(filepath.Join(ws, ".xbin", "log")); err != nil || !fi.IsDir() {
		// .xbin is masked in an isolated terminal (internal/term/binds.go),
		// so the file can't answer: xbind serves the same log (16-open-questions Q23).
		return logsFromAPI(comp, follow)
	}
	path := filepath.Join(ws, ".xbin", "log", util.CompKey(comp)+".log")
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("no logs for %s yet (%s)", comp, path)
	}
	defer f.Close()
	if fi, _ := f.Stat(); fi != nil && fi.Size() > 64<<10 && follow {
		_, _ = f.Seek(-64<<10, io.SeekEnd)
	}
	if _, err := io.Copy(os.Stdout, f); err != nil {
		return err
	}
	for follow {
		time.Sleep(300 * time.Millisecond)
		if _, err := io.Copy(os.Stdout, f); err != nil {
			return err
		}
	}
	return nil
}

// logsFromAPI streams a backend's log from GET /logs, what `bx logs` does
// where it can't read .xbin/log itself: the whole log up to the route's
// 1 MiB tail, or with -f the last 64 KiB and then everything appended.
func logsFromAPI(comp string, follow bool) error {
	q := url.Values{"component": {comp}}
	if follow {
		q.Set("follow", "1")
	} else {
		q.Set("tail", strconv.Itoa(1<<20))
	}
	base, client := transport()
	c := *client
	if follow {
		c.Timeout = 0 // a follow streams until it is interrupted
	}
	req, err := http.NewRequest("GET", base+"/api/xbin/logs?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	if tok := ownerToken(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		var e struct{ Error string }
		if json.Unmarshal(b, &e) == nil && e.Error != "" {
			return fmt.Errorf("%s (%s)", e.Error, resp.Status)
		}
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	_, err = io.Copy(os.Stdout, resp.Body)
	return err
}

// tsItem is one deployment in /tile-status's summary.
type tsItem struct {
	Name       string          `json:"name"`
	State      string          `json:"state"`
	Gen        int             `json:"gen"`
	Checkpoint json.RawMessage `json:"checkpoint"`
}

// checkpoint is the item's checkpoint id, spelled as an id or a Checkpoint.
func (it tsItem) checkpoint() string {
	var id string
	if json.Unmarshal(it.Checkpoint, &id) == nil {
		return id
	}
	var c struct{ ID string }
	_ = json.Unmarshal(it.Checkpoint, &c)
	return c.ID
}

// logsOf streams deployment dep's log from GET /logs?deployment= (16-open-
// questions Q23), after GET /deployments has resolved the tile and found
// dep. A non-primary answer echoes dep in X-XBin-Deployment; one that
// doesn't came from an xbind that answered for the primary, and isn't shown.
func logsOf(comp, dep string, follow bool) error {
	if comp == "" {
		return usageError("logs", "which tile? name it, or run bx in the tile's terminal")
	}
	if err := checkName("logs", dep); err != nil {
		return err
	}
	st, err := resolveDeployment(comp, dep)
	if err != nil {
		return err
	}
	q := url.Values{"component": {st.Tile}, "deployment": {dep}}
	if follow {
		q.Set("follow", "1")
	} else {
		q.Set("tail", strconv.Itoa(1<<20))
	}
	base, client := transport()
	c := *client
	if follow {
		c.Timeout = 0 // a follow streams until it is interrupted
	}
	req, err := http.NewRequest("GET", base+"/api/xbin/logs?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	if tok := ownerToken(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := c.Do(req)
	if err != nil {
		return &dcError{code: exitFailed, msg: err.Error(), transport: true}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return httpRefusal(resp.StatusCode, b)
	}
	if !echoes(resp.Header.Get("X-XBin-Deployment"), dep, st.primary()) {
		return noEcho("/logs", st, dep)
	}
	if dep != st.primary() {
		fmt.Fprintf(dcErr, "%s+%s: backend log\n", st.Tile, dep)
	}
	_, err = io.Copy(os.Stdout, resp.Body)
	return err
}

// --- bx deployment ls, and the edge list ---

// lsState is the state as ls renders it (11-contract §1.1).
type lsState struct {
	Tile             string `json:"tile"`
	Record           bool   `json:"record"`
	View             string `json:"view"`
	Primary          string `json:"primary"`
	LiveReload       string `json:"liveReload"`
	ProtectedPrimary bool   `json:"protectedPrimary"`
	Deployments      []struct {
		Name       string `json:"name"`
		Primary    bool   `json:"primary"`
		LiveReload bool   `json:"liveReload"`
		Checkpoint *struct {
			ID string `json:"id"`
		} `json:"checkpoint"`
		Status struct {
			State     string        `json:"state"`
			Gen       int           `json:"gen"`
			Error     string        `json:"error"`
			Deploying *deployEntry  `json:"deploying"`
			Queued    []deployEntry `json:"queued"`
		} `json:"status"`
		Data          *dataState   `json:"data"`
		LastDeploy    *deployEntry `json:"lastDeploy"`
		Deliveries    *bool        `json:"deliveries"`
		Registrations []struct {
			Kind, Name, Schedule, Prefix string
			Dormant                      bool
		} `json:"registrations"`
		WouldNotify []struct{ At, To, Title string } `json:"wouldNotify"`
	} `json:"deployments"`
	Edges []struct {
		ID, Kind, To, Role, Policy, Default string
		Values                              []string
		Set                                 bool
		Effective, Why                      string
		Refused, Clamped                    int64
	} `json:"edges"`
	Caller *struct {
		Bound *string `json:"bound"`
	} `json:"caller"`
}

// depList prints a tile's deployments (11-contract §9.2): what each runs,
// its status and data, and the one this session targets; with no tile and
// no $XBIN_COMPONENT, every tile whose /components row has a summary.
func depList(cmd string, a dcArgs) error {
	if len(a.pos) > 1 {
		return usageError(cmd, "too many arguments: %s", strings.Join(a.pos, " "))
	}
	ref := os.Getenv("XBIN_COMPONENT")
	if len(a.pos) == 1 {
		ref = strings.Trim(a.pos[0], "/")
	}
	if ref != "" {
		_, raw, err := getDeployState(ref)
		if err != nil {
			return err
		}
		if a.json {
			printRaw(dcOut, raw)
			return nil
		}
		return printDeployments(raw)
	}
	var e *dcError // the family first: an xbind without it says so (exit 6)
	if _, err := dcCall("GET", "/api/xbin/deployments", nil); errors.As(err, &e) && e.code == exitNoDeployments {
		return err
	}
	b, err := dcCall("GET", "/api/xbin/components", nil)
	if err != nil {
		return err
	}
	var rows []struct {
		Path        string          `json:"path"`
		Deployments json.RawMessage `json:"deployments"`
	}
	if err := decodeAnswer(b, &rows); err != nil {
		return err
	}
	var all []json.RawMessage
	for _, r := range rows {
		if len(r.Deployments) == 0 || string(r.Deployments) == "null" {
			continue
		}
		_, raw, err := getDeployState(r.Path)
		if err != nil {
			return err
		}
		all = append(all, bytes.TrimSpace(raw))
	}
	if a.json {
		out, _ := json.Marshal(map[string]any{"tiles": all})
		printRaw(dcOut, out)
		return nil
	}
	if len(all) == 0 {
		fmt.Fprintln(dcOut, "no tile has deployments yet (bx live-reload pause or bx deployment add starts them)")
	}
	for i, raw := range all {
		if i > 0 {
			fmt.Fprintln(dcOut)
		}
		if err := printDeployments(raw); err != nil {
			return err
		}
	}
	return nil
}

func printDeployments(raw []byte) error {
	var s lsState
	if err := decodeAnswer(raw, &s); err != nil {
		return err
	}
	p := firstOf(s.Primary, "main")
	head := s.Tile + " · primary " + p
	if s.ProtectedPrimary {
		head += " (protected)"
	}
	switch {
	case !s.Record:
		head += " · live reload → " + p + " (no deployments)"
	case s.LiveReload != "":
		head += " · live reload → " + s.LiveReload
	case s.View != "reader":
		head += " · live reload paused"
	}
	fmt.Fprintln(dcOut, head)
	bound, hasBound := "", false
	if s.Caller != nil && s.Caller.Bound != nil {
		bound, hasBound = *s.Caller.Bound, true
	} else if os.Getenv("XBIN_COMPONENT") == s.Tile {
		bound, hasBound = os.Getenv("XBIN_DEPLOYMENT"), true
	}
	rows := [][]string{{"NAME", "ROLE", "CODE", "STATUS", "DATA", ""}}
	var notes []string
	for _, d := range s.Deployments {
		role, code := "-", "-"
		if d.Primary {
			role = "primary"
		}
		if d.Checkpoint != nil && d.Checkpoint.ID != "" {
			code = "pinned " + d.Checkpoint.ID
		} else if d.LiveReload {
			code = "follows work tree"
		}
		status := d.Status.State
		if d.Status.Gen > 0 {
			status += fmt.Sprintf(" g%d", d.Status.Gen)
		}
		if e := d.Status.Deploying; e != nil {
			status += " · deploying " + e.Checkpoint
		}
		if n := len(d.Status.Queued); n > 0 {
			status += fmt.Sprintf(" · %d queued", n)
		}
		if d.LastDeploy != nil && d.LastDeploy.Result == "failed" {
			status += " · last deploy failed"
		}
		mark := ""
		if hasBound && (bound == d.Name || bound == "" && d.Primary && !s.ProtectedPrimary) {
			mark = "← this terminal"
		}
		data := dataWords(d.Data, d.Name == "main")
		if s.View == "reader" {
			data = "-"
		}
		rows = append(rows, []string{d.Name, role, code, strings.TrimSpace(status), data, mark})
		if d.Status.Error != "" {
			notes = append(notes, d.Name+": "+d.Status.Error)
		}
		var regs []string
		for _, r := range d.Registrations {
			reg := r.Kind + " " + r.Name
			if r.Schedule != "" {
				reg += " (" + r.Schedule + ")"
			}
			switch {
			case !r.Dormant:
			case r.Kind == "cron" || r.Kind == "bus":
				reg += " dormant (deliveries off)"
			default:
				reg += " dormant (routes reach the primary only)"
			}
			regs = append(regs, reg)
		}
		if !d.Primary && d.Deliveries != nil && !*d.Deliveries {
			notes = append(notes, d.Name+": deliveries off — its cron jobs and bus subscriptions don't fire")
		}
		if len(regs) > 0 {
			notes = append(notes, d.Name+": "+strings.Join(regs, " · "))
		}
		for _, n := range d.WouldNotify {
			notes = append(notes, fmt.Sprintf("%s: would notify %s · %q%s", d.Name, who(n.To), n.Title, stamp("", n.At)))
		}
	}
	width := make([]int, len(rows[0]))
	for _, r := range rows {
		for i, c := range r {
			width[i] = max(width[i], len([]rune(c)))
		}
	}
	for _, r := range rows {
		var b strings.Builder
		for i, c := range r {
			b.WriteString(c + strings.Repeat(" ", width[i]-len([]rune(c))+2))
		}
		fmt.Fprintln(dcOut, "  "+strings.TrimRight(b.String(), " "))
	}
	for _, n := range notes {
		fmt.Fprintln(dcOut, "  "+n)
	}
	return nil
}

// listEdges prints a tile's outbound edges with their policy for its
// non-primary deployments, and the calls each refused or clamped.
func listEdges(a dcArgs, ref string) error {
	_, raw, err := getDeployState(ref)
	if err != nil {
		return err
	}
	if a.json {
		printRaw(dcOut, raw)
		return nil
	}
	var s lsState
	if err := decodeAnswer(raw, &s); err != nil {
		return err
	}
	fmt.Fprintf(dcOut, "%s: what non-primary deployments may use (the primary uses every edge as today)\n", s.Tile)
	if len(s.Edges) == 0 {
		fmt.Fprintln(dcOut, "  no edges")
	}
	for _, e := range s.Edges {
		to := e.To
		if e.Role != "" {
			to = strings.TrimSpace(to + " (" + e.Role + ")")
		}
		policy := e.Policy + map[bool]string{true: " (set)", false: " (default)"}[e.Set]
		if e.Effective != "" && e.Effective != e.Policy {
			policy = e.Effective + " — " + e.Why
		}
		fmt.Fprintf(dcOut, "  %s  %s  %s  %s · takes %s · refused %d · clamped %d\n", e.ID, e.Kind, firstOf(to, "-"), policy, strings.Join(e.Values, "|"), e.Refused, e.Clamped)
	}
	return nil
}

// stamp renders "by ana 2d ago" for a stamp's parts, or "".
func stamp(by, at string) string {
	s := ""
	if by != "" {
		s += " by " + who(by)
	}
	if t := ago(at); t != "" {
		s += " " + t
	}
	return s
}

// dataWords is a deployment's data in 10-ux §12.1's words.
func dataWords(d *dataState, primary bool) string {
	switch {
	case d == nil && primary:
		return "original"
	case d == nil:
		return "-"
	case d.Busy != "":
		return d.Busy + "…"
	case d.State == "empty" && d.Reset:
		return "started empty · reset" + stamp("", d.At)
	case d.State == "empty":
		return "started empty"
	case d.State == "seeded", d.State == "restored":
		s := d.State
		if d.From != "" {
			s += " from " + d.From
		}
		if t, err := time.Parse(time.RFC3339, d.At); err == nil {
			s += " " + t.Local().Format("2006-01-02")
		}
		return s
	}
	return firstOf(d.State, "-")
}
