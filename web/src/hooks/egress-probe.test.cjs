const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const test = require("node:test");
const ts = require("typescript");

// Exercise the provider's request ownership without a browser or a live proxy.
function mount(api) {
  const slots = [];
  let cursor = 0;
  const react = {
    createContext: () => ({ Provider: "provider" }),
    useState(initial) {
      const index = cursor++;
      if (!(index in slots)) slots[index] = initial;
      return [slots[index], (value) => { slots[index] = typeof value === "function" ? value(slots[index]) : value; }];
    },
    useRef(initial) {
      const index = cursor++;
      if (!(index in slots)) slots[index] = { current: initial };
      return slots[index];
    },
    useCallback: (fn) => fn,
    useMemo: (fn) => fn(),
    useEffect() { /* Effects are covered separately by the polling scheduler tests. */ },
  };
  const source = fs.readFileSync(path.join(__dirname, "../app/(main)/_components/egress-ip-banner.tsx"), "utf8");
  const { outputText } = ts.transpileModule(source, {
    compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX },
  });
  const exports = {};
  let polling;
  new Function("require", "exports", outputText)((id) => {
    if (id === "react") return react;
    if (id === "react/jsx-runtime") return { jsx: (_type, props) => props };
    if (id === "@/lib/api") return { api };
    if (id === "@/hooks/use-polling") return { usePolling: (refresh, interval) => { polling = { refresh, interval }; } };
    return {};
  }, exports);
  return {
    render() {
      cursor = 0;
      return exports.EgressIpProvider({ children: null }).value;
    },
    get polling() { return polling; },
  };
}
function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}

test("proxy refresh shares settings and probe requests and releases ownership after success", async () => {
  const settings = deferred();
  const probe = deferred();
  let reads = 0;
  let probes = 0;
  const mounted = mount({
    settings: () => { reads++; return settings.promise; },
    testEgressProxy: () => { probes++; return probe.promise; },
  });
  const value = mounted.render();
  const first = value.probeEgress();
  const duplicate = value.probeEgress();
  assert.equal(first, duplicate);
  assert.equal(reads, 1);
  settings.resolve({ global_proxy: "http://127.0.0.1:8888" });
  await Promise.resolve();
  assert.equal(probes, 1);
  const report = { checks: [] };
  probe.resolve(report);
  await first;
  assert.equal(mounted.render().report, report);

  await mounted.render().probeEgress();
  assert.equal(reads, 2);
  assert.equal(probes, 2);
});

test("failed rechecks keep the last result and disabling discards a late response", async () => {
  const late = deferred();
  const report = { checks: [] };
  let calls = 0;
  const mounted = mount({
    settings: async () => ({ global_proxy: "", show_egress_ip: true }),
    setSettings: async (patch) => patch,
    testEgressProxy: () => {
      calls++;
      if (calls === 1) return Promise.resolve(report);
      if (calls === 2) return Promise.reject(new Error("offline"));
      return late.promise;
    },
  });
  await mounted.render().probeEgress();
  await mounted.render().setEnabled(true);
  mounted.render();
  assert.equal(mounted.polling.interval, 60_000);
  await assert.rejects(mounted.render().probeEgress(), /offline/);
  assert.equal(mounted.render().report, report);
  assert.equal(mounted.render().isLoading, false);
  const pending = mounted.render().probeEgress();
  await Promise.resolve();
  assert.equal(mounted.render().report, report);
  await mounted.render().setEnabled(false);
  late.resolve({ checks: [{ key: "late" }] });
  await pending;
  assert.equal(mounted.render().report, null);
  assert.equal(mounted.render().enabled, false);
  assert.equal(mounted.polling.interval, null);
});
