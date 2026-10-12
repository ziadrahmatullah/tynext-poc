# DRAFT — final menunggu tim OT

Daftar tag per device untuk publisher TF6701. **Belum final.** Nama tag, satuan, dan skala di bawah
adalah usulan sisi cloud; yang mengikat baru ada setelah tim OT mengirim tag list final dari TwinCAT.
Kolom "Sumber" dicantumkan untuk traceability — supaya tiap angka di DB bisa dilacak balik ke register
aslinya.

Rujukan arsitektur: diagram **PTS-BESS-EMS-PD-001 Rev P** (11 Okt 2026).

Aturan umum:

- `metrics` hanya angka; `state` untuk enum dan boolean.
- Satuan daya **kW**, reaktif **kvar**, tegangan **V**, arus **A**, frekuensi **Hz**, SOC **%**.
- Tanda daya: **+ = discharge / konsumsi dari sisi bus**, − = charge. Berlaku untuk `bess-01.p_kw`.
- `ts` diambil dari jam PLC (NTP), ISO 8601 UTC dengan milidetik.

---

## `ems-pv-01` — class `ems`

Nilai internal loop EMS-PV Controller. Tidak ada register eksternal; semuanya variabel TwinCAT.

### metrics

| Tag | Satuan | Arti | Sumber |
|---|---|---|---|
| `p_pm02_kw` | kW | Daya terukur PM-02 yang dipakai loop sebagai feed-forward | internal (mirror `pm-02.p_kw`, MG-500-B reg 1078) |
| `p_pv_inv_kw` | kW | Total daya inverter PV terbaca | internal (mirror `combox-01`, reg 2604) |
| `p_pv_fact_kw` | kW | Daya PV aktual setelah pembatasan | internal |
| `p_lim_fact_kw` | kW | Limit yang benar-benar berlaku setelah ramp/clamp | internal |
| `pi_trim_kw` | kW | Keluaran PI trim (≤ 150 kW, Ti 20 s) | internal |
| `eta_link` | — | Efisiensi link yang dipakai loop feed-forward | internal (parameter) |
| `p_pv_lim_kw` | kW | Setpoint `P_PV,lim` yang ditulis ke Combox | internal → Combox reg **40050** (write) |
| `p_chg_allow_kw` | kW | Envelope charge: `MIN(f(SOC), P_chg,BMS, 1500, 1000)` | internal |
| `p_load_kw` | kW | Beban pabrik | internal (mirror `pm-04.p_kw`) |
| `p_gen_kw` | kW | `pm-03a.p_kw + pm-03b.p_kw` — per section, bukan per unit | internal |
| `p_stg_adv_kw` | kW | Advisory setpoint untuk operator STG | internal |
| `dp_kw` | kW | `p_gen_kw − p_stg_adv_kw` | internal |
| `q_ref_kvar` | kvar | Referensi Q ke PV (default 0 karena PF = 1) | internal |

### state

| Tag | Nilai | Catatan |
|---|---|---|
| `mode` | `OFF` \| `MAN` \| `AUTO` | Mode loop EMS-PV |
| `override` | `none` \| `T-01` \| `T-02` \| `T-03` \| `T-04` | T-01 over-charge · T-02 comm fault ≥10 s & SOC ≥92% · T-03 BESS not forming · T-04 gen reverse power |
| `adv_rule` | `TBC` | Id rule advisory STG yang sedang aktif. Enumerasi menunggu tim OT; contoh di repo memakai `R1`…`R6` |
| `pv_day` | `TBC` | Klasifikasi hari PV. Contoh di repo memakai `LOW` \| `MID` \| `HIGH` |
| `q_mode` | `TBC` | Q_MODE 0 = PF 1,000 (default), Q_MODE 1 opsional (OP-07, menunggu Hopewind). Contoh di repo memakai `PF1` \| `Q_SET` |

---

## `bess-01` — class `bess`

Sumber: Topband MG-500-B, Modbus TCP port **1502**, map **V105**, akses **read-only**.
MG-500-B tetap master BESS (VF/slack, black start, auto-start DG) dan logikanya tidak diubah.

| Tag | Jenis | Satuan | Sumber |
|---|---|---|---|
| `soc_pct` | metric | % | MG-500-B reg **38** |
| `p_chg_bms_kw` | metric | kW | MG-500-B reg **46** (batas charge dari BMS) |
| `p_kw` | metric | kW | Σ PCS reg **15018** (+ = discharge, − = charge) |
| `f_hz` | metric | Hz | MG-500-B (`TBC`: nomor register) |
| `forming` | state | boolean | MG-500-B (`TBC`: nomor register) |

---

## `pm-02` — class `meter`

| Tag | Jenis | Satuan | Sumber |
|---|---|---|---|
| `p_kw` | metric | kW | MG-500-B reg **1078** (ADL400 dibaca lewat MG-500-B) |

---

## `combox-01` — class `pv`

Sumber: Hopewind Combox2000, Modbus TCP port **502** lewat fiber optik.

| Tag | Jenis | Satuan | Sumber |
|---|---|---|---|
| `p_pv_inv_kw` | metric | kW | Combox reg **2604** |
| `p_pv_lim_readback_kw` | metric | kW | Combox reg **2612** (readback dari limit yang ditulis ke reg 40050) |

---

## `pv-inv-01` … `pv-inv-09` — class `pv`

9× Hopewind 110 kW, dibaca lewat Combox.

| Tag | Jenis | Satuan | Sumber |
|---|---|---|---|
| `p_kw` | metric | kW | Combox, per inverter (`TBC`: offset register per unit) |
| `q_kvar` | metric | kvar | Combox, per inverter (`TBC`) |
| `status` | state | string | Combox (`TBC`: pemetaan kode status → string) |

---

## `pm-01`, `pm-02b`, `pm-03a`, `pm-03b`, `pm-04` — class `meter`

| Tag | Jenis | Satuan |
|---|---|---|
| `p_kw` | metric | kW |
| `q_kvar` | metric | kvar |
| `v` | metric | V |
| `i_a` | metric | A |
| `f_hz` | metric | Hz |

| Device | Sumber |
|---|---|
| `pm-01` | Combox Meter #1 (`TBC`: register map) |
| `pm-02b` | PM5560, Modbus TCP dari PLC (`TBC`: alamat & register map) |
| `pm-03a` | EL3453 — Section A (STG-1, DG-1, DG-2, DG-3). CT 2000/5 A, siklus EtherCAT 10 ms. `TBC`: slot terminal |
| `pm-03b` | EL3453 — Section B (STG-2). CT 2000/5 A, siklus 10 ms. `TBC`: slot terminal |
| `pm-04` | EL3453 — beban pabrik di QF-L. CT 2000/5 A, siklus 10 ms. `TBC`: slot terminal |

> Daya genset **tidak terukur per unit**. `p_gen_kw = pm-03a.p_kw + pm-03b.p_kw`.

---

## `ws-01` — class `weather`

Sumber: Combox Unit 3.

| Tag | Jenis | Satuan |
|---|---|---|
| `irradiance_wm2` | metric | W/m² |
| `t_amb_c` | metric | °C |
| `t_module_c` | metric | °C |

---

## `qf-1` … `qf-6`, `qf-g2`, `qf-t2`, `qf-l`, `qf-p1` — class `breaker`

`metrics` dikirim kosong (`{}`).

| Tag | Jenis | Nilai | Sumber |
|---|---|---|---|
| `closed` | state | boolean | DI aux breaker ke PLC |
| `tripped` | state | boolean | DI aux breaker ke PLC |

---

## `stg-01`, `stg-02`, `dg-01`, `dg-02`, `dg-03` — class `gen`

Status saja, **tanpa kW**. `metrics` dikirim kosong (`{}`).

| Tag | Jenis | Nilai | Sumber |
|---|---|---|---|
| `on` | state | boolean | DI aux ke PLC |

---

## Yang masih ditunggu dari tim OT

1. Tag list final dari TwinCAT (nama variabel, tipe, skala, dead-band).
2. Nomor register untuk semua baris bertanda `TBC` di atas.
3. Enumerasi nilai `adv_rule`, `pv_day`, `q_mode`, dan `pv-inv-*.status`.
4. Konfirmasi bahwa `ts` diambil dari jam PLC yang ter-sinkron NTP.
5. Konfirmasi device ke-35 jika memang ada — daftar Rev P yang kami terima hanya menghasilkan 34.

---

## Kode alarm advisory STG

`dp_kw = p_gen_kw − p_stg_adv_kw`. Alarm terbit setelah `|dp_kw| > 50 kW` bertahan 60 s.

| Kode | Kondisi | Arti bagi operator |
|---|---|---|
| `A-11` | `dp_kw > +50 kW` | Output STG di atas advisory → **turunkan** |
| `A-12` | `dp_kw < −50 kW` | Output STG di bawah advisory → **naikkan** |

> `TBC` — A-13…A-15 disebut di Rev P tetapi definisinya belum kami terima.
