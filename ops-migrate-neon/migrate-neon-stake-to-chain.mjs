#!/usr/bin/env node
/**
 * EASTCHAIN — Migrasi balance + stake (+ pending unstake) dari Neon identity → east-validator
 *
 * Sumber: identity.users (human EAST, DOUBLE/NUMERIC)
 * Tujuan: POST {VALIDATOR}/admin/seed  (subunits, 1 EAST = 1_000_000)
 *
 * Prasyarat validator:
 *   - API_SECRET / X-API-Secret aktif
 *   - /admin/seed sudah support staked + pending_unstake + mode (lihat VALIDATOR_PATCH.md)
 *     Jika belum di-patch, hanya field "balance" yang tertulis di chain.
 *
 * Usage:
 *   DATABASE_IDENTITY_URL=postgres://... \
 *   EAST_VALIDATOR_URL=https://east-validator-production.up.railway.app \
 *   VALIDATOR_API_SECRET=... \
 *   node migrate-neon-stake-to-chain.mjs [--dry-run] [--execute] [--mode=merge_max|overwrite] [--min-staked=0]
 *
 * Default: --dry-run (tidak menulis ke chain).
 */

import pg from "pg";

const SUBUNITS = 1_000_000n;

const IDENTITY_URL = process.env.DATABASE_IDENTITY_URL || process.env.DATABASE_URL || "";
const VALIDATOR_URL = (process.env.EAST_VALIDATOR_URL || process.env.VALIDATOR_HTTP_URL || "")
  .trim()
  .replace(/\/$/, "");
const API_SECRET =
  process.env.VALIDATOR_API_SECRET ||
  process.env.EAST_VALIDATOR_API_SECRET ||
  process.env.API_SECRET ||
  "";

const args = new Set(process.argv.slice(2));
const EXECUTE = args.has("--execute");
const DRY_RUN = !EXECUTE || args.has("--dry-run");

function argVal(prefix, fallback) {
  const hit = process.argv.slice(2).find((a) => a.startsWith(prefix));
  if (!hit) return fallback;
  return hit.slice(prefix.length) || fallback;
}

const MODE = argVal("--mode=", "merge_max"); // merge_max | overwrite | balance_only
const MIN_STAKED_HUMAN = Number(argVal("--min-staked=", "0")) || 0;
const LIMIT = Number(argVal("--limit=", "0")) || 0; // 0 = all
const ONLY_ADDRESS = (argVal("--address=", "") || "").toLowerCase();

function humanToSubunits(human) {
  // Neon stores DOUBLE human EAST; avoid float drift via string fixed 6dp
  const n = Number(human);
  if (!Number.isFinite(n) || n <= 0) return 0n;
  const fixed = n.toFixed(6);
  const [whole, frac = ""] = fixed.split(".");
  const wholePart = BigInt(whole);
  const fracPart = BigInt((frac + "000000").slice(0, 6));
  return wholePart * SUBUNITS + fracPart;
}

function subunitsToHumanStr(sub) {
  const s = BigInt(sub);
  const whole = s / SUBUNITS;
  const frac = (s % SUBUNITS).toString().padStart(6, "0");
  return `${whole}.${frac}`;
}

async function fetchChainAccount(address) {
  const res = await fetch(`${VALIDATOR_URL}/account/${address}`, {
    headers: { Accept: "application/json" },
  });
  if (!res.ok) {
    return { balance: 0, staked: 0, pending_unstake: 0, nonce: 0, missing: true };
  }
  const j = await res.json();
  return {
    balance: Number(j.balance ?? 0),
    staked: Number(j.staked ?? 0),
    pending_unstake: Number(j.pending_unstake ?? 0),
    nonce: Number(j.nonce ?? 0),
    missing: false,
  };
}

async function seedAccount(row) {
  const body = {
    address: row.address,
    balance: Number(row.balanceSubunits),
    staked: Number(row.stakedSubunits),
    pending_unstake: Number(row.pendingSubunits),
    mode: MODE,
  };
  // Guard: JSON number safe integer range
  for (const k of ["balance", "staked", "pending_unstake"]) {
    if (!Number.isSafeInteger(body[k])) {
      throw new Error(`${k} exceeds JS safe integer — use smaller batches or bigint-aware API`);
    }
  }
  const res = await fetch(`${VALIDATOR_URL}/admin/seed`, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Accept: "application/json",
      "X-API-Secret": API_SECRET,
    },
    body: JSON.stringify(body),
  });
  const text = await res.text();
  let json;
  try {
    json = JSON.parse(text);
  } catch {
    json = { raw: text };
  }
  if (!res.ok) {
    throw new Error(`seed HTTP ${res.status}: ${text.slice(0, 400)}`);
  }
  return json;
}

async function main() {
  console.log("=== EAST Neon → Validator stake/balance migration ===");
  console.log({
    dryRun: DRY_RUN,
    mode: MODE,
    minStakedHuman: MIN_STAKED_HUMAN,
    limit: LIMIT || "all",
    onlyAddress: ONLY_ADDRESS || null,
    validator: VALIDATOR_URL || "(missing)",
    hasSecret: Boolean(API_SECRET),
    hasIdentityUrl: Boolean(IDENTITY_URL),
  });

  if (!IDENTITY_URL) {
    console.error("DATABASE_IDENTITY_URL required");
    process.exit(1);
  }
  if (!VALIDATOR_URL) {
    console.error("EAST_VALIDATOR_URL required");
    process.exit(1);
  }
  if (!DRY_RUN && !API_SECRET) {
    console.error("VALIDATOR_API_SECRET required for --execute");
    process.exit(1);
  }

  const pool = new pg.Pool({
    connectionString: IDENTITY_URL,
    ssl: IDENTITY_URL.includes("localhost") ? false : { rejectUnauthorized: false },
    max: 5,
  });

  // wallet_address is the identity address (may be legacy hash or self-custody EVM).
  // Migrate whatever is stored — operator can re-run per vault address after UPGRADE.
  let sql = `
    SELECT
      telegram_id,
      lower(wallet_address) AS wallet_address,
      COALESCE(balance, 0)::float8 AS balance,
      COALESCE(staked_amount, 0)::float8 AS staked_amount,
      COALESCE(pending_unstake_amount, 0)::float8 AS pending_unstake_amount,
      COALESCE(wallet_type, 'custodial_hash') AS wallet_type
    FROM identity.users
    WHERE wallet_address IS NOT NULL
      AND wallet_address LIKE '0x%'
      AND (
        COALESCE(balance, 0) > 0
        OR COALESCE(staked_amount, 0) > 0
        OR COALESCE(pending_unstake_amount, 0) > 0
      )
  `;
  const params = [];
  if (ONLY_ADDRESS) {
    params.push(ONLY_ADDRESS);
    sql += ` AND lower(wallet_address) = $${params.length}`;
  }
  if (MIN_STAKED_HUMAN > 0) {
    params.push(MIN_STAKED_HUMAN);
    sql += ` AND COALESCE(staked_amount, 0) >= $${params.length}`;
  }
  sql += ` ORDER BY staked_amount DESC, balance DESC`;
  if (LIMIT > 0) {
    params.push(LIMIT);
    sql += ` LIMIT $${params.length}`;
  }

  const { rows } = await pool.query(sql, params);
  console.log(`\nNeon rows to consider: ${rows.length}\n`);

  const report = {
    ok: 0,
    skipped: 0,
    failed: 0,
    dry: 0,
    errors: [],
  };

  for (const r of rows) {
    const address = r.wallet_address;
    const balanceSub = humanToSubunits(r.balance);
    const stakedSub = humanToSubunits(r.staked_amount);
    const pendingSub = humanToSubunits(r.pending_unstake_amount);

    if (balanceSub === 0n && stakedSub === 0n && pendingSub === 0n) {
      report.skipped++;
      continue;
    }

    let chain;
    try {
      chain = await fetchChainAccount(address);
    } catch (e) {
      report.failed++;
      report.errors.push({ address, error: String(e.message || e) });
      console.error(`FAIL read chain ${address}:`, e.message || e);
      continue;
    }

    const entry = {
      telegram_id: r.telegram_id,
      address,
      wallet_type: r.wallet_type,
      neon: {
        balance: r.balance,
        staked: r.staked_amount,
        pending: r.pending_unstake_amount,
      },
      neonSubunits: {
        balance: balanceSub.toString(),
        staked: stakedSub.toString(),
        pending: pendingSub.toString(),
      },
      chainBefore: {
        balance: chain.balance,
        staked: chain.staked,
        pending: chain.pending_unstake,
        human_balance: subunitsToHumanStr(chain.balance),
        human_staked: subunitsToHumanStr(chain.staked),
      },
    };

    // Informative: if merge_max and chain already >= neon, still call seed (no-op effect)
    const alreadyCovered =
      MODE === "merge_max" &&
      BigInt(chain.balance) >= balanceSub &&
      BigInt(chain.staked) >= stakedSub &&
      BigInt(chain.pending_unstake) >= pendingSub;

    if (DRY_RUN) {
      report.dry++;
      console.log(
        JSON.stringify({
          action: alreadyCovered ? "dry-run-noop-covered" : "dry-run-would-seed",
          ...entry,
        }),
      );
      continue;
    }

    try {
      const result = await seedAccount({
        address,
        balanceSubunits: balanceSub,
        stakedSubunits: stakedSub,
        pendingSubunits: pendingSub,
      });
      const after = await fetchChainAccount(address);
      report.ok++;
      console.log(
        JSON.stringify({
          action: "seeded",
          address,
          mode: MODE,
          chainAfter: {
            balance: after.balance,
            staked: after.staked,
            pending: after.pending_unstake,
            human_staked: subunitsToHumanStr(after.staked),
          },
          api: result,
        }),
      );
    } catch (e) {
      report.failed++;
      report.errors.push({ address, error: String(e.message || e) });
      console.error(`FAIL seed ${address}:`, e.message || e);
    }
  }

  await pool.end();

  console.log("\n=== Summary ===");
  console.log(report);
  if (DRY_RUN) {
    console.log("\nDry-run only. Re-run with --execute to write to validator.");
  }
  if (report.failed > 0) process.exit(2);
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
