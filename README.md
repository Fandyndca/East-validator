# east-validator (Go) — Sealer + Local State

Lightweight validator for EASTCHAIN, designed for **Railway free tier**.

## Role

- **Fullnode Browser** (online, stake ≥ 1000 EAST) → propose blocks bergantian
- **This service (Railway)** → verify signature + seal + simpan state
- **Lightnode** → terima header yang sudah di-seal

## Features (Phase 1.1)

- Local state (BadgerDB): balances, stake, pending unstake, nonces
- **Genesis** dengan hard cap **1,000,000,000 EAST** + supply buckets (sama tokenomics existing)
- **EIP-191 / secp256k1** signature (compatible ethers `personal_sign`)
- Endpoint `POST /consensus/propose` untuk Fullnode Browser
- Pruning block lama (default keep 3000)
- Single binary, Dockerfile kecil

## Env vars (Railway)

| Variable | Wajib | Keterangan |
|----------|-------|------------|
| `API_SECRET` | ya | Proteksi endpoint write |
| `CHAIN_SIGNING_PRIVATE_KEY` | ya (untuk seal) | 0x + 32-byte hex (secp256k1) |
| `CHAIN_SIGNING_ADDRESS` | disarankan | Alamat publik pasangan key di atas |
| `NODE_ID` | - | default `validator-1` |
| `DATA_DIR` | - | default `/app/data` (pasang Volume!) |
| `GENESIS_PATH` | - | default `/app/genesis.json` |
| `KEEP_RECENT_BLOCKS` | - | default `3000` |
| `PORT` | auto | Railway inject otomatis |

## API

### Public
```
GET /health
GET /stats
GET /account/{address}
GET /block/latest
GET /block/{height}
GET /supply
GET /supply/{bucket}
```

### Protected (`X-API-Secret`)
```
POST /tx
POST /consensus/propose
POST /admin/seed
POST /admin/prune
```

### Contoh propose (Fullnode Browser)

```json
POST /consensus/propose
{
  "proposal_id": "prop-123",
  "height": 1,
  "prev_hash": "GENESIS",
  "tx_hashes": ["0xabc..."],
  "merkle_root": "0x...",
  "block_hash": "0x...",
  "timestamp": 1730000000000,
  "proposer": "0xYourEvmAddress",
  "signature": "0x...EIP191..."
}
```

Signature message format:
```
EASTCHAIN_PROPOSAL|{proposal_id}|{height}|{block_hash}
```

Sealer menandatangani header dengan:
```
EASTCHAIN_BLOCK|{height}|{block_hash}
```

## Genesis / Max Supply

Hard-coded & di-validate di boot:

| Bucket | Cap |
|--------|-----|
| mining | 300,000,000 |
| staking | 80,000,000 |
| validator | 80,000,000 |
| campaign | 40,000,000 |
| liquidity | 150,000,000 |
| treasury | 100,000,000 |
| emergency | 70,000,000 |
| marketing | 70,000,000 |
| team | 60,000,000 |
| founder | 50,000,000 |
| **TOTAL** | **1,000,000,000** |

Min stake validator: **100 EAST**

Min stake fullnode: **10 EAST** (sama `VALIDATOR_MINIMUM_STAKE`).

## Deploy Railway

1. Push repo ke GitHub
2. New Project → Deploy from GitHub
3. **Volume** → mount `/app/data`
4. Set env di atas
5. Healthcheck path: `/health`

## Masih TODO (jangan pakai dana real dulu)

- Tx signature verification (saat ini `/tx` masih open setelah API secret)
- Leader schedule on-sealer (masih percaya proposal yang masuk)
- State root Merkle sungguhan
- Hubungkan broadcast ke Railway hub / lightnode yang sudah ada
