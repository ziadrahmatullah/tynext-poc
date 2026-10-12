-- Master data device. Diisi dari contracts/devices.json oleh cmd/migrate,
-- bukan oleh file SQL, supaya daftar device hanya punya satu sumber kebenaran.

CREATE TABLE devices (
    id      uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    code    text NOT NULL UNIQUE,
    class   text NOT NULL CHECK (class IN ('ems', 'pv', 'bess', 'meter', 'weather', 'breaker', 'gen')),
    name    text NOT NULL,
    breaker text,
    section text CHECK (section IS NULL OR section IN ('A', 'B')),
    source  jsonb,
    enabled boolean NOT NULL DEFAULT true
);

COMMENT ON TABLE  devices         IS 'Daftar device PTS. Sumber: contracts/devices.json.';
COMMENT ON COLUMN devices.code    IS 'deviceId pada topic rain/pts/{class}/{deviceId}/...';
COMMENT ON COLUMN devices.breaker IS 'Kode breaker terkait, merujuk devices.code milik class breaker.';
COMMENT ON COLUMN devices.section IS 'Section A atau B. Daya genset hanya terukur per section (pm-03a / pm-03b).';
COMMENT ON COLUMN devices.source  IS 'Traceability ke register asal, mis. {"via":"mg500","regs":{"soc_pct":38}}.';
