-- The wiki's tables as the build before knowledge-store owners made them
-- (commit 93256f9f, project_wiki.go: wikiSchema, then addWikiSchema's
-- ALTER) — the starting point of TestKnowStoreFromBaseSchema. Do not edit:
-- it stands for databases in the wild.
CREATE TABLE IF NOT EXISTS wiki_pages (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id  INTEGER NOT NULL,
  slug        TEXT    NOT NULL,
  title       TEXT    NOT NULL DEFAULT '',
  body        TEXT    NOT NULL DEFAULT '',
  version     INTEGER NOT NULL DEFAULT 0,
  sources     TEXT    NOT NULL DEFAULT '[]',
  archived    INTEGER NOT NULL DEFAULT 0,
  updated_by  TEXT    NOT NULL DEFAULT '',
  updated_run INTEGER NOT NULL DEFAULT 0,
  updated_ms  INTEGER NOT NULL DEFAULT 0,
  created_ms  INTEGER NOT NULL DEFAULT 0,
  reads       INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_wiki_pages_slug ON wiki_pages(project_id, slug);
-- every version of every page: body, who, run, task, note
CREATE TABLE IF NOT EXISTS wiki_revisions (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  page_id    INTEGER NOT NULL,
  project_id INTEGER NOT NULL,
  version    INTEGER NOT NULL,
  title      TEXT    NOT NULL DEFAULT '',
  body       TEXT    NOT NULL DEFAULT '',
  archived   INTEGER NOT NULL DEFAULT 0,
  by         TEXT    NOT NULL DEFAULT '',
  run_id     INTEGER NOT NULL DEFAULT 0,
  task_n     INTEGER NOT NULL DEFAULT 0,
  note       TEXT    NOT NULL DEFAULT '',
  at_ms      INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_wiki_rev ON wiki_revisions(page_id, version);
CREATE INDEX IF NOT EXISTS idx_wiki_rev_project ON wiki_revisions(project_id, id);
-- the version of a page a run last read (or wrote): its write's base
CREATE TABLE IF NOT EXISTS wiki_reads (
  run_id  INTEGER NOT NULL,
  page_id INTEGER NOT NULL,
  version INTEGER NOT NULL,
  at_ms   INTEGER NOT NULL,
  PRIMARY KEY (run_id, page_id)
);
CREATE INDEX IF NOT EXISTS idx_wiki_reads_page ON wiki_reads(page_id);
CREATE VIRTUAL TABLE IF NOT EXISTS wiki_fts USING fts5(
  title, body, project_id UNINDEXED, page_id UNINDEXED, tokenize='porter unicode61 remove_diacritics 2'
);
-- the index version a run's prompt shows (a task's brief: from its start)
CREATE TABLE IF NOT EXISTS wiki_snaps (
  run_id     INTEGER PRIMARY KEY,
  project_id INTEGER NOT NULL,
  version    INTEGER NOT NULL
);
-- what a sandbox's .xbin/wiki copy holds: the project's revision id at its sync
CREATE TABLE IF NOT EXISTS wiki_sync (
  ref        TEXT    NOT NULL,
  project_id INTEGER NOT NULL,
  seq        INTEGER NOT NULL,
  at_ms      INTEGER NOT NULL,
  PRIMARY KEY (ref, project_id)
);
-- a task's dream rounds (project_dream.go)
CREATE TABLE IF NOT EXISTS wiki_dreams (
  task_id    INTEGER PRIMARY KEY,
  project_id INTEGER NOT NULL,
  n          INTEGER NOT NULL,
  turns      INTEGER NOT NULL DEFAULT 0,
  dreams     INTEGER NOT NULL DEFAULT 0,
  pr_done    INTEGER NOT NULL DEFAULT 0,
  end_done   INTEGER NOT NULL DEFAULT 0,
  round      INTEGER NOT NULL DEFAULT 0,
  tokens     INTEGER NOT NULL DEFAULT 0,
  last_total INTEGER NOT NULL DEFAULT 0,
  active     INTEGER NOT NULL DEFAULT 0, -- its dream round was delivered: the turn taking it up is the dream's
  started_ms INTEGER NOT NULL DEFAULT 0, -- when (the page files it ingests are newer)
  last_dream INTEGER NOT NULL DEFAULT 0, -- the turn that ended last was the dream's (the judge skips it)
  judge_ms   INTEGER NOT NULL DEFAULT 0, -- the last work turn's verdict is due (the dream waits for it)
  hold_clean INTEGER NOT NULL DEFAULT 0, -- the end dream holds the task's cleanup back
  updated_ms INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_wiki_dreams_project ON wiki_dreams(project_id);
ALTER TABLE wiki_revisions ADD COLUMN at TEXT NOT NULL DEFAULT '';
