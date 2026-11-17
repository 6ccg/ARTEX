import { applyRows, isSchemaObject, toRows } from "./tool-schema.ts";
import assert from "node:assert/strict";
import test from "node:test";

test("schema editor skips boolean properties and retains supported nested parameters", () => {
  const schema = {
    type: "object",
    required: ["count"],
    properties: {
      allowed: true,
      forbidden: false,
      count: { type: "integer", default: 0 },
      items: { type: "array", items: { required: ["name"], properties: { any: true, name: { type: "string" } } } },
    },
  };
  const rows = toRows(schema);
  assert.deepEqual(
    rows.map((row) => row.name),
    ["count", "items", "name"],
  );
  assert.equal(rows[0].defaultStr, "0");
  assert.equal(rows[0].required, true);
  assert.equal(rows[2].parentKey, "items");
  assert.equal(rows[2].required, true);
  rows[0].defaultStr = "12";
  rows[2].description = "Edited name";
  const updated = applyRows(schema, rows);
  assert.equal(updated.properties.allowed, true);
  assert.equal(updated.properties.forbidden, false);
  assert.equal(updated.properties.count.default, 12);
  assert.equal(updated.properties.items.items.properties.name.description, "Edited name");
  assert.equal(schema.properties.count.default, 0);
});

test("schema object check rejects primitive roots before the editor or save accesses properties", () => {
  for (const value of [null, [], true, "schema", 42]) {
    assert.equal(isSchemaObject(value), false);
    assert.deepEqual(toRows(value), []);
  }
  assert.equal(isSchemaObject({}), true);
});
