CREATE TABLE events (
    seq INTEGER PRIMARY KEY CHECK (seq > 0),
    run_id TEXT NOT NULL,
    event BLOB NOT NULL
);
CREATE INDEX events_by_run ON events(run_id, seq);
CREATE TABLE projections (
    namespace TEXT NOT NULL,
    key TEXT NOT NULL,
    value BLOB NOT NULL,
    PRIMARY KEY (namespace, key)
);
CREATE TABLE metadata (key TEXT PRIMARY KEY, value TEXT NOT NULL);
INSERT INTO metadata(key, value) VALUES ('jsonl_imported', 'false');
