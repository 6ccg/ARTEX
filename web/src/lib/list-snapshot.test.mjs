import { reconcileList, sameSnapshot } from "./list-snapshot.ts";
import assert from "node:assert/strict";
import test from "node:test";

test("polling reuses unchanged rows while retaining changed counters and ordering", () => {
  const previous = [
    { id: "1", tokens: { input: 3 } },
    { id: "2", tokens: { input: 5 } },
  ];
  const unchanged = structuredClone(previous);
  assert.equal(
    reconcileList(previous, unchanged, (row) => row.id),
    previous,
  );
  unchanged[1].tokens.input++;
  const updated = reconcileList(previous, unchanged, (row) => row.id);
  assert.equal(updated[0], previous[0]);
  assert.equal(updated[1], unchanged[1]);
  assert.equal(updated[1].tokens.input, 6);
  const reordered = reconcileList(previous, [unchanged[1], unchanged[0]], (row) => row.id);
  assert.deepEqual(
    reordered.map((row) => row.id),
    ["2", "1"],
  );
  assert.equal(reordered[1], previous[0]);
  assert.deepEqual(
    reconcileList(previous, [], (row) => row.id),
    [],
  );
});

test("snapshot equality recognizes unchanged empty polls and nested data changes", () => {
  assert.ok(sameSnapshot({ items: [], total: 0 }, { total: 0, items: [] }));
  assert.ok(!sameSnapshot({ items: [] }, { items: [1] }));
  assert.ok(!sameSnapshot({ count: null }, {}));
  assert.ok(!sameSnapshot([], {}));
});
