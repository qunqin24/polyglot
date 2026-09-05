import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import ts from "typescript";

const source = await readFile(new URL("../src/lib/query-cache.ts", import.meta.url), "utf8");
const compiled = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 },
});
const { getQuery, invalidateQueries, clearQueries } = await import(
  `data:text/javascript;base64,${Buffer.from(compiled.outputText).toString("base64")}`
);

const deferred = () => {
  let resolve, reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
};
const realNow = Date.now;
let now = realNow();
Date.now = () => now;

try {
  // Two mounted consumers and a fast return navigation share a single slow
  // request. The second visit can synchronously paint data, without 300 ms RTT.
  let requests = 0;
  const providers = getQuery('["providers"]');
  const unsubscribe = providers.subscribe(() => {});
  const fetchProviders = async () => {
    requests++;
    await new Promise((resolve) => setTimeout(resolve, 300));
    return [{ id: 1, enabled: true }];
  };
  const first = providers.load(fetchProviders);
  assert.equal(providers.load(fetchProviders), first);
  await first;
  unsubscribe();
  const revisit = getQuery('["providers"]');
  assert.equal(revisit, providers);
  assert.deepEqual(revisit.getSnapshot().data, [{ id: 1, enabled: true }]);
  await revisit.load(fetchProviders);
  assert.equal(requests, 1, "return navigation must not fetch fresh data again");

  // Stale data remains visible during refresh, repeated polls join the same
  // pending request, and a network failure keeps the last useful result.
  now += 16_000;
  const refresh = deferred();
  const refreshing = providers.load(() => refresh.promise);
  assert.equal(providers.getSnapshot().loading, true);
  assert.equal(providers.getSnapshot().data[0].enabled, true);
  assert.equal(providers.load(fetchProviders, true), refreshing);
  refresh.reject(new Error("offline"));
  await refreshing;
  assert.equal(providers.getSnapshot().error.message, "offline");
  assert.equal(providers.getSnapshot().data[0].id, 1);
  await providers.load(async () => [{ id: 1, enabled: false }], true);
  assert.equal(providers.getSnapshot().data[0].enabled, false);
  assert.equal(providers.getSnapshot().error, null);

  // Saving while an earlier GET is still in flight must not allow that old
  // response to roll back the newly loaded configuration.
  const old = deferred();
  const oldLoad = providers.load(() => old.promise, true);
  invalidateQueries();
  await providers.load(async () => [{ id: 2 }]);
  old.resolve([{ id: 0 }]);
  await oldLoad;
  assert.deepEqual(providers.getSnapshot().data, [{ id: 2 }]);

  // Filter/page identities are isolated. Null is a completed result, not an
  // endless loading state (several optional model pickers return null).
  const pageOne = getQuery('["logs",1]');
  const pageTwo = getQuery('["logs",2]');
  await pageOne.load(async () => [1]);
  await pageTwo.load(async () => [2]);
  assert.deepEqual(pageOne.getSnapshot().data, [1]);
  assert.deepEqual(pageTwo.getSnapshot().data, [2]);
  const optional = getQuery('["optional"]');
  await optional.load(async () => null);
  assert.equal(optional.getSnapshot().settled, true);
  assert.equal(optional.getSnapshot().loading, false);

  // Logout clears data without starting fresh authenticated requests. A late
  // result cannot repopulate either the old view or the next session's cache.
  const late = deferred();
  const lateLoad = providers.load(() => late.promise, true);
  const revision = providers.getSnapshot().revision;
  clearQueries();
  assert.equal(providers.getSnapshot().revision, revision);
  assert.equal(providers.getSnapshot().data, null);
  const nextSession = getQuery('["providers"]');
  assert.notEqual(nextSession, providers);
  late.resolve([{ id: 999 }]);
  await lateLoad;
  assert.equal(providers.getSnapshot().data, null);
  assert.equal(nextSession.getSnapshot().data, null);

  // Bound idle cache lifetime and entry count while retaining mounted data.
  const keep = nextSession.subscribe(() => {});
  const idle = getQuery('["idle"]');
  now += 5 * 60_000;
  assert.notEqual(getQuery('["idle"]'), idle);
  assert.equal(getQuery('["providers"]'), nextSession);
  const oldest = getQuery('["oldest"]');
  for (let i = 0; i < 101; i++) getQuery(JSON.stringify(["filter", i]));
  assert.notEqual(getQuery('["oldest"]'), oldest);
  assert.equal(getQuery('["providers"]'), nextSession);
  keep();
} finally {
  clearQueries();
  Date.now = realNow;
}

console.log("Query cache: slow navigation, deduplication, refresh, mutation races, session isolation and eviction passed.");
