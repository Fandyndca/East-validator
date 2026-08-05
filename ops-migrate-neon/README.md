# Migrasi stake + balance Neon → east-validator (on-chain)

Memindahkan **`balance`**, **`staked_amount`**, **`pending_unstake_amount`** dari `identity.users` (Neon, human EAST) ke state validator (BadgerDB, **subunits** × 1_000_000).

Tanpa ini, user lama tetap “kaya di Neon” tetapi Send/Stake/Unstake on-chain gagal atau stake = 0 di chain.

---

## Urutan kerja (wajib)

### A. Patch + redeploy validator dulu

API `/admin/seed` bawaan **hanya** menulis `balance`. Stake tidak ikut.

1. Ikuti **`VALIDATOR_PATCH.md`**
2. Gabungkan logika **`go-seed-account.go`** ke `internal/state/state.go` (ganti `SeedBalance` lama)
3. Ganti handler seed dengan **`go-handle-seed.go.snippet`** di `internal/api/server.go` (tambah `strings` import jika perlu)
4. Redeploy service **east-validator** di Railway

Uji:

```bash
curl -sS -X POST "$VALIDATOR/admin/seed" \
  -H "Content-Type: application/json" \
  -H "X-API-Secret: $API_SECRET" \
  -d '{
    "address":"0xD06e3ACDbbEeCf31392F5da4F8B04B99AE35ea9f",
    "balance":20000000000000,
    "staked":2000000000,
    "pending_unstake":0,
    "mode":"merge_max"
  }'

curl -sS "$VALIDATOR/account/0xD06e3ACDbbEeCf31392F5da4F8B04B99AE35ea9f"
# staked harus 2000000000 (bukan 0)
```

### B. Jalankan skrip migrasi

```bash
cd migrate-neon-to-chain
npm install

export DATABASE_IDENTITY_URL="postgres://...neon.../identity"   # pool identity
export EAST_VALIDATOR_URL="https://east-validator-production.up.railway.app"
export VALIDATOR_API_SECRET="..."   # sama dengan API_SECRET di validator

# 1) Dry-run (default) — tidak menulis
node migrate-neon-stake-to-chain.mjs --dry-run --mode=merge_max

# 2) Satu alamat dulu
node migrate-neon-stake-to-chain.mjs --execute --mode=merge_max \
  --address=0xD06e3ACDbbEeCf31392F5da4F8B04B99AE35ea9f

# 3) Semua user yang punya balance/stake/pending > 0
node migrate-neon-stake-to-chain.mjs --execute --mode=merge_max
```

### Flags

| Flag | Arti |
|------|------|
| `--dry-run` | Hanya log (default jika tanpa `--execute`) |
| `--execute` | Tulis ke `/admin/seed` |
| `--mode=merge_max` | Per field: `max(chain, neon)` — **aman**, tidak menurunkan saldo chain |
| `--mode=overwrite` | Timpa exact angka Neon (hati-hati) |
| `--mode=balance_only` | Hanya balance (API lama) |
| `--min-staked=100` | Hanya user staked ≥ 100 EAST di Neon |
| `--limit=50` | Batasi jumlah baris |
| `--address=0x...` | Satu alamat |

---

## Mode yang disarankan

**`merge_max`** untuk cutover pertama:

- Chain sudah 20M + 2K staked → Neon 111 staked → hasil staked tetap **max** (2K), tidak turun ke 111.
- Neon 5000 staked, chain 0 → chain jadi 5000.

Pakai **`overwrite`** hanya jika Anda yakin Neon adalah sumber kebenaran mutlak dan ingin menimpa angka chain.

---

## Unit

| Neon (`identity.users`) | Validator |
|-------------------------|-----------|
| `balance` human EAST | `balance` subunits (= human × 1e6) |
| `staked_amount` human | `staked` subunits |
| `pending_unstake_amount` human | `pending_unstake` subunits |

---

## Dual address (legacy vs vault)

Skrip memigrasi **`wallet_address` di Neon** (kolom Profile).

- Jika Profile masih `0xD44e…` (LEGACY) dan vault `0xD06e…`, stake Neon ikut ke **`0xD44e`**.
- Untuk unstake on-chain dari vault, stake harus ada di **`0xD06e`**.

Opsi:

1. **UPGRADE** custody dulu (wallet_address → vault), lalu jalankan migrasi, **atau**
2. Migrasi dua kali / manual seed ke alamat vault:

```bash
# Setelah tahu mapping telegram → vault EVM, seed manual:
curl -sS -X POST "$VALIDATOR/admin/seed" \
  -H "Content-Type: application/json" \
  -H "X-API-Secret: $API_SECRET" \
  -d '{"address":"0xVAULT...","balance":...,"staked":...,"pending_unstake":0,"mode":"merge_max"}'
```

---

## Setelah migrasi

1. `USE_CHAIN_BALANCE=true` + redeploy wallet → UI Active stake = chain.
2. Jangan double-spend: stop path Neon untuk send/stake (patch on-chain-only).
3. Opsional: nol-kan / kunci `staked_amount` di Neon setelah verifikasi (ops terpisah — skrip ini **tidak** mengubah Neon).

---

## Verifikasi

```bash
# Sample user
curl -sS "$VALIDATOR/account/0x..."

# Harus selaras (subunits) dengan max(neon, chain_sebelum) jika merge_max
```

Neon query cek:

```sql
SELECT telegram_id, wallet_address, balance, staked_amount, pending_unstake_amount
FROM identity.users
WHERE staked_amount > 0 OR balance > 0
ORDER BY staked_amount DESC
LIMIT 20;
```
