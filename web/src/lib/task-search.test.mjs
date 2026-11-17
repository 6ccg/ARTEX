import { matchesTaskQuery } from "./task-search.ts";
import assert from "node:assert/strict";
import test from "node:test";

test("task search retains all fields and refreshes edited task snapshots", () => {
  const task = { id: "123", name: "Portal", category_name: "External", description: "应用检查", goal: "Inspect LOGIN" };
  for (const query of ["", "123", "portal", "external", "应用", "login"]) assert.ok(matchesTaskQuery(task, query));
  assert.equal(matchesTaskQuery(task, "portalexternal"), false);
  assert.equal(matchesTaskQuery(task, "changed"), false);
  assert.ok(matchesTaskQuery({ ...task, goal: "Changed goal" }, "changed"));
});
