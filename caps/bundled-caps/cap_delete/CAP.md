---
name: cap_delete
description: "Completely remove a saved capability from the stores: the learnings rows (cap + proposal categories) with their FK-cascade children (entities/actions/keywords/anticipated_queries/cluster_scores) + FTS + session_active_caps, then the cap_<name>__* data tables + cap_store_meta in the cap store. Every sqlite3 call runs with -bail and a __OK__ sentinel, so a SQL error aborts instead of half-applying, and stderr stays separate so the reason survives. Safety-checks knowledge_gaps.resolved_by before any write. Returns {cap_name, found, deleted:{learnings, data_tables}, notes?}. Does NOT remove the on-disk CAP.md (a disk-backed cap returns at the next daemon start) and does not touch scheduled_jobs. Input: {cap_name: string} matching [a-zA-Z][a-zA-Z0-9_]* (normalised to lower case). Irreversible."
version: 546
tags: [capability, maintenance, destructive]
scope: user
auto_active: true
---

## Purpose

Completely remove a saved capability from the databases. Two stores are involved:

- **yesmem.db** — the `learnings` rows for the cap (its own row plus the
  supersede chain and any staged proposal rows), their FK-cascade children
  (`learning_entities`, `learning_actions`, `learning_keywords`,
  `learning_anticipated_queries`, `learning_cluster_scores`), the
  `anticipated_queries_fts` and `learnings_fts` rows, and the
  `session_active_caps` rows for the cap.
- **caps.db** — the daemon's cap store (`internal/storage/cap_store.go`,
  `OpenCapsDB` uses `<dir>/caps.db`): every `cap_<name>__*` data table, the
  `cap_store_meta` entry, and any `session_active_caps` rows (`capDB()` prefers
  caps.db when it is open). `capabilities.db` is the legacy file name and is
  only consulted as a fallback for old installs.

History: `session_active_caps` was `session_active_capabilities` and its key
column was `capability_name` until the v0.55 rename
(`internal/storage/schema.go:420-421`). Deleting on the old name fails at
prepare time, so activated entries survived a cap deletion — this version keys
on `cap_name`.

Failure handling: every `sqlite3` invocation runs with `-bail`, and each
transaction ends with `SELECT '__OK__'`, which is only reached if `COMMIT` ran.

- stderr is deliberately *not* merged into stdout. The daemon's `sh()` captures
  the streams separately and throws with stderr, so `2>&1` would replace the SQL
  error with a bare "exit code N"; leaving stderr alone keeps the reason.
- a REPL whose `sh()` returns stdout instead of throwing never sees that throw,
  so a missing `__OK__` is also treated as a failure.
- the cap-store stage is wrapped in try/catch: if it throws after the yesmem.db
  half has committed, the handler still returns `{error, detail, partial}`
  instead of letting the throw escape with no report.

`-bail` is what makes the outcome independent of how sqlite3 is invoked: as a
single argv command it already stops at the first error, but reading a
multi-line script from stdin it aborts only the failing line and still commits
the rest — a partially applied delete. Statement lists are built from the tables
that actually exist, so `-bail` never trips over a legitimately absent table.

Input: `{cap_name: string}` matching `[a-zA-Z][a-zA-Z0-9_]*`; it is lower-cased
before use, because cap names are lower-case by construction
(`storage.ValidateCapName`) and the table prefix match is case-sensitive.

**Not covered — check these yourself after a delete:**

- the `CAP.md` on disk under `<capsDir>/<name>/`. `SyncCapsFromDisk` runs at
  every daemon start and re-imports it, so a disk-backed cap comes back. The
  result carries a `note` naming the file when one is found.
- `scheduled_jobs` rows that reference the cap (`scheduled_jobs.cap_name`). Such
  a job keeps firing and logs `cap "<name>" not found` on every tick; disable or
  delete it yourself.

## Scripts

### cap_delete
kind: tool

```javascript
async ({cap_name}) => {
  if (typeof cap_name !== 'string' || !/^[a-zA-Z][a-zA-Z0-9_]*$/.test(cap_name)) {
    return {error: 'cap_name must match [a-zA-Z][a-zA-Z0-9_]*'};
  }
  cap_name = cap_name.toLowerCase();
  const capsDir = (await sh('echo $HOME')).trim() + '/.claude/caps';
  const dir = (await sh('echo $HOME')).trim() + '/.claude/yesmem';
  const ydb = dir + '/yesmem.db';
  const capDbs = [dir + '/caps.db', dir + '/capabilities.db'];

  const isFile = async (p) => (await sh('[ -f "' + p + '" ] && echo yes || echo no')).trim() === 'yes';

  const tableNames = async (db) => {
    if (!(await isFile(db))) return [];
    const out = await sh('sqlite3 -bail "' + db + '" "SELECT name FROM sqlite_master WHERE type = \'table\';"');
    return out.split('\n').map(s => s.trim()).filter(s => s.length > 0);
  };

  // One transaction. null on success, else the failure text. -bail stops at the
  // first SQL error, and __OK__ only appears when COMMIT ran, so a half-applied
  // delete can be neither reported nor mistaken for success. stderr stays a
  // separate stream: the daemon's sh() throws with it, and merging would hide
  // the reason behind "exit code N".
  const runTx = async (db, stmts) => {
    if (!stmts.length) return null;
    const out = await sh('sqlite3 -bail "' + db + '" "BEGIN IMMEDIATE; ' + stmts.join(' ') + ' COMMIT; SELECT \'__OK__\';"');
    const lines = out.split('\n').map(s => s.trim()).filter(s => s.length > 0);
    return lines[lines.length - 1] === '__OK__' ? null : (out.trim() || 'sqlite3 produced no output');
  };

  const notes = [];
  let ids = [];
  if (await isFile(ydb)) {
    // Exact prefix match, no wildcard semantics: '_' is a pattern wildcard, so a
    // pattern for e.g. cap_delete would also match a sibling cap named capXdelete.
    // Restricted to cap-ish categories so an unrelated row that happens to share
    // the prefix is never swept up.
    const idsRaw = await sh('sqlite3 -bail "' + ydb + '" "SELECT id FROM learnings WHERE substr(content, 1, ' + (cap_name.length + 2) + ') = \'' + cap_name + ' —\' AND category IN (\'cap\', \'capability\', \'cap_proposed\', \'cap_proposed_accepted\', \'cap_proposed_rejected\');"');
    ids = idsRaw.split('\n').map(s => s.trim()).filter(s => /^\d+$/.test(s)).map(Number);
  } else {
    notes.push('yesmem.db not found — only the cap store was cleaned');
  }

  if (ids.length) {
    const idsSql = '(' + ids.join(',') + ')';
    const kgRaw = await sh('sqlite3 -bail "' + ydb + '" "SELECT id, topic FROM knowledge_gaps WHERE resolved_by IN ' + idsSql + ';"');
    if (kgRaw.trim()) return {error: 'knowledge_gaps FK collision — resolve manually first', refs: kgRaw.trim()};

    const present = await tableNames(ydb);
    const yStmts = [];
    for (const t of ['learning_entities', 'learning_actions', 'learning_keywords', 'learning_anticipated_queries', 'learning_cluster_scores']) {
      if (present.includes(t)) yStmts.push('DELETE FROM ' + t + ' WHERE learning_id IN ' + idsSql + ';');
    }
    if (present.includes('anticipated_queries_fts')) yStmts.push('DELETE FROM anticipated_queries_fts WHERE learning_id IN ' + idsSql + ';');
    // The activation column was renamed in schema v0.55 (internal/storage/schema.go:421).
    if (present.includes('session_active_caps')) yStmts.push('DELETE FROM session_active_caps WHERE cap_name=\'' + cap_name + '\';');
    yStmts.push('DELETE FROM learnings WHERE id IN ' + idsSql + ';');
    if (present.includes('learnings_fts')) yStmts.push('DELETE FROM learnings_fts WHERE rowid IN ' + idsSql + ';');

    const yerr = await runTx(ydb, yStmts);
    if (yerr) return {error: 'yesmem.db tx failed', detail: yerr};
  } else if (notes.length === 0) {
    notes.push('no learnings row matched — only the cap store was cleaned');
  }

  const dropped = [];
  try {
    for (const cdb of capDbs) {
      const present = await tableNames(cdb);
      if (!present.length) continue;
      const prefixed = present.filter(t => t.indexOf('cap_' + cap_name + '__') === 0);
      const unsafe = prefixed.filter(t => !/^[a-zA-Z_][a-zA-Z0-9_]*$/.test(t));
      if (unsafe.length) return {error: 'unquotable table name(s) — nothing dropped in ' + cdb, detail: unsafe.join(', '), partial: {learnings: ids}};
      const stmts = prefixed.map(t => 'DROP TABLE IF EXISTS ' + t + ';');
      if (present.includes('cap_store_meta')) stmts.push('DELETE FROM cap_store_meta WHERE cap_name=\'' + cap_name + '\';');
      // session_active_caps can live here too (storage/session_caps.go capDB()).
      if (present.includes('session_active_caps')) stmts.push('DELETE FROM session_active_caps WHERE cap_name=\'' + cap_name + '\';');
      const err = await runTx(cdb, stmts);
      if (err) return {error: 'cap store tx failed', detail: cdb + ': ' + err, partial: {learnings: ids, data_tables: dropped}};
      dropped.push(...prefixed);
    }
  } catch (e) {
    return {error: 'cap store stage threw', detail: String(e && e.message ? e.message : e), partial: {learnings: ids, data_tables: dropped}};
  }

  // The on-disk CAP.md is out of scope for deletion, but silence about it would
  // make the delete look permanent when SyncCapsFromDisk will re-import it.
  const diskCap = capsDir + '/' + cap_name + '/CAP.md';
  if (await isFile(diskCap)) {
    notes.push('CAP.md still on disk at ' + diskCap + ' — SyncCapsFromDisk re-imports it at the next daemon start; remove the file yourself');
  }

  const result = {cap_name, found: ids.length > 0, deleted: {learnings: ids, data_tables: dropped}};
  if (notes.length) result.notes = notes;
  return result;
}
```
