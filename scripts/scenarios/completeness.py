#!/usr/bin/env python3
"""Bandingkan count-file simulator dengan isi database.

Dipakai skenario 03. Keluarannya satu baris JSON supaya skripnya bisa membaca
angka-angkanya dengan jq.

    completeness.py <count-file> <db-counts> <threshold>

<db-counts> berisi satu baris per device: "<code> <jumlah pesan tersimpan>".
"""
import json
import sys


def main() -> int:
    if len(sys.argv) != 4:
        print(__doc__, file=sys.stderr)
        return 2

    counts = json.load(open(sys.argv[1], encoding="utf-8"))
    threshold = float(sys.argv[3])

    stored_by_code = {}
    with open(sys.argv[2], encoding="utf-8") as handle:
        for line in handle:
            line = line.strip()
            if not line:
                continue
            code, n = line.rsplit(" ", 1)
            stored_by_code[code] = int(n)

    scored, skipped = [], []
    for code, c in sorted(counts["devices"].items()):
        sent = c["telemetry"]
        stored = stored_by_code.get(code, 0)
        # Device class breaker dan gen mengirim state saja tanpa metrics, jadi
        # tidak ada baris telemetry untuk dihitung. Mereka dilaporkan terpisah
        # alih-alih diberi nilai 0% yang menyesatkan.
        if c["metricsPerMessage"] == 0:
            skipped.append({"code": code, "class": c["class"], "sent": sent})
            continue
        pct = 100.0 * stored / sent if sent else 0.0
        scored.append({"code": code, "sent": sent, "stored": stored, "pct": round(pct, 3)})

    total_sent = sum(r["sent"] for r in scored)
    total_stored = sum(r["stored"] for r in scored)
    overall = 100.0 * total_stored / total_sent if total_sent else 0.0
    worst = min(scored, key=lambda r: r["pct"]) if scored else None

    json.dump({
        "overall": round(overall, 3),
        "totalSent": total_sent,
        "totalStored": total_stored,
        "scoredDevices": len(scored),
        "skipped": skipped,
        "worst": worst,
        "below": [r for r in scored if r["pct"] < threshold],
        "corrupted": counts["totals"]["corrupted"],
        "duplicated": counts["totals"]["duplicated"],
        "queued": counts["totals"]["queued"],
    }, sys.stdout)
    print()
    return 0


if __name__ == "__main__":
    sys.exit(main())
