import { countTextMatches } from "./utils.ts";
import assert from "node:assert/strict";
import test from "node:test";

test("packet search counts matches at the end of the text and handles empty or unmatched queries", () => {
  assert.equal(countTextMatches("a", "a"), 1);
  assert.equal(countTextMatches("Host: test\r\n\r\ntest", " TEST "), 2);
  assert.equal(countTextMatches("aaaa", "aa"), 2);
  assert.equal(countTextMatches("abc", "missing"), 0);
  assert.equal(countTextMatches("abc", " "), 0);
});
