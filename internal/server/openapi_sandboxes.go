package server

// openapi_sandboxes.go — the tile-sandbox runtime's routes (D120,
// docs/protocol.md §Tile sandboxes): the sandboxes a manager tile — its
// backend, holding cap:sandboxes — defines and drives. endpoints() appends
// them.

const (
	capManager        = "manager tile (cap:sandboxes)"
	capManagerOrAdmin = "manager tile (cap:sandboxes), or admin (?tile=)"
	sbxTag            = "Tile sandboxes"
	sbxErrors         = " Errors are the sandbox-manager contract's {error, refusal, state?, etag?, retryAfterMs?} (docs/sandbox-manager.md). A path with a . or .. segment, or an encoded /, . or \\ in a segment, and a name or id failing its grammar are 400 invalid before anything is looked up. Without --isolate every manager route but runtime answers 501 unsupported; a route this xbind doesn't serve yet answers 501 unsupported (runtime's caps lists what it serves)."
)

func integer() oapi         { return oapi{"type": "integer"} }
func object(d string) oapi  { return oapi{"type": "object", "description": d} }
func sbxName() oapi         { return pathParam("name", "sandbox name, [a-z0-9][a-z0-9-]{0,31}") }
func sbxExec() []oapi       { return []oapi{sbxName(), pathParam("id", "exec id, [0-9a-f]{6}-[0-9]{1,12}")} }
func sbxSnap() oapi         { return pathParam("sid", "snapshot id, s-[0-9]{1,12}") }
func sbxPath(q string) oapi { return queryParam("path", q, true) }
func sbxWait() oapi {
	return queryParam("wait", "seconds to wait for the transition (≤ limits.waitMaxSec, more is clamped; absent = waitMaxSec; 0 = don't wait): the answer is the sandbox as it stands then, and the transition goes on", false)
}

// sandboxDef is the create body's schema; PATCH takes the same fields but
// name, mode and from, plus version.
func sandboxDef(create bool) oapi {
	props := oapi{
		"memMiB": integer(), "vcpus": integer(), "diskGiB": integer(),
		"net":         object(`{egress: "none" | "class:<sandbox-net slot>"}`),
		"mounts":      oapi{"type": "array", "items": object("{res, path?, at, ro?} | {source: true, at}")},
		"defaults":    object("{cwd, uid, gid, shell, env} — env keys XBIN_* are refused"),
		"labels":      object("opaque strings, ≤ 1 KiB in all"),
		"for":         str("a claim: the consumer tile (≤ 128)"),
		"forUser":     str("a claim: the person (≤ 128)"),
		"idleStopMin": integer(),
		"autoStart":   boolean(),
	}
	if !create {
		props["version"] = integer()
		return jsonBody("the fields to change", props)
	}
	props["name"] = str("[a-z0-9][a-z0-9-]{0,31}; runtime, policy and copy are reserved")
	props["mode"] = str("namespace | vm")
	props["clientId"] = str("repeat-safe create: the same request answers 200, another 409 exists")
	props["start"] = boolean()
	props["from"] = object("{sandbox, snapshot?}: a clone (snapshots)")
	return jsonBody("the definition", props, "name", "mode")
}

func sandboxEndpoints() []ep {
	name := []oapi{sbxName()}
	return []ep{
		// runtime and policy
		{"GET", "/sandboxes/runtime", sbxTag, "What this manager tile may use", capManager,
			"{enabled, isolation, modes:[{mode, accel?}], unavailable:[{mode, reason}], users (any | root: a single-uid namespace host), egress:[{class, slot?, ref?, reach, rules?, note?}] (none, then each sandbox-net slot of the tile), caps (the contract capabilities served), limits {sandboxes, running, memMiB, vcpus, diskGiB, perSandbox, idleStopMin, runTimeoutMaxMs, runOutputMax, execsRunning, outputRing, stdinMax, fileMax, tarMax, waitMaxSec, flows {tcp, udp}: a sandbox's concurrent connections}, used {sandboxes, running, memMiB, vcpus, diskBytes}}. Answers without --isolate too (isolation: false)." + sbxErrors,
			nil, nil, "Runtime"},
		{"GET", "/sandboxes/policy", sbxTag, "The sandboxes policy", "admin",
			"{policy (effective), stored (zero = default), error? (why the policy file can't be read: tile sandboxes are off — policy.enabled false, every start 503 — until a PUT writes a new file)}: {enabled, perTile {max, running, memMiB, vcpus, diskGiB}, perSandbox {memMiB, vcpus, diskGiB, maxMemMiB, maxVCPUs, maxDiskGiB, pids}, total {memMiB (0 = ¾ of the host's RAM), pids}: every tile sandbox together, no per-tile override, idleStopMin, outputRingMiB, outputBudgetMiB, overrides {<tile>: {perTile, perSandbox, …}}}.",
			nil, nil, "{policy, stored}"},
		{"PUT", "/sandboxes/policy", sbxTag, "Set the sandboxes policy", "admin",
			"A partial policy merged onto the stored one (absent fields stay; an override replaces that tile's, null removes it; onto the defaults when the file can't be read, which clears its error), then validated (400 invalid). enabled false stops every running tile sandbox (state kept).",
			nil, jsonBody("a partial policy", oapi{"enabled": boolean(), "perTile": object(""), "perSandbox": object(""), "total": object("{memMiB, pids}"), "idleStopMin": integer(), "outputRingMiB": integer(), "outputBudgetMiB": integer(), "overrides": object("by tile path")}), "{policy, stored}"},
		// definitions
		{"POST", "/sandboxes", sbxTag, "Define a tile sandbox", capManager,
			"201 SandboxInfo (200 when clientId repeats the same request). Sizes: 0 = the policy default, clamped to the caps (the answer says what applied). mode is required and must be available now; net names none or a sandbox-net slot of the tile; a res mount is a filesystem resource of the tile's own scope that it declares in uses and holds (a reader's is read-only at start); source mounts the tile's code read-only; at is absolute, not / and not under /proc, /sys, /dev, /run/xbin or /opt/xbin. 409 exists: the name, or a clientId used for another request; 429 limit: perTile.max, or VM disks over perTile.diskGiB. start: true starts it within ?wait." + sbxErrors,
			[]oapi{sbxWait()}, sandboxDef(true), "SandboxInfo"},
		{"GET", "/sandboxes/{name}", sbxTag, "One tile sandbox", capManager,
			"SandboxInfo {name, uid (its identity: a name deleted and created again gets another), state (creating | stopped | starting | running | stopping | error), stateDetail, mode, accel?, memMiB, vcpus, diskGiB, net {egress, reach, egressNext, note}, mounts, defaults, labels, for, forUser, idleStopMin, autoStart, base {version, outdated}, users, diskBytes, snapshots, execsRunning, created, started?, lastActive?, version, clientId?, restartNeeded}." + sbxErrors,
			name, nil, "SandboxInfo"},
		{"PATCH", "/sandboxes/{name}", sbxTag, "Change a tile sandbox", capManager,
			"The fields present change (defaults and labels as a whole); name, mode and from can't (400). version guards a lost update (412 precondition). A VM disk only grows. The answer has restartNeeded when a change waits for the next start (egressNext for an egress)." + sbxErrors,
			name, sandboxDef(false), "SandboxInfo"},
		{"DELETE", "/sandboxes/{name}", sbxTag, "Delete a tile sandbox", capManagerOrAdmin,
			"Stops it, puts its state aside for a confined removal (which goes on after the answer) and forgets it: 204. An admin names the tile with ?tile=." + sbxErrors,
			[]oapi{sbxName(), queryParam("tile", "admin: the tile whose sandbox it is", false)}, nil, "204"},
		// lifecycle
		{"POST", "/sandboxes/{name}/start", sbxTag, "Start a tile sandbox", capManager,
			"SandboxInfo once running, or when ?wait runs out. A failed start leaves it stopped with the failure in stateDetail. Admission: 429 limit over perTile.running, perTile.memMiB, perTile.vcpus, the workspace's total.memMiB (memMiB + 128 per sandbox) or perTile.diskGiB (the tile's sandbox bytes), naming the cap. 409 state while it is in error (its state or base image is gone, or another overlay flavour wrote its upper: reset it; a rebase repairs a missing base) or an earlier run still holds its state after 5 s; 503 while the sandboxes policy is off or its file can't be read, the workspace disk is low, or the vault is sealed; 400 when a mount or its egress class is no longer the tile's. A running sandbox with no activity for its idleStopMin (a non-tty exec, a run, a file operation or an attached terminal holds it) is stopped, state kept." + sbxErrors,
			[]oapi{sbxName(), sbxWait()}, nil, "SandboxInfo"},
		{"POST", "/sandboxes/{name}/stop", sbxTag, "Stop a tile sandbox", capManagerOrAdmin,
			"Sync, then kill; the state is kept and running execs end killed. An admin names the tile with ?tile= (stateDetail: stopped by a workspace admin). However a sandbox ends — a stop, its agent exiting, its root filesystem dying, the OOM killer, a narrowed sandbox-net class — it is stopped with why in stateDetail." + sbxErrors,
			[]oapi{sbxName(), sbxWait(), queryParam("tile", "admin: the tile whose sandbox it is", false)}, nil, "SandboxInfo"},
		{"POST", "/sandboxes/{name}/reset", sbxTag, "Reset a tile sandbox", capManager,
			"Stops it if it runs (its execs end killed), puts its state aside for a confined removal and clears its base pin — the next start runs the current base image from an empty upper — and starts it again if it ran: SandboxInfo. Snapshots are kept; repairs error; idempotent." + sbxErrors, []oapi{sbxName(), sbxWait()}, nil, "SandboxInfo"},
		{"POST", "/sandboxes/{name}/rebase", sbxTag, "Rebase a tile sandbox", capManager,
			"Stops it if it runs, keeps its state and pins it to the current base image (may break package-manager state), and starts it again if it ran: SandboxInfo. Snapshots are kept; repairs a missing base (409 state when its state is missing: reset it)." + sbxErrors, []oapi{sbxName(), sbxWait()}, nil, "SandboxInfo"},
		// commands
		{"POST", "/sandboxes/{name}/run", sbxTag, "Run a command and wait", capManager,
			"The contract's run: body {cmd | argv, cwd, env, stdin, timeoutMs, maxOutput, merge, uid?, gid?, forUser?} → {exitCode, signal, timedOut, ms, stdout:{head, tail, elided, bytes}, stderr} (one output with merge). Not audited (data plane)." + sbxErrors,
			name, jsonBody("the command", oapi{"cmd": str(""), "argv": arr(), "cwd": str(""), "env": object(""), "stdin": str(""), "timeoutMs": integer(), "maxOutput": integer(), "merge": boolean(), "uid": integer(), "gid": integer(), "forUser": str("")}), "the run's result"},
		{"GET", "/sandboxes/{name}/execs", sbxTag, "Background execs", capManager,
			"{execs:[Exec]}; an Exec is {id, label, cmd, argv, cwd, tty, state (running | exited | killed | lost), exitCode, signal, started, ended, total, clientId, forUser, uid}." + sbxErrors, name, nil, "{execs}"},
		{"POST", "/sandboxes/{name}/execs", sbxTag, "Start a background exec", capManager,
			"The contract's exec: body {cmd | argv, cwd?, env?, tty?, rows?, cols?, stdin?, timeoutMs?, label?, clientId?, uid?, gid?, forUser?} → 201 Exec (200 on a clientId repeat). A tty exec whose forUser has noTerminal (D88) is 403. Not audited (data plane)." + sbxErrors,
			name, jsonBody("the exec", oapi{"cmd": str(""), "argv": arr(), "cwd": str(""), "env": object(""), "tty": boolean(), "rows": integer(), "cols": integer(), "stdin": boolean(), "timeoutMs": integer(), "label": str(""), "clientId": str(""), "uid": integer(), "gid": integer(), "forUser": str("")}), "Exec"},
		{"GET", "/sandboxes/{name}/execs/{id}", sbxTag, "One exec", capManager,
			"Exec. An id from before xbind restarted, or of an exec its sandbox's stop ended, is 410 lost." + sbxErrors, sbxExec(), nil, "Exec"},
		{"DELETE", "/sandboxes/{name}/execs/{id}", sbxTag, "Kill and forget an exec", capManager,
			"Kills its process group: 204." + sbxErrors, sbxExec(), nil, "204"},
		{"GET", "/sandboxes/{name}/execs/{id}/output", sbxTag, "Read an exec's output", capManager,
			"By byte offset with a long-poll: {start, end, total, ringStart, data, encoding, state, exitCode, signal}." + sbxErrors,
			append(sbxExec(), queryParam("since", "offset", false), queryParam("max", "bytes (≤ 1 MiB)", false), queryParam("waitMs", "≤ 30000", false), queryParam("encoding", "text | base64", false)), nil, "the chunk"},
		{"POST", "/sandboxes/{name}/execs/{id}/stdin", sbxTag, "Write an exec's stdin", capManager,
			"The raw body (≤ limits.stdinMax); ?eof=1 closes stdin. 204. Not audited (data plane)." + sbxErrors,
			append(sbxExec(), queryParam("eof", "1 closes stdin", false)), freeBody("bytes"), "204"},
		{"POST", "/sandboxes/{name}/execs/{id}/signal", sbxTag, "Signal an exec", capManager,
			"{signal: INT | TERM | KILL | HUP, group?} → 204. Not audited (data plane)." + sbxErrors,
			sbxExec(), jsonBody("the signal", oapi{"signal": str("INT | TERM | KILL | HUP"), "group": boolean()}, "signal"), "204"},
		{"POST", "/sandboxes/{name}/execs/{id}/resize", sbxTag, "Resize a tty exec", capManager,
			"{rows, cols} → 204. Not audited (data plane)." + sbxErrors,
			sbxExec(), jsonBody("the size", oapi{"rows": integer(), "cols": integer()}, "rows", "cols"), "204"},
		{"GET", "/sandboxes/{name}/execs/{id}/tty", sbxTag, "Attach to a tty exec (WebSocket)", capManager,
			"A WebSocket on the /ws/term wire: binary frames both ways, the ring's tail replayed first; {op:session, id, sandbox, echoAck} first (sessionId and sandboxId replace id and sandbox), acks and pongs, {op:exit, code} at the end. Only the manager's instance token reaches it; the manager relays it to its consumers. A forUser with noTerminal (D88) is 403." + sbxErrors,
			append(sbxExec(), queryParam("sessionId", "the session frame's id ([A-Za-z0-9._-]{1,64})", false), queryParam("sandboxId", "the session frame's sandbox", false), queryParam("forUser", "the person (a claim)", false)), nil, "101 Switching Protocols"},
		{"GET", "/sandboxes/{name}/tty", sbxTag, "Start a tty exec and attach (WebSocket)", capManager,
			"Starts the login shell (or cmd) on a PTY and attaches, as the attach route." + sbxErrors,
			[]oapi{sbxName(), queryParam("cwd", "", false), queryParam("cmd", "", false), queryParam("rows", "", false), queryParam("cols", "", false), queryParam("uid", "", false), queryParam("gid", "", false), queryParam("forUser", "", false), queryParam("sessionId", "", false), queryParam("sandboxId", "", false)}, nil, "101 Switching Protocols"},
		// files and trees
		{"GET", "/sandboxes/{name}/files/stat", sbxTag, "Stat a path in a sandbox", capManager,
			"{path, type, size, mode, mtimeMs, etag, target?}. Paths are absolute inside the sandbox and resolved there." + sbxErrors,
			[]oapi{sbxName(), sbxPath("absolute, inside the sandbox")}, nil, "the stat"},
		{"GET", "/sandboxes/{name}/files/content", sbxTag, "Read a file", capManager,
			"The bytes, with an ETag." + sbxErrors,
			[]oapi{sbxName(), sbxPath("absolute, inside the sandbox"), queryParam("offset", "", false), queryParam("length", "", false)}, nil, "the bytes"},
		{"PUT", "/sandboxes/{name}/files/content", sbxTag, "Write a file", capManager,
			"Replaced atomically; the raw body (≤ limits.fileMax, else 413). ifMatch → 412 precondition with the current etag; ifNoneMatch=* creates only. → the stat. Not audited (data plane)." + sbxErrors,
			[]oapi{sbxName(), sbxPath("absolute, inside the sandbox"), queryParam("mode", "octal, e.g. 0644", false), queryParam("mkdirs", "1", false), queryParam("ifMatch", "etag", false), queryParam("ifNoneMatch", "*", false)}, freeBody("the content"), "the stat"},
		{"GET", "/sandboxes/{name}/files/list", sbxTag, "List a directory", capManager,
			"{path, entries:[{name, type, size, mtimeMs, mode, target?}], truncated}." + sbxErrors,
			[]oapi{sbxName(), sbxPath("absolute, inside the sandbox"), queryParam("limit", "default 1000", false)}, nil, "the listing"},
		{"POST", "/sandboxes/{name}/files/mkdir", sbxTag, "Make a directory", capManager,
			"{path, parents} → 204. Not audited (data plane)." + sbxErrors,
			name, jsonBody("the directory", oapi{"path": str(""), "parents": boolean()}, "path"), "204"},
		{"POST", "/sandboxes/{name}/files/remove", sbxTag, "Remove a path", capManager,
			"{path, recursive} → 204. Not audited (data plane)." + sbxErrors,
			name, jsonBody("the path", oapi{"path": str(""), "recursive": boolean()}, "path"), "204"},
		{"POST", "/sandboxes/{name}/files/move", sbxTag, "Move a path", capManager,
			"{from, to, overwrite} → 204; onto an existing path without overwrite is 412 precondition. Not audited (data plane)." + sbxErrors,
			name, jsonBody("the move", oapi{"from": str(""), "to": str(""), "overwrite": boolean()}, "from", "to"), "204"},
		{"GET", "/sandboxes/{name}/tar", sbxTag, "A tree as a tar", capManager,
			"application/x-tar, names relative to path; symlinks stored as links." + sbxErrors,
			[]oapi{sbxName(), sbxPath("a directory, inside the sandbox"), queryParam("exclude", "a glob relative to path (repeatable)", false)}, nil, "application/x-tar"},
		{"PUT", "/sandboxes/{name}/tar", sbxTag, "Extract a tar", capManager,
			"The body (≤ limits.tarMax) extracted under path, never outside it: 204. Not audited (data plane)." + sbxErrors,
			[]oapi{sbxName(), sbxPath("a directory, inside the sandbox"), queryParam("mkdirs", "1", false)}, freeBody("a tar stream"), "204"},
		{"POST", "/sandboxes/copy", sbxTag, "Copy between two sandboxes", capManager,
			"{from:{sandbox, path}, to:{sandbox, path}, overwrite} → 204: both of the caller's own. Not audited (data plane)." + sbxErrors,
			nil, jsonBody("the copy", oapi{"from": object("{sandbox, path}"), "to": object("{sandbox, path}"), "overwrite": boolean()}, "from", "to"), "204"},
		// snapshots
		{"GET", "/sandboxes/{name}/snapshots", sbxTag, "A sandbox's snapshots", capManager,
			"{snapshots:[{id, name, created, bytes}]}." + sbxErrors, name, nil, "{snapshots}"},
		{"POST", "/sandboxes/{name}/snapshots", sbxTag, "Take a snapshot", capManager,
			"{name, clientId} → 201 (stops the sandbox briefly; restarts it if it ran)." + sbxErrors,
			name, jsonBody("the snapshot", oapi{"name": str(""), "clientId": str("")}), "the snapshot"},
		{"POST", "/sandboxes/{name}/snapshots/{sid}/restore", sbxTag, "Restore a snapshot", capManager,
			"SandboxInfo; its execs are killed. A snapshot of another base or mode is 400 invalid." + sbxErrors,
			[]oapi{sbxName(), sbxSnap()}, nil, "SandboxInfo"},
		{"DELETE", "/sandboxes/{name}/snapshots/{sid}", sbxTag, "Delete a snapshot", capManager,
			"204." + sbxErrors, []oapi{sbxName(), sbxSnap()}, nil, "204"},
	}
}
