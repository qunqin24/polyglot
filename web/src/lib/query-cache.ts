// Session-memory cache for page data. Payload viewers and revealed secrets do
// not use it. Navigation reuses recent data; explicit reloads always revalidate.
const staleTime = 15_000;
const retentionTime = 5 * 60_000;
const maxEntries = 100;

interface Snapshot<T> {
  data: T | null;
  error: unknown;
  loading: boolean;
  revision: number;
  settled: boolean;
}

export class Query<T> {
  private snapshot: Snapshot<T> = { data: null, error: null, loading: false, revision: 0, settled: false };
  private listeners = new Set<() => void>();
  private pending: Promise<void> | null = null;
  private generation = 0;
  private fetchedAt = 0;
  touchedAt = Date.now();

  getSnapshot = () => this.snapshot;
  subscribe = (listener: () => void) => {
    this.touchedAt = Date.now();
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
      this.touchedAt = Date.now();
    };
  };
  get active() { return this.listeners.size > 0 || this.pending !== null; }

  private publish(next: Snapshot<T>) {
    this.snapshot = next;
    for (const listener of this.listeners) listener();
  }

  invalidate() {
    this.generation++;
    this.pending = null;
    this.fetchedAt = 0;
    this.publish({
      ...this.snapshot,
      error: null,
      loading: false,
      revision: this.snapshot.revision + 1,
    });
  }

  clear(error: unknown) {
    this.generation++;
    this.pending = null;
    this.fetchedAt = 0;
    // Do not revalidate signed-out queries. Their requests require a session.
    this.publish({ data: null, error, loading: false, revision: this.snapshot.revision, settled: true });
  }

  load(fn: () => Promise<T>, force = false): Promise<void> {
    // Even an explicit refresh joins an outstanding request instead of
    // stacking requests when the network is slower than the polling interval.
    if (this.pending) return this.pending;
    if (!force && this.fetchedAt > 0 && Date.now() - this.fetchedAt < staleTime) {
      return Promise.resolve();
    }
    const generation = this.generation;
    this.publish({ ...this.snapshot, loading: true, error: null });
    const pending = Promise.resolve().then(fn).then(
      (data) => {
        if (generation !== this.generation) return;
        this.fetchedAt = Date.now();
        this.touchedAt = this.fetchedAt;
        this.publish({ ...this.snapshot, data, error: null, loading: false, settled: true });
      },
      (error: unknown) => {
        if (generation !== this.generation) return;
        this.publish({ ...this.snapshot, error, loading: false, settled: true });
      },
    ).finally(() => {
      if (this.pending === pending) this.pending = null;
    });
    this.pending = pending;
    return pending;
  }
}

const queries = new Map<string, Query<unknown>>();

export function getQuery<T>(key: string): Query<T> {
  const now = Date.now();
  // Only idle entries are evicted: mounted components keep a single shared
  // subscription, and a slow request can finish after its page was left.
  for (const [id, query] of queries) {
    if (!query.active && now - query.touchedAt >= retentionTime) queries.delete(id);
  }
  let query = queries.get(key);
  if (!query) {
    for (const [id, candidate] of queries) {
      if (queries.size < maxEntries) break;
      if (!candidate.active) queries.delete(id);
    }
    query = new Query<unknown>();
  }
  query.touchedAt = now;
  queries.delete(key);
  queries.set(key, query);
  return query as Query<T>;
}

// Incrementing generations also prevents an old, slow GET from overwriting a
// successful edit or restoring data from a session that has signed out.
export function invalidateQueries() {
  for (const query of queries.values()) query.invalidate();
}

export function clearQueries(error: unknown = null) {
  for (const query of queries.values()) query.clear(error);
  queries.clear();
}
