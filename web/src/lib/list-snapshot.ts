// API snapshots contain plain JSON values. Compare without serializing large
// strings, and retain unchanged rows so memoized components can skip rendering.
export function sameSnapshot(left: unknown, right: unknown): boolean {
  if (Object.is(left, right)) return true;
  if (left === null || right === null || typeof left !== "object" || typeof right !== "object") return false;
  if (Array.isArray(left) !== Array.isArray(right)) return false;
  const previous = left as Record<string, unknown>;
  const next = right as Record<string, unknown>;
  const keys = Object.keys(previous);
  if (keys.length !== Object.keys(next).length) return false;
  return keys.every((key) => Object.hasOwn(next, key) && sameSnapshot(previous[key], next[key]));
}

export function reconcileList<T>(previous: T[], incoming: T[], keyOf: (item: T) => string): T[] {
  const byKey = new Map(previous.map((item) => [keyOf(item), item]));
  const next = incoming.map((item) => {
    const existing = byKey.get(keyOf(item));
    return existing !== undefined && sameSnapshot(existing, item) ? existing : item;
  });
  return next.length === previous.length && next.every((item, index) => item === previous[index]) ? previous : next;
}
