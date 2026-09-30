package main

// The schema as the agent before coding agents (v0.3.64 and the fixes on it)
// creates it: sqlite_master of a fresh openDB, FTS shadow tables left out
// (the virtual tables make them). Frozen — the migration test seeds an
// existing instance's database from it (harness_store_test.go).
const agentSchemaBeforeHarness = `
CREATE TABLE runs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  title TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'idle',
  wake_at INTEGER NOT NULL DEFAULT 0,
  parent_id INTEGER NOT NULL DEFAULT 0,
  config TEXT NOT NULL DEFAULT '',
  summary TEXT NOT NULL DEFAULT '',
  result TEXT NOT NULL DEFAULT '',
  pending TEXT NOT NULL DEFAULT '',
  last_prompt_tokens INTEGER NOT NULL DEFAULT 0,
  created INTEGER NOT NULL,
  updated INTEGER NOT NULL
, kind TEXT NOT NULL DEFAULT '', root_id INTEGER NOT NULL DEFAULT 0, depth INTEGER NOT NULL DEFAULT 0, detached INTEGER NOT NULL DEFAULT 0, outcome TEXT NOT NULL DEFAULT '', settled_at INTEGER NOT NULL DEFAULT 0, cancel_req INTEGER NOT NULL DEFAULT 0, lease_owner TEXT NOT NULL DEFAULT '', lease_until INTEGER NOT NULL DEFAULT 0, llm_calls INTEGER NOT NULL DEFAULT 0, prompt_tokens INTEGER NOT NULL DEFAULT 0, completion_tokens INTEGER NOT NULL DEFAULT 0, turn_steps INTEGER NOT NULL DEFAULT 0, turn_started INTEGER NOT NULL DEFAULT 0, compact_note INTEGER NOT NULL DEFAULT 0, owner TEXT NOT NULL DEFAULT '', visibility TEXT NOT NULL DEFAULT 'team', team_role TEXT NOT NULL DEFAULT 'participant', origin TEXT NOT NULL DEFAULT '', origin_id INTEGER NOT NULL DEFAULT 0, session_key TEXT NOT NULL DEFAULT '', title_src TEXT NOT NULL DEFAULT '', activity_ms INTEGER NOT NULL DEFAULT 0);
CREATE TABLE messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id INTEGER NOT NULL,
  seq INTEGER NOT NULL,
  role TEXT NOT NULL,
  content TEXT NOT NULL DEFAULT '',
  name TEXT NOT NULL DEFAULT '',
  tool_call_id TEXT NOT NULL DEFAULT '',
  tool_calls TEXT NOT NULL DEFAULT '',
  tokens INTEGER NOT NULL DEFAULT 0,
  compacted INTEGER NOT NULL DEFAULT 0,
  created INTEGER NOT NULL
, meta TEXT NOT NULL DEFAULT '', masked INTEGER NOT NULL DEFAULT 0);
CREATE INDEX idx_messages_run ON messages(run_id, seq);
CREATE VIRTUAL TABLE messages_fts USING fts5(
  content, run_id UNINDEXED, msg_id UNINDEXED, tokenize='porter'
);
CREATE TABLE steps (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id INTEGER NOT NULL,
  seq INTEGER NOT NULL,
  kind TEXT NOT NULL,
  detail TEXT NOT NULL DEFAULT '',
  created INTEGER NOT NULL
);
CREATE INDEX idx_steps_run ON steps(run_id, seq);
CREATE TABLE memory (
  run_id INTEGER NOT NULL,
  key TEXT NOT NULL,
  value TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (run_id, key)
);
CREATE TABLE settings (
  k TEXT PRIMARY KEY,
  v TEXT NOT NULL DEFAULT ''
);
CREATE TABLE skills (
  name TEXT PRIMARY KEY,
  description TEXT NOT NULL DEFAULT '',
  content TEXT NOT NULL DEFAULT '',
  created INTEGER NOT NULL,
  updated INTEGER NOT NULL
, owner TEXT NOT NULL DEFAULT '', lane TEXT NOT NULL DEFAULT '');
CREATE TABLE schedules (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL DEFAULT '',
  cron TEXT NOT NULL DEFAULT '',
  goal TEXT NOT NULL DEFAULT '',
  system TEXT NOT NULL DEFAULT '',
  watcher INTEGER NOT NULL DEFAULT 0,
  enabled INTEGER NOT NULL DEFAULT 1,
  run_id INTEGER NOT NULL DEFAULT 0,
  last_run INTEGER NOT NULL DEFAULT 0,
  created INTEGER NOT NULL
, toolset TEXT NOT NULL DEFAULT '', class TEXT NOT NULL DEFAULT '', owner TEXT NOT NULL DEFAULT '', visibility TEXT NOT NULL DEFAULT 'team', mode TEXT NOT NULL DEFAULT '', target_run INTEGER NOT NULL DEFAULT 0, created_by_run INTEGER NOT NULL DEFAULT 0, last_run_id INTEGER NOT NULL DEFAULT 0, last_status TEXT NOT NULL DEFAULT '');
CREATE TABLE repl_files (
  run_id INTEGER NOT NULL,
  path TEXT NOT NULL,
  content TEXT NOT NULL DEFAULT '',
  bytes INTEGER NOT NULL DEFAULT 0,
  version INTEGER NOT NULL DEFAULT 1,
  created INTEGER NOT NULL,
  updated INTEGER NOT NULL, mime TEXT NOT NULL DEFAULT '', blob TEXT NOT NULL DEFAULT '', sha256 TEXT NOT NULL DEFAULT '', source TEXT NOT NULL DEFAULT '', parent INTEGER NOT NULL DEFAULT 0, meta_ver INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (run_id, path)
);
CREATE TABLE repl_log (
  run_id INTEGER NOT NULL,
  seq INTEGER NOT NULL,
  kind TEXT NOT NULL DEFAULT 'eval',
  code TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL DEFAULT 'running',
  ms INTEGER NOT NULL DEFAULT 0,
  created INTEGER NOT NULL,
  PRIMARY KEY (run_id, seq)
);
CREATE INDEX idx_repl_log_run ON repl_log(run_id, seq);
CREATE TABLE run_deps (
  run_id       INTEGER NOT NULL,
  dep_id       INTEGER NOT NULL,
  kind         TEXT NOT NULL DEFAULT 'await',
  state        TEXT NOT NULL DEFAULT 'pending',
  delivered    INTEGER NOT NULL DEFAULT 0,
  tool_call_id TEXT NOT NULL DEFAULT '',
  on_error     TEXT NOT NULL DEFAULT 'report',
  created      INTEGER NOT NULL,
  settled      INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (run_id, dep_id)
);
CREATE INDEX idx_deps_dep   ON run_deps(dep_id);
CREATE INDEX idx_deps_undel ON run_deps(run_id, delivered, state);
CREATE TABLE run_trees (
  root_id   INTEGER PRIMARY KEY,
  spawned   INTEGER NOT NULL DEFAULT 0,
  max_spawn INTEGER NOT NULL DEFAULT 0,
  max_depth INTEGER NOT NULL DEFAULT 0,
  created   INTEGER NOT NULL
);
CREATE TABLE message_files (
  msg_id INTEGER NOT NULL,
  run_id INTEGER NOT NULL,
  path   TEXT NOT NULL,
  PRIMARY KEY (msg_id, path)
);
CREATE INDEX idx_message_files_run ON message_files(run_id);
CREATE TABLE inbox (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id INTEGER NOT NULL,
  kind TEXT NOT NULL,
  body TEXT NOT NULL DEFAULT '{}',
  client_id TEXT NOT NULL DEFAULT '',
  created INTEGER NOT NULL,
  delivered_at INTEGER NOT NULL DEFAULT 0,
  msg_id INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_inbox_run ON inbox(run_id, delivered_at, id);
CREATE UNIQUE INDEX idx_inbox_client ON inbox(run_id, client_id) WHERE client_id<>'';
CREATE TABLE links (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  parent_id INTEGER NOT NULL,
  child_id INTEGER NOT NULL,
  tool_call_id TEXT NOT NULL DEFAULT '',
  mode TEXT NOT NULL DEFAULT 'fg',
  state TEXT NOT NULL DEFAULT 'running',
  outcome TEXT NOT NULL DEFAULT '',
  result TEXT NOT NULL DEFAULT '',
  deadline INTEGER NOT NULL DEFAULT 0,
  delivered INTEGER NOT NULL DEFAULT 0,
  demoted_at INTEGER NOT NULL DEFAULT 0,
  label TEXT NOT NULL DEFAULT '',
  created INTEGER NOT NULL,
  settled INTEGER NOT NULL DEFAULT 0,
  legacy INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_links_parent ON links(parent_id, delivered, state);
CREATE INDEX idx_links_child ON links(child_id, id);
CREATE UNIQUE INDEX idx_links_open ON links(child_id) WHERE state='running';
CREATE TABLE link_deps (
  child_id INTEGER NOT NULL,
  dep_link INTEGER NOT NULL,
  dep_run INTEGER NOT NULL,
  PRIMARY KEY (child_id, dep_link)
);
CREATE INDEX idx_link_deps_dep ON link_deps(dep_link);
CREATE TABLE run_grants (
  root_id INTEGER NOT NULL,
  cap TEXT NOT NULL,
  granted_by TEXT NOT NULL DEFAULT '',
  expires_ms INTEGER NOT NULL,
  created_ms INTEGER NOT NULL,
  PRIMARY KEY (root_id, cap)
);
CREATE TABLE asks (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id INTEGER NOT NULL,
  msg_id INTEGER NOT NULL DEFAULT 0,
  seq INTEGER NOT NULL DEFAULT 0,
  source TEXT NOT NULL DEFAULT '',
  who TEXT NOT NULL DEFAULT '',
  text TEXT NOT NULL DEFAULT '',
  at INTEGER NOT NULL
);
CREATE INDEX idx_asks_run ON asks(run_id, id);
CREATE UNIQUE INDEX idx_asks_msg ON asks(msg_id) WHERE msg_id<>0;
CREATE TABLE summaries (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id INTEGER NOT NULL,
  text TEXT NOT NULL DEFAULT '',
  upto_seq INTEGER NOT NULL DEFAULT 0,
  messages INTEGER NOT NULL DEFAULT 0,
  at INTEGER NOT NULL
);
CREATE INDEX idx_summaries_run ON summaries(run_id, id);
CREATE VIRTUAL TABLE summaries_fts USING fts5(
  text, run_id UNINDEXED, sum_id UNINDEXED, tokenize='porter'
);
CREATE TABLE run_members (
  run_id INTEGER NOT NULL, user TEXT NOT NULL,
  role TEXT NOT NULL DEFAULT 'participant',
  added_by TEXT NOT NULL DEFAULT '', via TEXT NOT NULL DEFAULT '',
  created INTEGER NOT NULL, PRIMARY KEY (run_id, user));
CREATE TABLE share_links (
  id INTEGER PRIMARY KEY AUTOINCREMENT, run_id INTEGER NOT NULL,
  token_hash TEXT NOT NULL UNIQUE, role TEXT NOT NULL DEFAULT 'participant',
  created_by TEXT NOT NULL, created INTEGER NOT NULL,
  expires INTEGER NOT NULL DEFAULT 0, max_uses INTEGER NOT NULL DEFAULT 0,
  uses INTEGER NOT NULL DEFAULT 0, revoked INTEGER NOT NULL DEFAULT 0);
CREATE TABLE run_user_state (
  run_id INTEGER NOT NULL, user TEXT NOT NULL,
  pinned_at INTEGER NOT NULL DEFAULT 0, archived_at INTEGER NOT NULL DEFAULT 0,
  read_ms INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (run_id, user));
CREATE TABLE sessions (
  key TEXT PRIMARY KEY, origin TEXT NOT NULL, origin_id INTEGER NOT NULL DEFAULT 0,
  run_id INTEGER NOT NULL DEFAULT 0,
  owner TEXT NOT NULL DEFAULT '', visibility TEXT NOT NULL DEFAULT 'private',
  reset_policy TEXT NOT NULL DEFAULT '', resets INTEGER NOT NULL DEFAULT 0,
  created INTEGER NOT NULL, last_in INTEGER NOT NULL DEFAULT 0,
  address TEXT NOT NULL DEFAULT '');
CREATE INDEX idx_runs_conv ON runs(parent_id, activity_ms, id);
CREATE INDEX idx_runs_owner ON runs(owner, parent_id);
CREATE INDEX idx_runs_origin ON runs(origin, origin_id, id);
CREATE INDEX idx_runs_session ON runs(session_key) WHERE session_key<>'';
CREATE INDEX idx_members_user ON run_members(user);
CREATE INDEX idx_rus_user ON run_user_state(user, pinned_at);
CREATE INDEX idx_sessions_origin ON sessions(origin, origin_id);
CREATE TABLE channels (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  adapter TEXT NOT NULL, platform TEXT NOT NULL DEFAULT '',
  account_id TEXT NOT NULL DEFAULT '', account_name TEXT NOT NULL DEFAULT '',
  bot_id TEXT NOT NULL DEFAULT '', bot_name TEXT NOT NULL DEFAULT '',
  features TEXT NOT NULL DEFAULT '[]',
  state TEXT NOT NULL DEFAULT 'unclaimed',
  owner TEXT NOT NULL DEFAULT '', visibility TEXT NOT NULL DEFAULT 'private',
  name TEXT NOT NULL DEFAULT '', policy TEXT NOT NULL DEFAULT '{}',
  created INTEGER NOT NULL, claimed_at INTEGER NOT NULL DEFAULT 0,
  last_seen INTEGER NOT NULL DEFAULT 0,
  UNIQUE(adapter, account_id));
CREATE TABLE channel_events (
  channel_id INTEGER NOT NULL, event_id TEXT NOT NULL, created INTEGER NOT NULL,
  PRIMARY KEY (channel_id, event_id));
CREATE INDEX idx_chev_created ON channel_events(created);
CREATE TABLE channel_peers (
  channel_id INTEGER NOT NULL, peer_id TEXT NOT NULL, name TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL DEFAULT 'pending',
  trusted INTEGER NOT NULL DEFAULT 0,
  code TEXT NOT NULL DEFAULT '', code_expires INTEGER NOT NULL DEFAULT 0,
  code_sent INTEGER NOT NULL DEFAULT 0,
  created INTEGER NOT NULL, approved_by TEXT NOT NULL DEFAULT '',
  xbin_user TEXT NOT NULL DEFAULT '', linked_at INTEGER NOT NULL DEFAULT 0,
  dm_addr TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (channel_id, peer_id));
CREATE TABLE outbox (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  channel_id INTEGER NOT NULL, session_key TEXT NOT NULL DEFAULT '',
  run_id INTEGER NOT NULL DEFAULT 0, kind TEXT NOT NULL,
  address TEXT NOT NULL DEFAULT '{}', body TEXT NOT NULL DEFAULT '{}',
  created INTEGER NOT NULL, state TEXT NOT NULL DEFAULT 'pending',
  acked_at INTEGER NOT NULL DEFAULT 0, error TEXT NOT NULL DEFAULT '',
  ref TEXT NOT NULL DEFAULT '');
CREATE INDEX idx_outbox_pending ON outbox(channel_id, state, id);
CREATE INDEX idx_outbox_created ON outbox(state, created);
CREATE TABLE channel_files (
  id TEXT PRIMARY KEY, channel_id INTEGER NOT NULL,
  name TEXT NOT NULL, mime TEXT NOT NULL DEFAULT '', size INTEGER NOT NULL DEFAULT 0,
  content TEXT NOT NULL DEFAULT '', blob TEXT NOT NULL DEFAULT '',
  created INTEGER NOT NULL);
CREATE INDEX idx_chfiles_created ON channel_files(created);
CREATE TABLE reply_files (
  run_id INTEGER NOT NULL, path TEXT NOT NULL, created INTEGER NOT NULL,
  PRIMARY KEY (run_id, path));
CREATE TABLE triggers (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL UNIQUE,
  source TEXT NOT NULL, source_ref TEXT NOT NULL, match TEXT NOT NULL DEFAULT '',
  goal TEXT NOT NULL, system TEXT NOT NULL DEFAULT '',
  mode TEXT NOT NULL DEFAULT 'isolated', target_run INTEGER NOT NULL DEFAULT 0,
  toolset TEXT NOT NULL DEFAULT 'private', data_class TEXT NOT NULL DEFAULT 'private',
  deliver TEXT NOT NULL DEFAULT '',
  owner TEXT NOT NULL DEFAULT '', visibility TEXT NOT NULL DEFAULT 'private',
  enabled INTEGER NOT NULL DEFAULT 1, status TEXT NOT NULL DEFAULT '',
  max_per_hour INTEGER NOT NULL DEFAULT 30,
  created INTEGER NOT NULL, last_event INTEGER NOT NULL DEFAULT 0, last_run_id INTEGER NOT NULL DEFAULT 0, class TEXT NOT NULL DEFAULT '');
CREATE TABLE trigger_events (
  trigger_id INTEGER NOT NULL, event_id TEXT NOT NULL, source TEXT NOT NULL DEFAULT '',
  topic TEXT NOT NULL DEFAULT '', accepted INTEGER NOT NULL DEFAULT 0, reason TEXT NOT NULL DEFAULT '',
  run_id INTEGER NOT NULL DEFAULT 0, created INTEGER NOT NULL,
  PRIMARY KEY (trigger_id, event_id));
CREATE INDEX idx_trigev_time ON trigger_events(trigger_id, created);
CREATE TABLE sandbox_jobs (
	root_id      INTEGER NOT NULL,
	job          INTEGER NOT NULL,
	run_id       INTEGER NOT NULL,
	tool_call_id TEXT NOT NULL DEFAULT '',
	ref          TEXT NOT NULL,
	exec_id      TEXT NOT NULL DEFAULT '',
	command      TEXT NOT NULL,
	cwd          TEXT NOT NULL DEFAULT '',
	state        TEXT NOT NULL DEFAULT 'starting',
	exit_code    INTEGER,
	read_off     INTEGER NOT NULL DEFAULT 0,
	fg           INTEGER NOT NULL DEFAULT 1,
	created_ms   INTEGER NOT NULL,
	ended_ms     INTEGER NOT NULL DEFAULT 0, client_id TEXT NOT NULL DEFAULT '', signal TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (root_id, job)
);
CREATE INDEX idx_sandbox_jobs_call ON sandbox_jobs(run_id, tool_call_id);
CREATE TABLE sandbox_creates (
	root_id    INTEGER NOT NULL,
	n          INTEGER NOT NULL,
	name       TEXT NOT NULL,
	call_id    TEXT NOT NULL DEFAULT '',
	ref        TEXT NOT NULL DEFAULT '',
	state      TEXT NOT NULL DEFAULT 'pending',
	created_ms INTEGER NOT NULL,
	PRIMARY KEY (root_id, n)
);
CREATE TABLE repl_file_versions (
  run_id INTEGER NOT NULL,
  path TEXT NOT NULL,
  version INTEGER NOT NULL,
  content TEXT NOT NULL DEFAULT '',
  bytes INTEGER NOT NULL DEFAULT 0,
  mime TEXT NOT NULL DEFAULT '',
  blob TEXT NOT NULL DEFAULT '',
  sha256 TEXT NOT NULL DEFAULT '',
  source TEXT NOT NULL DEFAULT '',
  parent INTEGER NOT NULL DEFAULT 0,
  updated INTEGER NOT NULL,
  PRIMARY KEY (run_id, path, version)
);
CREATE INDEX idx_runs_status ON runs(status, wake_at);
CREATE INDEX idx_runs_parent ON runs(parent_id);
CREATE INDEX idx_runs_root ON runs(root_id);
`
