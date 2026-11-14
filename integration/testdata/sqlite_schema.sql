-- Fixture for the SQLite introspection tests. The expected result is in
-- sqlite_schema.golden; each table below exercises the cases named above it.

-- Rowid alias, every column kind, defaults, a CHECK ... IN enum.
CREATE TABLE users (
  id            INTEGER PRIMARY KEY,
  email         TEXT NOT NULL UNIQUE,
  name          VARCHAR(100) NOT NULL DEFAULT '',
  role          TEXT NOT NULL DEFAULT 'member' CHECK (role IN ('admin', 'member', 'guest')),
  is_active     BOOLEAN NOT NULL DEFAULT 1,
  password_hash TEXT NOT NULL,
  balance       DECIMAL(10,2),
  rating        REAL,
  avatar        BLOB,
  settings      JSON,
  external_id   UUID,
  birthday      DATE,
  wake_time     TIME,
  created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at    TIMESTAMP
);

-- AUTOINCREMENT, a foreign key with an implied parent key and a parent name
-- in the wrong case, a self-reference, generated columns, a named
-- table-level CHECK enum.
CREATE TABLE posts (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  author_id   INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  reviewer_id INTEGER REFERENCES USERS ON DELETE SET NULL ON UPDATE CASCADE,
  parent_id   INTEGER REFERENCES posts(id) ON DELETE RESTRICT,
  title       TEXT NOT NULL,
  body        TEXT,
  status      VARCHAR(20) NOT NULL DEFAULT 'draft',
  word_count  INTEGER GENERATED ALWAYS AS (length(body)) VIRTUAL,
  slug        TEXT GENERATED ALWAYS AS (lower(replace(title, ' ', '-'))) STORED,
  CONSTRAINT posts_status CHECK (status IN ('draft', 'published', 'archived'))
);

-- WITHOUT ROWID with a text primary key.
CREATE TABLE tags (
  code  TEXT PRIMARY KEY,
  label TEXT NOT NULL
) WITHOUT ROWID;

-- Composite primary key whose order differs from the column order, and a
-- reference whose table and column names are in the wrong case.
CREATE TABLE post_tags (
  post_id  INTEGER NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
  tag_code TEXT NOT NULL REFERENCES Tags(CODE) ON DELETE RESTRICT ON UPDATE CASCADE,
  added_at DATETIME,
  PRIMARY KEY (tag_code, post_id)
);

-- Composite foreign key with explicit parent columns.
CREATE TABLE tag_notes (
  id       INTEGER PRIMARY KEY,
  post_id  INTEGER NOT NULL,
  tag_code TEXT NOT NULL,
  note     TEXT,
  FOREIGN KEY (post_id, tag_code) REFERENCES post_tags (post_id, tag_code) ON DELETE CASCADE
);

-- No primary key, and a composite foreign key with an implied parent key.
CREATE TABLE tag_votes (
  voter_id INTEGER NOT NULL REFERENCES users,
  tag_code TEXT NOT NULL,
  post_id  INTEGER NOT NULL,
  FOREIGN KEY (tag_code, post_id) REFERENCES post_tags
);

-- STRICT table. "INT PRIMARY KEY" is not a rowid alias, but STRICT makes it
-- NOT NULL.
CREATE TABLE measurements (
  id      INT PRIMARY KEY,
  sensor  TEXT NOT NULL,
  value   REAL NOT NULL DEFAULT 0.0,
  payload ANY,
  raw     BLOB
) STRICT;

-- A text primary key in a rowid table may hold NULL; columns without a type.
CREATE TABLE settings (
  key   TEXT PRIMARY KEY,
  value,
  kind  DEFAULT NULL
);

-- "INTEGER PRIMARY KEY DESC" is not a rowid alias. Foreign keys SQLite
-- accepts but can never enforce: only the one naming a column of a table
-- outside the schema can be represented.
CREATE TABLE imports (
  id         INTEGER PRIMARY KEY DESC,
  batch_id   INTEGER REFERENCES batches,
  source_id  INTEGER REFERENCES sources(id),
  user_email TEXT REFERENCES users(no_such_column)
);

-- Everything below must be ignored: indexes, triggers, views, virtual
-- tables and their shadow tables, and the sqlite_sequence table that
-- AUTOINCREMENT creates.
CREATE INDEX posts_author ON posts (author_id);

CREATE TRIGGER users_touch AFTER UPDATE ON users
BEGIN
  UPDATE users SET updated_at = CURRENT_TIMESTAMP WHERE id = NEW.id;
END;

CREATE VIEW active_users AS SELECT id, email FROM users WHERE is_active;

CREATE VIRTUAL TABLE post_search USING fts5(title, body);
