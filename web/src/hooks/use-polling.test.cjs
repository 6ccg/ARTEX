const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const test = require("node:test");
const ts = require("typescript");

function mountPoller(refresh, visibility = "visible", initialDelay) {
  const timers = new Map();
  const listeners = new Map();
  let cleanup;
  let nextTimer = 0;
  const document = {
    visibilityState: visibility,
    addEventListener: (event, callback) => listeners.set(event, callback),
    removeEventListener: (event) => listeners.delete(event),
  };
  const source = fs.readFileSync(path.join(__dirname, "use-polling.ts"), "utf8");
  const { outputText } = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  });
  const exports = {};
  new Function("require", "exports", "document", "setTimeout", "clearTimeout", outputText)(
    () => ({
      useEffect: (effect) => {
        cleanup = effect();
      },
    }),
    exports,
    document,
    (callback) => {
      timers.set(++nextTimer, callback);
      return nextTimer;
    },
    (id) => timers.delete(id),
  );
  // biome-ignore lint/correctness/useHookAtTopLevel: the harness captures useEffect directly to test timer cleanup without a DOM.
  if (initialDelay === undefined) exports.usePolling(refresh, 1000);
  else cleanup = exports.startPolling(refresh, 1000, initialDelay);
  return {
    timers,
    tick() {
      const [id, callback] = timers.entries().next().value;
      timers.delete(id);
      callback();
    },
    hide() {
      document.visibilityState = "hidden";
      listeners.get("visibilitychange")?.();
    },
    show() {
      document.visibilityState = "visible";
      listeners.get("visibilitychange")?.();
    },
    unmount() {
      cleanup();
      assert.equal(listeners.size, 0);
    },
  };
}

test("polls wait for a slow response, stop in the background and resume once", async () => {
  let calls = 0;
  let finish;
  const poller = mountPoller(() => {
    calls++;
    return new Promise((resolve) => {
      finish = resolve;
    });
  });
  assert.equal(calls, 1);
  assert.equal(poller.timers.size, 0);
  poller.hide();
  poller.show();
  assert.equal(calls, 1);
  finish();
  await Promise.resolve();
  assert.equal(poller.timers.size, 1);
  poller.hide();
  assert.equal(poller.timers.size, 0);
  poller.show();
  assert.equal(calls, 2);
  poller.unmount();
  finish();
  await Promise.resolve();
  assert.equal(poller.timers.size, 0);
});

test("a page opened in the background waits until visible", () => {
  let calls = 0;
  const poller = mountPoller(() => {
    calls++;
  }, "hidden");
  assert.equal(calls, 0);
  poller.show();
  assert.equal(calls, 1);
  poller.unmount();
});

test("delayed polling retries after failure and cancels pending work when hidden or unmounted", async () => {
  let calls = 0;
  const poller = mountPoller(async () => {
    calls++;
    if (calls === 1) throw new Error("offline");
  }, "visible", 400);
  assert.equal(calls, 0);
  assert.equal(poller.timers.size, 1);
  poller.tick();
  await Promise.resolve();
  assert.equal(calls, 1);
  assert.equal(poller.timers.size, 1);
  poller.hide();
  assert.equal(poller.timers.size, 0);
  poller.show();
  await Promise.resolve();
  assert.equal(calls, 2);
  assert.equal(poller.timers.size, 1);
  poller.unmount();
  assert.equal(poller.timers.size, 0);
});
