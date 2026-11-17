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

test("graph refresh fetches only changed ranges and keeps edges crossing range boundaries", async () => {
  const bounds = [];
  const edge = { src: "1", rel: "derived_from", dst: "3" };
  const ranges = [
    { after: "0", through: "2", revision: "a" },
    { after: "2", through: "3", revision: "b" },
  ];
  const api = client((url) => {
    assert.equal(url.searchParams.get("task"), "9");
    if (url.searchParams.get("view") === "index") return { ranges };
    const after = url.searchParams.get("after");
    bounds.push(after);
    return after === "0"
      ? { nodes: [{ id: "1" }, { id: "2" }], edges: [edge], has_more: false }
      : { nodes: [{ id: "3" }], edges: [edge], has_more: false };
  });
  const graph = await api.explorationGraph("9");
  assert.deepEqual(bounds, ["0", "2"]);
  assert.equal(graph.nodes.map((node) => node.id).join(","), "1,2,3");
  assert.equal(graph.edges.length, 1);
  assert.equal(await api.explorationGraph("9", graph), graph);
  assert.deepEqual(bounds, ["0", "2"]);
  ranges[1] = { ...ranges[1], revision: "c" };
  const refreshed = await api.explorationGraph("9", graph);
  assert.deepEqual(bounds, ["0", "2", "2"]);
  assert.equal(refreshed.ranges[0], graph.ranges[0]);
  assert.equal(refreshed.edges.length, 1);
});

test("a graph range that grows during refresh is retried on the next poll", async () => {
  const api = client((url) => url.searchParams.has("view")
    ? { ranges: [{ after: "0", through: "3", revision: "a" }] }
    : { nodes: [], edges: [], has_more: true });
  await assert.rejects(api.explorationGraph("9"), /range changed/);
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
