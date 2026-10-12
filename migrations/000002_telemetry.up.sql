-- Deret waktu numerik dan nilai non-numerik.

-- TODO(prod): PARTITION BY RANGE (ts) + pg_partman
-- Partisi harian ditunda di PoC: satu jam data dari 34 device muat dengan
-- nyaman di satu tabel, dan partisi menambah bagian yang bisa gagal tanpa
-- membuktikan apa pun yang diuji di sini.
CREATE TABLE telemetry (
    device_id   uuid NOT NULL REFERENCES devices (id) ON DELETE CASCADE,
    metric      text NOT NULL,
    ts          timestamptz NOT NULL,
    value       double precision NOT NULL,
    received_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (device_id, metric, ts)
);

-- Primary key (device_id, metric, ts) sekaligus jadi kunci dedup: pengiriman
-- ulang QoS 1 dan replay buffer masuk lewat ON CONFLICT DO NOTHING.

CREATE INDEX telemetry_ts_brin ON telemetry USING brin (ts);

COMMENT ON TABLE  telemetry             IS 'Satu baris per (device, metric, ts). Dedup lewat primary key.';
COMMENT ON COLUMN telemetry.ts          IS 'Jam PLC dari payload. TBC: acuan timestamp belum diputuskan.';
COMMENT ON COLUMN telemetry.received_at IS 'Jam server saat pesan ditulis. received_at - ts = lag.';

CREATE TABLE device_state (
    device_id uuid NOT NULL REFERENCES devices (id) ON DELETE CASCADE,
    key       text NOT NULL,
    value     text NOT NULL,
    ts        timestamptz NOT NULL,
    PRIMARY KEY (device_id, key)
);

COMMENT ON TABLE device_state IS 'Nilai state terakhir per device. Boolean disimpan sebagai ''true''/''false''.';

CREATE TABLE state_log (
    id        bigserial PRIMARY KEY,
    device_id uuid NOT NULL REFERENCES devices (id) ON DELETE CASCADE,
    key       text NOT NULL,
    old_value text,
    new_value text NOT NULL,
    ts        timestamptz NOT NULL
);

CREATE INDEX state_log_device_ts ON state_log (device_id, ts DESC);

COMMENT ON TABLE state_log IS 'Hanya transisi: baris ditulis ketika nilai berbeda dari yang tersimpan.';
