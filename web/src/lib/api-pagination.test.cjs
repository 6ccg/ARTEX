const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const test = require("node:test");
const vm = require("node:vm");
const ts = require("typescript");

const compiled = ts.transpileModule(fs.readFileSync(path.join(__dirname, "api.ts"), "utf8"), {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS },
}).outputText;

function client(respond) {
  const exports = {};
  vm.runInNewContext(compiled, {
    exports,
    Error,
    URLSearchParams,
    require: (id) => {
      if (id === "@/lib/mock/enabled") return { MOCK: false };
      if (id === "@/lib/mock/handler") return {};
      throw new Error(`Unexpected runtime import: ${id}`);
    },
    fetch: async (url) => ({ ok: true, status: 200, json: async () => respond(new URL(url, "http://test")) }),
  });
  return exports.api;
}

test("graph pages retain every node and deduplicate edges crossing page boundaries", async () => {
  const cursors = [];
  const edge = { src: "1", rel: "derived_from", dst: "3" };
  const api = client((url) => {
    assert.equal(url.searchParams.get("task"), "9");
    const cursor = Number(url.searchParams.get("after"));
    cursors.push(cursor);
    return cursor === 0
      ? { nodes: [{ id: "1" }, { id: "2" }], edges: [edge], next_cursor: 2, has_more: true }
      : { nodes: [{ id: "3" }], edges: [edge], next_cursor: 3, has_more: false };
  });
  const graph = await api.explorationGraph("9");
  assert.deepEqual(cursors, [0, 2]);
  assert.equal(graph.nodes.map((node) => node.id).join(","), "1,2,3");
  assert.equal(graph.edges.length, 1);
});

test("invalid graph pagination fails instead of repeatedly requesting the same page", async () => {
  const api = client(() => ({ nodes: [], edges: [], next_cursor: 0, has_more: true }));
  await assert.rejects(api.explorationGraph("9"), /cursor did not advance/);
});

test("conversation paging and ID selection carry the same server filters", async () => {
  const requests = [];
  const api = client((url) => {
    requests.push(url);
    return url.searchParams.get("view") === "ids" ? { ids: [7, 9] } : { conversations: [], total: 2 };
  });
  const filters = { scope: "automation", agent: "reporter", q: "100% #7" };
  await api.conversations({ ...filters, limit: 100, stats: 0 });
  const ids = await api.conversationIDs(filters);
  for (const url of requests)
    for (const [key, value] of Object.entries(filters)) {
      assert.equal(url.searchParams.get(key), value);
    }
  assert.equal(requests[0].searchParams.get("stats"), "0");
  assert.equal(ids.join(","), "7,9");
});
