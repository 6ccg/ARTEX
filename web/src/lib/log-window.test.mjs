import assert from "node:assert/strict";
import test from "node:test";
import { appendLiveLog, prependLogHistory } from "./log-window.ts";

const line = (id) => ({ db_id: id, seq: id, ts: "", level: "info", tag: "", text: "" });

test("older history advances its cursor after reaching the 5000-row window", () => {
  const full = Array.from({ length: 5000 }, (_, i) => line(1001 + i));
  const first = prependLogHistory(
    full,
    Array.from({ length: 200 }, (_, i) => line(801 + i)),
  );
  const second = prependLogHistory(
    first,
    Array.from({ length: 200 }, (_, i) => line(601 + i)),
  );
  assert.equal(first.length, 5000);
  assert.equal(first[0].db_id, 801);
  assert.equal(second[0].db_id, 601);
  assert.equal(full[0].db_id, 1001);
});

test("live trimming and replay deduplication leave the historical reading window intact", () => {
  const history = Array.from({ length: 5000 }, (_, i) => line(i + 1));
  const live = history.slice(-3000);
  assert.equal(appendLiveLog(live, line(5000)), live);
  const updated = appendLiveLog(live, line(5001));
  assert.equal(updated.length, 3000);
  assert.equal(updated[0].db_id, 2002);
  assert.equal(history.length, 5000);
  assert.equal(history[0].db_id, 1);
  assert.equal(prependLogHistory([line(2), line(3)], [line(1), line(2)]).length, 3);
});
