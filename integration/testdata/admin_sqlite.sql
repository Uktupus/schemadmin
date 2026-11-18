CREATE TABLE authors (
	id            INTEGER PRIMARY KEY,
	name          TEXT NOT NULL,
	email         TEXT UNIQUE,
	password_hash TEXT NOT NULL DEFAULT 'unset',
	bio           TEXT
);

CREATE TABLE posts (
	id           INTEGER PRIMARY KEY,
	author_id    INTEGER NOT NULL REFERENCES authors (id) ON DELETE CASCADE,
	title        TEXT NOT NULL,
	status       TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'published')),
	views        INTEGER NOT NULL DEFAULT 0 CHECK (views >= 0),
	published_on DATE
);

CREATE TABLE comments (
	id      INTEGER PRIMARY KEY,
	post_id INTEGER NOT NULL REFERENCES posts (id) ON DELETE RESTRICT,
	body    TEXT NOT NULL
);

CREATE TABLE post_tags (
	post_id INTEGER NOT NULL REFERENCES posts (id) ON DELETE CASCADE,
	tag     TEXT NOT NULL,
	PRIMARY KEY (post_id, tag)
);

CREATE TABLE "weird ""table"" <b>x</b>" (
	"id"                        INTEGER PRIMARY KEY,
	"col with space"            TEXT,
	"<script>alert(1)</script>" TEXT,
	"semi;colon--"              TEXT
);

CREATE TABLE nullkeys (
	k TEXT PRIMARY KEY,
	v TEXT
);

CREATE TABLE kinds (
	id             INTEGER PRIMARY KEY,
	k_varchar      VARCHAR(40),
	k_text         TEXT,
	k_int          INTEGER,
	k_float        REAL,
	k_decimal      DECIMAL(12, 4),
	k_bool         BOOLEAN,
	k_date         DATE,
	k_time         TIME,
	k_datetime     DATETIME,
	k_json         JSON,
	k_uuid         UUID,
	k_bytes        BLOB,
	k_enum         TEXT CHECK (k_enum IN ('sad', 'ok', 'happy')),
	k_secret_token TEXT
);

INSERT INTO authors (name, email, password_hash, bio) VALUES
	('Ada', 'ada@example.com', 'HASH-ADA-SECRET', 'Line one' || char(10) || 'Line two'),
	('<script>alert(1)</script>"><img src=x onerror=alert(2)>', NULL, 'HASH-XSS-SECRET', ''),
	('Grace', 'grace@example.com', 'HASH-GRACE-SECRET', NULL);

INSERT INTO posts (author_id, title, status, views, published_on) VALUES
	(1, 'Hello', 'published', 10, '2024-01-15'),
	(1, 'Draft two', 'draft', 0, NULL),
	(3, '100% _wild_', 'published', 5, '2024-03-01'),
	(3, 'Fourth', 'draft', 7, '2024-06-30'),
	(2, 'Fifth', 'published', 1, NULL);

INSERT INTO comments (post_id, body) VALUES (1, 'First!'), (1, 'Second'), (3, 'Third');

INSERT INTO post_tags (post_id, tag) VALUES (1, 'go'), (1, 'a,b/c ~x'), (3, 'go');

INSERT INTO nullkeys (k, v) VALUES (NULL, 'a'), (NULL, 'b'), ('x', 'c'), ('', 'd');

-- 1: ordinary values, with everything a form could mangle.
INSERT INTO kinds VALUES (1,
	'  <b>"quoted"</b> & ünïcödé  ',
	char(10) || 'leading newline' || char(13) || char(10) || 'crlf' || char(10) || 'lf' || char(9) || 'tab  ' || char(10),
	-42, 0.1, 12.34, 1, '2024-02-29', '03:04:05', '2024-01-02 03:04:05',
	'{"a":  [1, 2]}', '6ba7b810-9dad-11d1-80b4-00c04fd430c8', X'00FF10', 'happy', 'TOKEN-SECRET');
-- 2: NULL everywhere.
INSERT INTO kinds (id) VALUES (2);
-- 3: empty and zero values, which are not NULL.
INSERT INTO kinds VALUES (3, '', '', 0, 0.0, 0, 0, '', '', '2024-01-02T03:04:05Z', 'null', '', X'', 'sad', '');
-- 4: values that do not match the declared types, which SQLite allows.
INSERT INTO kinds VALUES (4, 'x', 'y', 'abc', 'not a number', 'twelve', 'yes', '2024-13-45', 'noon', 1700000000,
	'not json', 'not-a-uuid', 'text in a blob', NULL, NULL);
-- 5: text that no HTML form can carry.
INSERT INTO kinds (id, k_varchar, k_text) VALUES (5, 'a' || char(0) || 'b', CAST(X'61FF62' AS TEXT));
