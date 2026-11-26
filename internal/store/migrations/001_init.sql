CREATE TABLE IF NOT EXISTS person(
    id            INTEGER PRIMARY KEY,
    name          TEXT    NOT NULL,
    weight        REAL    NOT NULL DEFAULT 0.2,
    cap_override  INTEGER,           -- bytes; NULL = weight-derived
    enabled       INTEGER NOT NULL DEFAULT 1,
    sub_token     TEXT    NOT NULL UNIQUE,
    created_at    INTEGER NOT NULL,
    locale        TEXT    NOT NULL DEFAULT 'fa'
);

CREATE TABLE IF NOT EXISTS device(
    mac             TEXT PRIMARY KEY,
    ip              TEXT,
    hostname        TEXT,
    first_seen      INTEGER,
    last_seen       INTEGER,
    owner_person_id INTEGER REFERENCES person(id) ON DELETE SET NULL,
    bound_at        INTEGER,
    bind_source     TEXT
);
CREATE INDEX IF NOT EXISTS device_ip ON device(ip);

CREATE TABLE IF NOT EXISTS route(
    id            INTEGER PRIMARY KEY,
    name          TEXT    NOT NULL UNIQUE,
    kind          TEXT    NOT NULL DEFAULT 'direct',
    upstream_json TEXT    NOT NULL DEFAULT '{}',
    allow_all     INTEGER NOT NULL DEFAULT 1,
    enabled       INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE IF NOT EXISTS credential(
    id        INTEGER PRIMARY KEY,
    person_id INTEGER NOT NULL REFERENCES person(id) ON DELETE CASCADE,
    route_id  INTEGER NOT NULL REFERENCES route(id) ON DELETE CASCADE,
    uuid      TEXT    NOT NULL,
    port      INTEGER NOT NULL,
    enabled   INTEGER NOT NULL DEFAULT 1,
    UNIQUE(person_id, route_id)
);

CREATE TABLE IF NOT EXISTS class(
    id   INTEGER PRIMARY KEY,
    name TEXT NOT NULL UNIQUE
);

CREATE TABLE IF NOT EXISTS rate(
    route_id INTEGER NOT NULL REFERENCES route(id) ON DELETE CASCADE,
    class_id INTEGER NOT NULL REFERENCES class(id) ON DELETE CASCADE,
    weight   REAL    NOT NULL DEFAULT 1.0,
    PRIMARY KEY (route_id, class_id)
);

CREATE TABLE IF NOT EXISTS period(
    id                INTEGER PRIMARY KEY,
    start_utc         INTEGER NOT NULL UNIQUE,
    end_utc           INTEGER NOT NULL,
    anchor_jalali_day INTEGER NOT NULL,
    pool_charged      INTEGER,
    guest_pool        INTEGER,
    raw_ceiling       INTEGER,
    closed_at         INTEGER,
    alloc_mode        TEXT    NOT NULL DEFAULT 'weight' -- weight | manual
);

CREATE TABLE IF NOT EXISTS allocation(
    period_id INTEGER NOT NULL,
    person_id INTEGER NOT NULL,
    bytes     INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY(period_id, person_id)
);

CREATE TABLE IF NOT EXISTS usage(
    period_id  INTEGER NOT NULL,
    subject    TEXT    NOT NULL,
    route_id   INTEGER NOT NULL,
    class_id   INTEGER NOT NULL,
    bytes_up   INTEGER NOT NULL DEFAULT 0,
    bytes_down INTEGER NOT NULL DEFAULT 0,
    charged    REAL    NOT NULL DEFAULT 0,
    PRIMARY KEY(period_id, subject, route_id, class_id)
);

CREATE TABLE IF NOT EXISTS block(
    subject    TEXT PRIMARY KEY,
    until      INTEGER,
    reason     TEXT,
    created_by TEXT
);

CREATE TABLE IF NOT EXISTS event(
    at      INTEGER NOT NULL,
    kind    TEXT    NOT NULL,
    subject TEXT,
    detail  TEXT
);

CREATE TABLE IF NOT EXISTS setting(
    k TEXT PRIMARY KEY,
    v TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS domestic_site(
    domain  TEXT NOT NULL,
    network TEXT NOT NULL,
    updated TEXT NOT NULL DEFAULT '',
    due     TEXT NOT NULL DEFAULT '',
    PRIMARY KEY(domain, network)
);
CREATE INDEX IF NOT EXISTS domestic_site_domain ON domestic_site(domain);

INSERT OR IGNORE INTO class(id, name) VALUES (1, 'default'), (2, 'nimbaha');

INSERT OR IGNORE INTO setting(k, v) VALUES
    ('anchor_start_jalali', ''),
    ('pool_charged_gb', ''),
    ('guest_pool_gb', '5'),
    ('guest_device_gb', '1'),
    ('raw_ceiling_gb', ''),
    ('credential_base_port', '10443');
