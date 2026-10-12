-- Pesan yang tidak lolos validasi. Disimpan utuh supaya bisa diperiksa bersama
-- tim OT, bukan hanya dihitung.

CREATE TABLE ingest_rejects (
    id          bigserial PRIMARY KEY,
    topic       text NOT NULL,
    payload     bytea,
    reason      text NOT NULL,
    received_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX ingest_rejects_received_at ON ingest_rejects (received_at DESC);

COMMENT ON TABLE  ingest_rejects         IS 'Payload invalid. Ingest mencatat lalu lanjut — tidak pernah crash.';
COMMENT ON COLUMN ingest_rejects.payload IS 'Byte asli apa adanya, termasuk kalau bukan JSON.';
