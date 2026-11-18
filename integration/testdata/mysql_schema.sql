-- Fixture for the MySQL introspection tests. The databases app and billing are
-- introspected, in that order; the expected result is in mysql_schema.golden.

CREATE DATABASE IF NOT EXISTS app;
CREATE DATABASE IF NOT EXISTS billing;
CREATE DATABASE IF NOT EXISTS archive;

CREATE TABLE IF NOT EXISTS app.users (
  id            BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  email         VARCHAR(254) NOT NULL,
  name          VARCHAR(100) NOT NULL DEFAULT '',
  role          ENUM('admin','member','guest') NOT NULL DEFAULT 'member',
  is_active     TINYINT(1) NOT NULL DEFAULT 1,
  password_hash TEXT NOT NULL,
  balance       DECIMAL(10,2) NULL,
  rating        FLOAT NULL,
  score         DOUBLE NULL,
  avatar        BLOB NULL,
  settings      JSON NULL,
  legacy        JSON NULL,
  external_id   CHAR(36) NOT NULL DEFAULT (UUID()),
  country       CHAR(2) NULL,
  nickname      VARCHAR(255) NULL,
  birthday      DATE NULL,
  wake_time     TIME NULL,
  created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at    DATETIME NULL,
  tags          JSON NULL,
  session_note  TEXT NULL,
  referral      VARCHAR(255) NULL DEFAULT 'none'
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS app.posts (
  id          INT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  author_id   BIGINT NOT NULL,
  reviewer_id BIGINT NULL,
  parent_id   INT NULL,
  title       TEXT NOT NULL,
  body        TEXT NULL,
  status      VARCHAR(32) NOT NULL DEFAULT 'draft',
  word_count  INT GENERATED ALWAYS AS (CHAR_LENGTH(body)) STORED,
  revision    BIGINT NOT NULL DEFAULT 0,
  CONSTRAINT posts_author FOREIGN KEY (author_id) REFERENCES app.users (id) ON DELETE CASCADE,
  CONSTRAINT posts_reviewer FOREIGN KEY (reviewer_id) REFERENCES app.users (id) ON DELETE SET NULL ON UPDATE CASCADE,
  CONSTRAINT posts_parent FOREIGN KEY (parent_id) REFERENCES app.posts (id) ON DELETE RESTRICT,
  CONSTRAINT posts_status CHECK (status IN ('draft', 'published'))
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS app.tags (
  code  VARCHAR(64) NOT NULL PRIMARY KEY,
  label TEXT NOT NULL
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS app.post_tags (
  post_id  INT NOT NULL,
  tag_code VARCHAR(64) NOT NULL,
  added_at TIMESTAMP NULL,
  PRIMARY KEY (tag_code, post_id),
  UNIQUE KEY uq_post_tags_by_post (post_id, tag_code),
  CONSTRAINT post_tags_post FOREIGN KEY (post_id) REFERENCES app.posts (id) ON DELETE CASCADE,
  CONSTRAINT post_tags_tag FOREIGN KEY (tag_code) REFERENCES app.tags (code) ON DELETE RESTRICT ON UPDATE CASCADE
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS app.tag_notes (
  id       BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  post_id  INT NOT NULL,
  tag_code VARCHAR(64) NOT NULL,
  note     TEXT NULL,
  CONSTRAINT tag_notes_fk FOREIGN KEY (post_id, tag_code) REFERENCES app.post_tags (post_id, tag_code) ON DELETE CASCADE
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS app.tag_votes (
  voter_id BIGINT NOT NULL,
  tag_code VARCHAR(64) NOT NULL,
  post_id  INT NOT NULL,
  CONSTRAINT tag_votes_user FOREIGN KEY (voter_id) REFERENCES app.users (id),
  CONSTRAINT tag_votes_post_tag FOREIGN KEY (tag_code, post_id) REFERENCES app.post_tags (tag_code, post_id)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS app.events (
  id          BIGINT NOT NULL AUTO_INCREMENT,
  happened_at TIMESTAMP NOT NULL,
  kind        TEXT NOT NULL,
  user_id     BIGINT NULL,
  PRIMARY KEY (id, happened_at),
  CONSTRAINT events_user FOREIGN KEY (user_id) REFERENCES app.users (id) ON DELETE SET NULL
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS app.event_notes (
  event_id    BIGINT NOT NULL,
  happened_at TIMESTAMP NOT NULL,
  note        TEXT NULL,
  CONSTRAINT event_notes_fk FOREIGN KEY (event_id, happened_at) REFERENCES app.events (id, happened_at)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS app.empty (
  dummy INT NULL
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS archive.invoices (
  id BIGINT NOT NULL PRIMARY KEY
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS billing.users (
  user_id    BIGINT NOT NULL PRIMARY KEY,
  currency   ENUM('EUR','USD') NOT NULL DEFAULT 'EUR',
  vat_number TEXT NULL,
  CONSTRAINT billing_users_app FOREIGN KEY (user_id) REFERENCES app.users (id) ON DELETE CASCADE
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS billing.invoices (
  id          BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  user_id     BIGINT NOT NULL,
  archived_as BIGINT NULL DEFAULT NULL,
  total       DECIMAL(20,2) NOT NULL,
  issued_on   DATE NOT NULL DEFAULT (CURRENT_DATE),
  CONSTRAINT billing_invoices_user FOREIGN KEY (user_id) REFERENCES billing.users (user_id),
  CONSTRAINT billing_invoices_archive FOREIGN KEY (archived_as) REFERENCES archive.invoices (id) ON DELETE SET DEFAULT
) ENGINE=InnoDB;

-- Ignored: tables outside the introspected databases, indexes and views.
CREATE DATABASE IF NOT EXISTS public_db;
CREATE TABLE IF NOT EXISTS public_db.outside (id INT PRIMARY KEY) ENGINE=InnoDB;
CREATE OR REPLACE VIEW app.active_users AS SELECT id, email FROM app.users WHERE is_active = 1;