const assert = require("node:assert/strict");
const test = require("node:test");

require("ts-node").register({
  skipProject: true,
  transpileOnly: true,
  compilerOptions: { target: "ES2022", module: "CommonJS", moduleResolution: "Node" },
});
const { mockHandle } = require("./handler.ts");

test("preview traffic groups aggregate before pagination and retain original requests on drilldown", async () => {
  const groups = await mockHandle("GET", "/traffic?view=routes&host=api.acme.com&path=orders&size=1");
  assert.equal(groups.total, 1);
  assert.equal(groups.matched_count, 2);
  const [group] = groups.groups;
  assert.equal(group.url, "https://api.acme.com/v1/orders");
  assert.equal(group.count, 2);
  assert.equal(group.resp_len, 1295);
  const query = `route=${encodeURIComponent(group.url)}&method=${group.method}&size=1`;
  const first = await mockHandle("GET", `/traffic?${query}&page=0`);
  const second = await mockHandle("GET", `/traffic?${query}&page=1`);
  assert.equal(first.total, 2);
  assert.equal(second.total, 2);
  assert.deepEqual(new Set([first.exchanges[0].url, second.exchanges[0].url]), new Set([
    "https://api.acme.com/v1/orders?id=1001", "https://api.acme.com/v1/orders?id=1002",
  ]));
});
