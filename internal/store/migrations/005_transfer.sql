CREATE TABLE IF NOT EXISTS transfer(
    id             INTEGER PRIMARY KEY,
    period_id      INTEGER NOT NULL,
    from_person_id INTEGER NOT NULL,
    to_person_id   INTEGER NOT NULL,
    bytes          INTEGER NOT NULL,
    at             INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS transfer_period ON transfer(period_id);
