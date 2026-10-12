-- Query bukti untuk PoC RAIN / PTS.
--
-- Pakai:  make check                        (semua query, lewat container postgres)
--         psql "$RAIN_PG_DSN" -f sql/checks.sql
--
-- Jendela waktu default 1 jam terakhir. Ubah lewat :window, mis.
--         make check WINDOW="15 minutes"

\set ON_ERROR_STOP on
\set QUIET on
\pset border 2
\pset null '∅'

\if :{?window}
\else
  \set window '1 hour'
\endif

\echo ''
\echo '════ 1. Kelengkapan per device per menit ════'
\echo 'Satu "pesan" = satu (device, ts). Device class breaker dan gen tidak muncul'
\echo 'di sini karena mereka mengirim state saja tanpa metrics, jadi tidak ada baris'
\echo 'telemetry untuk dihitung. Kelengkapan mereka diukur lewat skenario 03.'
\echo ''

-- name: kelengkapan-per-menit
WITH per_menit AS (
    SELECT d.code,
           date_trunc('minute', t.ts) AS menit,
           count(DISTINCT t.ts)       AS pesan
    FROM telemetry t
    JOIN devices d ON d.id = t.device_id
    WHERE t.ts > now() - :'window'::interval
    GROUP BY d.code, date_trunc('minute', t.ts)
)
SELECT code,
       count(*)                                        AS menit_terukur,
       min(pesan)                                      AS min_pesan,
       round(avg(pesan), 1)                            AS rata_pesan,
       max(pesan)                                      AS maks_pesan,
       round(100.0 * avg(pesan) / 60, 2)               AS persen_thd_60
FROM per_menit
GROUP BY code
ORDER BY persen_thd_60, code;

\echo ''
\echo '════ 2. Gap > 1 detik per device ════'
\echo 'Jarak antar pesan berurutan. Yang muncul di sini adalah detik yang hilang.'
\echo ''

-- name: gap-lebih-dari-1-detik
WITH pesan AS (
    SELECT DISTINCT d.code, t.ts
    FROM telemetry t
    JOIN devices d ON d.id = t.device_id
    WHERE t.ts > now() - :'window'::interval
), berurutan AS (
    SELECT code, ts, lag(ts) OVER (PARTITION BY code ORDER BY ts) AS ts_sebelumnya
    FROM pesan
)
SELECT code,
       ts_sebelumnya,
       ts,
       round(EXTRACT(EPOCH FROM (ts - ts_sebelumnya))::numeric, 3) AS gap_detik
FROM berurutan
WHERE ts_sebelumnya IS NOT NULL
  AND ts - ts_sebelumnya > interval '1.5 seconds'
ORDER BY gap_detik DESC, code
LIMIT 50;

\echo ''
\echo '════ 3. Duplikat ════'
\echo 'Angka resmi duplikat yang dibuang ada di metrics ingest:'
\echo '    curl -s $RAIN_HTTP_ADDR/metrics | grep rain_ingest_duplicates_total'
\echo 'Query di bawah membuktikan sisi database: tidak boleh ada baris ganda sama'
\echo 'sekali, karena (device_id, metric, ts) adalah primary key.'
\echo ''

-- name: duplikat-di-database
SELECT d.code, t.metric, t.ts, count(*) AS baris
FROM telemetry t
JOIN devices d ON d.id = t.device_id
WHERE t.ts > now() - :'window'::interval
GROUP BY d.code, t.metric, t.ts
HAVING count(*) > 1
ORDER BY baris DESC
LIMIT 20;

\echo ''
\echo '════ 4. Lag (received_at − ts) ════'
\echo ''

-- name: lag-p50-p95-maks
SELECT d.code,
       count(*)                                                                            AS baris,
       round((percentile_cont(0.50) WITHIN GROUP (ORDER BY lag_ms))::numeric, 1)           AS p50_ms,
       round((percentile_cont(0.95) WITHIN GROUP (ORDER BY lag_ms))::numeric, 1)           AS p95_ms,
       round(max(lag_ms)::numeric, 1)                                                      AS maks_ms
FROM (
    SELECT t.device_id,
           EXTRACT(EPOCH FROM (t.received_at - t.ts)) * 1000 AS lag_ms
    FROM telemetry t
    WHERE t.ts > now() - :'window'::interval
) x
JOIN devices d ON d.id = x.device_id
GROUP BY d.code
ORDER BY p95_ms DESC NULLS LAST
LIMIT 15;

\echo ''
\echo '── Lag keseluruhan ──'

-- name: lag-keseluruhan
SELECT count(*)                                                                  AS baris,
       round((percentile_cont(0.50) WITHIN GROUP (ORDER BY lag_ms))::numeric, 1) AS p50_ms,
       round((percentile_cont(0.95) WITHIN GROUP (ORDER BY lag_ms))::numeric, 1) AS p95_ms,
       round(max(lag_ms)::numeric, 1)                                            AS maks_ms
FROM (
    SELECT EXTRACT(EPOCH FROM (received_at - ts)) * 1000 AS lag_ms
    FROM telemetry
    WHERE ts > now() - :'window'::interval
) x;

\echo ''
\echo '════ 5. Riwayat online / offline ════'
\echo 'device_status menyimpan keadaan terakhir; state_log menyimpan transisinya.'
\echo ''

-- name: status-terakhir
SELECT d.code,
       s.online,
       s.last_seen,
       s.fw_version,
       round(EXTRACT(EPOCH FROM (now() - s.last_seen))::numeric, 1) AS detik_lalu
FROM device_status s
JOIN devices d ON d.id = s.device_id
ORDER BY s.online, d.code;

\echo ''
\echo '── Transisi offline dari alarm STALE (sumber: server) ──'

-- name: riwayat-stale
SELECT d.code,
       a.severity,
       a.raised_at,
       a.cleared_at,
       CASE WHEN a.cleared_at IS NULL THEN '∅'
            ELSE round(EXTRACT(EPOCH FROM (a.cleared_at - a.raised_at))::numeric, 1)::text
       END AS durasi_detik
FROM alarms a
JOIN devices d ON d.id = a.device_id
WHERE a.code = 'STALE' AND a.raised_at > now() - :'window'::interval
ORDER BY a.raised_at DESC
LIMIT 30;

\echo ''
\echo '════ 6. Alarm aktif ════'
\echo ''

-- name: alarm-aktif
SELECT a.source,
       a.code,
       d.code AS device,
       a.severity,
       a.message,
       a.value,
       a.raised_at
FROM alarms a
LEFT JOIN devices d ON d.id = a.device_id
WHERE a.cleared_at IS NULL
ORDER BY CASE a.severity WHEN 'critical' THEN 0 WHEN 'warning' THEN 1 ELSE 2 END,
         a.raised_at DESC;

\echo ''
\echo '════ 7. Pesan yang ditolak ════'
\echo ''

-- name: reject-per-alasan
SELECT left(reason, 70) AS alasan, count(*) AS jumlah, max(received_at) AS terakhir
FROM ingest_rejects
WHERE received_at > now() - :'window'::interval
GROUP BY left(reason, 70)
ORDER BY jumlah DESC
LIMIT 20;

\echo ''
\echo '════ 8. Ringkasan ════'
\echo ''

-- name: ringkasan
SELECT (SELECT count(*) FROM devices)                                                  AS device,
       (SELECT count(*) FROM telemetry WHERE ts > now() - :'window'::interval)           AS baris_telemetry,
       (SELECT count(DISTINCT (device_id, ts)) FROM telemetry
         WHERE ts > now() - :'window'::interval)                                         AS pesan_telemetry,
       (SELECT count(*) FROM ingest_rejects WHERE received_at > now() - :'window'::interval) AS reject,
       (SELECT count(*) FROM alarms WHERE cleared_at IS NULL)                          AS alarm_aktif,
       (SELECT count(*) FROM state_log WHERE ts > now() - :'window'::interval)           AS transisi_state;
