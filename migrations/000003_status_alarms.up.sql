-- Status online/offline dan alarm.

CREATE TABLE device_status (
    device_id  uuid PRIMARY KEY REFERENCES devices (id) ON DELETE CASCADE,
    online     boolean NOT NULL,
    last_seen  timestamptz,
    fw_version text
);

COMMENT ON TABLE  device_status           IS 'Diisi dari topic status (retained) dan dari LWT.';
COMMENT ON COLUMN device_status.last_seen IS 'ts payload; untuk LWT dipakai waktu terima karena LWT tidak membawa ts.';

CREATE TABLE alarms (
    id         bigserial PRIMARY KEY,
    source     text NOT NULL CHECK (source IN ('plc', 'server')),
    code       text,
    device_id  uuid REFERENCES devices (id) ON DELETE CASCADE,
    severity   text NOT NULL CHECK (severity IN ('info', 'warning', 'critical')),
    message    text NOT NULL,
    value      numeric,
    raised_at  timestamptz NOT NULL,
    cleared_at timestamptz
);

-- Satu alarm aktif per (device, code). Ini yang membuat 'raised' idempoten:
-- pengiriman ulang pesan yang sama tidak menambah baris.
CREATE UNIQUE INDEX alarms_active_uniq
    ON alarms (device_id, code)
    WHERE cleared_at IS NULL AND code IS NOT NULL;

CREATE INDEX alarms_open ON alarms (raised_at DESC) WHERE cleared_at IS NULL;

COMMENT ON TABLE  alarms        IS 'source=plc dari topic alarm; source=server dari stale detector ingest.';
COMMENT ON COLUMN alarms.code   IS 'A-xx / T-xx dari PLC, atau STALE dari server.';
COMMENT ON COLUMN alarms.value  IS 'Angka pemicu, mis. dp_kw saat A-12 terbit.';
