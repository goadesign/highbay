// browser_harness.cjs drives a real Chromium-family browser through the
// DevTools protocol for the opt-in Go test
// internal/project/views_browser_test.go (build tag `browser`). The Go test
// starts the project server, a second listener it owns on other origins, and
// the browser (headless, with a throwaway profile); it then runs
//
//   node browser_harness.cjs <input.json>
//
// This script opens tabs in that browser, does what the scenario below says,
// and prints what the browser itself sent, enforced and showed as one JSON
// object on stdout. It asserts nothing: the Go test judges the observations.
// It needs only Node's built-in WebSocket and no package.
"use strict";

const fs = require("node:fs");

// STEP_TIMEOUT_MS bounds every DevTools command, every awaited event, and
// every awaited page condition.
const STEP_TIMEOUT_MS = 15000;

// POLL_MS is the interval between two checks of an awaited page condition.
const POLL_MS = 25;

// VIOLATION_COLLECTOR runs in every document before the document's own
// script, through DevTools rather than the page, so the page's policy does not
// apply to it. It keeps every securitypolicyviolation the document reports.
const VIOLATION_COLLECTOR =
  "(" +
  function collect() {
    const seen = [];
    Object.defineProperty(window, "__surlaViolations", { value: seen });
    document.addEventListener("securitypolicyviolation", (event) => {
      seen.push({
        effective_directive: event.effectiveDirective,
        violated_directive: event.violatedDirective,
        blocked_uri: event.blockedURI,
        disposition: event.disposition,
        source_file: event.sourceFile,
      });
    });
  }.toString() +
  ")();";

// DevTools is one connection to the browser's DevTools endpoint. Commands to
// a tab carry that tab's flat session ID; events are handed to listeners.
class DevTools {
  // connect opens the browser-wide DevTools WebSocket at url.
  static connect(url) {
    return withDeadline(
      new Promise((resolve, reject) => {
        const ws = new WebSocket(url);
        ws.addEventListener("open", () => resolve(new DevTools(ws)), { once: true });
        ws.addEventListener("error", () => reject(new Error("could not connect to DevTools at " + url)), {
          once: true,
        });
      }),
      "connecting to DevTools"
    );
  }

  constructor(ws) {
    this.ws = ws;
    this.nextId = 1;
    this.pending = new Map();
    this.listeners = new Set();
    ws.addEventListener("message", (event) => this.receive(JSON.parse(String(event.data))));
    ws.addEventListener("close", () => {
      for (const { reject, method } of this.pending.values()) {
        reject(new Error("DevTools connection closed during " + method));
      }
      this.pending.clear();
    });
  }

  // send runs one DevTools command, in the tab of sessionId when given, and
  // resolves to its result.
  send(method, params = {}, sessionId) {
    const id = this.nextId++;
    const message = { id, method, params };
    if (sessionId) {
      message.sessionId = sessionId;
    }
    const answered = new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject, method });
      this.ws.send(JSON.stringify(message));
    });
    return withDeadline(answered, method).finally(() => this.pending.delete(id));
  }

  // on calls listener with every event until the returned function is called.
  on(listener) {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  }

  // waitForEvent resolves to the first event matching predicate.
  waitForEvent(predicate, what) {
    let off = () => {};
    const seen = new Promise((resolve) => {
      off = this.on((message) => {
        if (predicate(message)) {
          resolve(message);
        }
      });
    });
    return withDeadline(seen, what).finally(() => off());
  }

  // receive settles a command's answer or hands an event to the listeners.
  receive(message) {
    if (message.id !== undefined) {
      const call = this.pending.get(message.id);
      if (!call) {
        return;
      }
      if (message.error) {
        call.reject(new Error(call.method + ": " + message.error.message));
      } else {
        call.resolve(message.result);
      }
      return;
    }
    for (const listener of Array.from(this.listeners)) {
      listener(message);
    }
  }

  close() {
    this.ws.close();
  }
}

// withDeadline rejects with what when promise has not settled within
// STEP_TIMEOUT_MS.
function withDeadline(promise, what) {
  let timer;
  const deadline = new Promise((_, reject) => {
    timer = setTimeout(() => reject(new Error("timed out after " + STEP_TIMEOUT_MS + "ms: " + what)), STEP_TIMEOUT_MS);
  });
  return Promise.race([promise, deadline]).finally(() => clearTimeout(timer));
}

// sleep resolves after ms milliseconds; it only spaces out condition checks.
function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

// openTab creates a new tab on about:blank and starts recording what the
// browser does in it, including in frames that run in other processes.
async function openTab(dt) {
  const { targetId } = await dt.send("Target.createTarget", { url: "about:blank" });
  const { sessionId } = await dt.send("Target.attachToTarget", { targetId, flatten: true });
  const tab = {
    dt,
    targetId,
    // sessionId is the tab's own DevTools session.
    sessionId,
    // sessions holds the tab's session and those of frames and workers that
    // run in other processes; their events are recorded as the tab's.
    sessions: new Set([sessionId]),
    // frameSessions maps a frame in another process to its session.
    frameSessions: new Map(),
    // requests lists every network request in the order the browser began it.
    requests: [],
    byId: new Map(),
    // console holds console, log and target messages as text.
    console: [],
    // frames and contexts are every frame commit and script context.
    frames: [],
    contexts: [],
    // session is the first Authorization the page sent; it is used only to
    // try it from other origins and never printed.
    session: null,
  };
  dt.on((message) => {
    if (!tab.sessions.has(message.sessionId)) {
      return;
    }
    if (message.method === "Target.attachedToTarget") {
      void attachChild(tab, message.params);
      return;
    }
    if (message.method === "Target.detachedFromTarget") {
      tab.console.push("detached " + message.params.sessionId);
      return;
    }
    observe(tab, message);
  });
  await enableDomains(tab, sessionId, true);
  await dt.send("Target.setAutoAttach", { autoAttach: true, waitForDebuggerOnStart: true, flatten: true }, sessionId);
  return tab;
}

// enableDomains turns on the events this script records in one session.
async function enableDomains(tab, sessionId, isFrame) {
  if (isFrame) {
    await tab.dt.send("Page.enable", {}, sessionId);
    await tab.dt.send("Page.addScriptToEvaluateOnNewDocument", { source: VIOLATION_COLLECTOR }, sessionId);
  }
  await tab.dt.send("Runtime.enable", {}, sessionId);
  await tab.dt.send("Network.enable", {}, sessionId);
  await tab.dt.send("Log.enable", {}, sessionId);
}

// attachChild records a frame or worker that runs in another process as part
// of tab, then lets it start.
async function attachChild(tab, { sessionId, targetInfo }) {
  tab.sessions.add(sessionId);
  if (targetInfo.type === "iframe") {
    tab.frameSessions.set(targetInfo.targetId, sessionId);
  }
  tab.console.push("attached " + targetInfo.type + " " + targetInfo.url);
  try {
    await enableDomains(tab, sessionId, targetInfo.type === "iframe");
    await tab.dt.send("Target.setAutoAttach", { autoAttach: true, waitForDebuggerOnStart: true, flatten: true }, sessionId);
  } catch (err) {
    tab.console.push("could not observe " + targetInfo.type + ": " + err.message);
  }
  try {
    await tab.dt.send("Runtime.runIfWaitingForDebugger", {}, sessionId);
  } catch (err) {
    tab.console.push("could not start " + targetInfo.type + ": " + err.message);
  }
}

// observe records one DevTools event of tab.
function observe(tab, { method, params }) {
  switch (method) {
    case "Network.requestWillBeSent": {
      const entry = requestEntry(tab, params.requestId);
      entry.method = params.request.method;
      entry.url = params.request.url;
      entry.type = params.type || "";
      entry.page_headers = headerMap(params.request.headers);
      const auth = params.request.headers.Authorization || params.request.headers.authorization;
      if (tab.session === null && auth && auth.startsWith("Bearer ")) {
        tab.session = auth;
      }
      break;
    }
    case "Network.requestWillBeSentExtraInfo":
      requestEntry(tab, params.requestId).sent_headers = headerMap(params.headers);
      break;
    case "Network.responseReceived": {
      const entry = requestEntry(tab, params.requestId);
      entry.status = entry.status || params.response.status;
      break;
    }
    case "Network.responseReceivedExtraInfo":
      requestEntry(tab, params.requestId).status = params.statusCode;
      break;
    case "Network.loadingFailed": {
      const parts = [params.errorText];
      if (params.blockedReason) {
        parts.push("blocked: " + params.blockedReason);
      }
      if (params.corsErrorStatus) {
        parts.push("cors: " + params.corsErrorStatus.corsError);
      }
      if (params.canceled) {
        parts.push("canceled");
      }
      requestEntry(tab, params.requestId).failure = parts.join("; ");
      break;
    }
    case "Runtime.consoleAPICalled":
      tab.console.push(
        "console." + params.type + ": " + params.args.map((arg) => (arg.value !== undefined ? String(arg.value) : arg.description)).join(" ")
      );
      break;
    case "Runtime.exceptionThrown":
      tab.console.push(
        "exception: " + ((params.exceptionDetails.exception && params.exceptionDetails.exception.description) || params.exceptionDetails.text)
      );
      break;
    case "Log.entryAdded":
      tab.console.push("log " + params.entry.source + "/" + params.entry.level + ": " + params.entry.text);
      break;
    case "Page.frameNavigated":
      tab.frames.push(frameFacts(params.frame));
      break;
    case "Runtime.executionContextCreated":
      tab.contexts.push({
        origin: params.context.origin,
        frame_id: (params.context.auxData && params.context.auxData.frameId) || "",
        is_default: Boolean(params.context.auxData && params.context.auxData.isDefault),
      });
      break;
    default:
  }
}

// requestEntry returns tab's record of requestId, creating it in send order.
function requestEntry(tab, requestId) {
  let entry = tab.byId.get(requestId);
  if (!entry) {
    entry = { id: requestId, method: "", url: "", type: "", page_headers: {}, sent_headers: null, status: 0, failure: "" };
    tab.byId.set(requestId, entry);
    tab.requests.push(entry);
  }
  return entry;
}

// headerMap lowercases header names and hides credential values, keeping
// only their kind, so the observations never carry a session.
function headerMap(headers) {
  const out = {};
  for (const [name, value] of Object.entries(headers || {})) {
    const lower = name.toLowerCase();
    out[lower] = lower === "authorization" ? String(value).split(" ")[0] + " <redacted>" : String(value);
  }
  return out;
}

// frameFacts keeps the parts of a DevTools frame that say what it shows.
function frameFacts(frame) {
  return {
    id: frame.id,
    parent_id: frame.parentId || "",
    url: frame.url,
    security_origin: frame.securityOrigin || "",
    unreachable_url: frame.unreachableUrl || "",
  };
}

// navigate opens url in tab and resolves once the page's load event fired.
async function navigate(tab, url) {
  const loaded = tab.dt.waitForEvent(
    (message) => message.sessionId === tab.sessionId && message.method === "Page.loadEventFired",
    "loading " + url
  );
  const result = await tab.dt.send("Page.navigate", { url }, tab.sessionId);
  if (result.errorText) {
    throw new Error("navigating to " + url + " failed: " + result.errorText);
  }
  await loaded;
}

// reload reloads tab and resolves once the reloaded page's load event fired.
async function reload(tab) {
  const loaded = tab.dt.waitForEvent(
    (message) => message.sessionId === tab.sessionId && message.method === "Page.loadEventFired",
    "reloading the page"
  );
  await tab.dt.send("Page.reload", {}, tab.sessionId);
  await loaded;
}

// evaluate runs expression in tab's main frame, awaiting a promise result.
async function evaluate(tab, expression, sessionId = tab.sessionId, contextId) {
  const params = { expression, returnByValue: true, awaitPromise: true };
  if (contextId !== undefined) {
    params.contextId = contextId;
  }
  const { result, exceptionDetails } = await tab.dt.send("Runtime.evaluate", params, sessionId);
  if (exceptionDetails) {
    throw new Error(
      "page evaluation failed: " + ((exceptionDetails.exception && exceptionDetails.exception.description) || exceptionDetails.text)
    );
  }
  return result.value;
}

// until checks expression in tab until it is truthy and returns
// { value, timed_out }; it never throws, so a condition that never holds
// becomes an observation the Go test judges.
async function until(tab, expression) {
  const deadline = Date.now() + STEP_TIMEOUT_MS;
  for (;;) {
    try {
      const value = await evaluate(tab, expression);
      if (value) {
        return { value, timed_out: false };
      }
    } catch (err) {
      // The page may be between documents during a navigation.
    }
    if (Date.now() > deadline) {
      return { value: null, timed_out: true };
    }
    await sleep(POLL_MS);
  }
}

// settle waits until tab has at least min requests matching select and
// each of them failed, or was put on the wire with its network headers and,
// unless it is still open on purpose, answered. A request the browser refused
// to send only fails. Waiting for the count matters because the browser may
// report a request, such as a CORS preflight, after the page's fetch already
// settled. It resolves to whether that happened before the deadline.
async function settle(tab, select, min, stillOpen = () => false) {
  const deadline = Date.now() + STEP_TIMEOUT_MS;
  const settled = (r) => r.failure !== "" || (r.sent_headers !== null && (r.status !== 0 || stillOpen(r)));
  for (;;) {
    const matching = tab.requests.filter(select);
    if (matching.length >= min && matching.every(settled)) {
      return true;
    }
    if (Date.now() > deadline) {
      return false;
    }
    await sleep(POLL_MS);
  }
}

// violationsSince returns the securitypolicyviolation reports of tab's page
// after the first count, once there are more than count.
function violationsSince(tab, count) {
  return until(tab, "window.__surlaViolations.length > " + count + " ? window.__surlaViolations.slice(" + count + ") : null");
}

// frameView reads what one frame of tab shows through sessionId, in an
// isolated world so the frame's own policy and script cannot affect the
// reading.
async function frameView(tab, frameId, sessionId) {
  try {
    const { executionContextId } = await tab.dt.send(
      "Page.createIsolatedWorld",
      { frameId, worldName: "surla-browser-test" },
      sessionId
    );
    return await evaluate(
      tab,
      "({ href: location.href, origin: location.origin, has_views_root: document.getElementById('surla-checkpoint-root') !== null, text: (document.body ? document.body.innerText : '').slice(0, 300) })",
      sessionId,
      executionContextId
    );
  } catch (err) {
    return { error: err.message };
  }
}

// flattenFrames lists a DevTools frame tree depth first.
function flattenFrames(node, out = []) {
  out.push(frameFacts(node.frame));
  for (const child of node.childFrames || []) {
    flattenFrames(child, out);
  }
  return out;
}

// to returns a selector of requests with method to path on origin.
function to(origin, method, path) {
  return (r) => r.method === method && r.url.startsWith(origin + "/") && new URL(r.url).pathname === path;
}

// fetchOutcome is a page expression that fetches url with init and resolves
// to what the page could see: the answer's type and status, or the error.
function fetchOutcome(url, init) {
  return (
    "fetch(" +
    JSON.stringify(url) +
    ", " +
    init +
    ").then((r) => 'answered: ' + r.type + ' ' + r.status, (e) => 'refused: ' + e.name + ': ' + e.message)"
  );
}

// phoneView measures the current view after disclosures are open. Keyboard
// traversal uses real Tab events, including controls scrolled below the fold.
async function phoneView(tab, selector) {
  const keyboard = new Set();
  let first = null;
  for (let count = 0; count < 150; count++) {
    await tab.dt.send("Input.dispatchKeyEvent", { type: "keyDown", key: "Tab", code: "Tab", windowsVirtualKeyCode: 9 }, tab.sessionId);
    await tab.dt.send("Input.dispatchKeyEvent", { type: "keyUp", key: "Tab", code: "Tab", windowsVirtualKeyCode: 9 }, tab.sessionId);
    const active = await evaluate(tab, "document.activeElement?.id || (document.activeElement?.tagName === 'SUMMARY' ? 'SUMMARY:' + document.activeElement.textContent.trim() : document.activeElement?.tagName) || ''");
    if (first === null) first = active;
    else if (active === first) break;
    keyboard.add(active);
  }
  const measured = await evaluate(tab, "(" + function measure(selector) {
    const region = document.querySelector(selector);
    const visible = (node) => node.getClientRects().length > 0;
    const relevant = [document.getElementById("surla-team-attention"), region].filter(Boolean);
    const nodes = relevant.flatMap((node) => [node, ...node.querySelectorAll("*")]).filter(visible);
    return {
      found: region !== null,
      document_scroll_width: document.documentElement.scrollWidth,
      viewport_width: document.documentElement.clientWidth,
      viewport_height: window.innerHeight,
      attention_roles: Array.from(document.querySelectorAll("#surla-team-attention > div"), (node) => node.id.replace("surla-team-attention-", "")),
      role_states: Array.from(document.querySelectorAll("#surla-team-roles > li"), (node) => node.dataset.state),
      controls: nodes.filter((node) => node.matches("button") && !node.disabled).map((node) => node.id),
      overflowing: nodes.filter((node) => {
        const box = node.getBoundingClientRect();
        return box.left < -1 || box.right > document.documentElement.clientWidth + 1 || node.scrollWidth > node.clientWidth + 1;
      }).map((node) => node.id || node.tagName + "." + node.className),
      wrapped_long_text: Array.from(region?.querySelectorAll("p, li") || []).some((node) => {
        if (node.textContent.length < 50) return false;
        const range = document.createRange();
        range.selectNodeContents(node);
        return new Set(Array.from(range.getClientRects(), (box) => box.top)).size > 1;
      }),
      long_text: region?.innerText || "",
    };
  }.toString() + ")(" + JSON.stringify(selector) + ")");
  measured.keyboard_controls = Array.from(keyboard);
  return measured;
}

// phoneScenario reuses the same real page, server and DevTools connection.
async function phoneScenario(input, facts, views) {
  await views.dt.send("Emulation.setDeviceMetricsOverride", { width: 375, height: 800, deviceScaleFactor: 1, mobile: true }, views.sessionId);
  await evaluate(views, "document.getElementById('surla-nav-team').click(), true");
  await until(views, "document.querySelectorAll('#surla-team-roles > li[data-state=needs_you]').length === 3");
  await evaluate(views, "document.querySelectorAll('#surla-team-stage details').forEach((node) => { node.open = true; }), true");
  facts.phone = { team: await phoneView(views, "#surla-team-stage") };
  await evaluate(views, "document.getElementById('surla-nav-changes').click(), true");
  await until(views, "document.getElementById('surla-open-change-phone-build') !== null");
  await evaluate(views, "document.getElementById('surla-open-change-phone-build').click(), true");
  const blockedBuild = await until(views, "document.querySelector('#surla-build-test-writer[data-state=could_not_run]') !== null && document.getElementById('surla-build-again')?.disabled === false");
  if (blockedBuild.timed_out) throw new Error("the recorded build verdict did not render");
  await evaluate(views, "document.getElementById('surla-build-details').open = true");
  facts.phone.build = await phoneView(views, "#surla-build-section");
  await evaluate(views, "document.getElementById('surla-build-again').focus(), true");
  await views.dt.send("Input.dispatchKeyEvent", { type: "keyDown", key: "Enter", code: "Enter", windowsVirtualKeyCode: 13, text: "\r", unmodifiedText: "\r" }, views.sessionId);
  await views.dt.send("Input.dispatchKeyEvent", { type: "keyUp", key: "Enter", code: "Enter", windowsVirtualKeyCode: 13 }, views.sessionId);
  const question = await until(views, "document.getElementById('surla-build-question')?.contains(document.activeElement)");
  facts.phone.confirmation_opened = !question.timed_out;
  await views.dt.send("Input.dispatchKeyEvent", { type: "keyDown", key: "Escape", code: "Escape", windowsVirtualKeyCode: 27 }, views.sessionId);
  await views.dt.send("Input.dispatchKeyEvent", { type: "keyUp", key: "Escape", code: "Escape", windowsVirtualKeyCode: 27 }, views.sessionId);
  const cancelled = await until(views, "document.getElementById('surla-build-question') === null && document.activeElement?.id === 'surla-build-again'");
  facts.phone.confirmation_cancelled = !cancelled.timed_out;
  await evaluate(views, "document.getElementById('surla-nav-changes').click(), true");
  await until(views, "document.getElementById('surla-open-change-phone-findings') !== null");
  await evaluate(views, "document.getElementById('surla-open-change-phone-findings').click(), true");
  const findings = await until(views, "document.querySelectorAll('#surla-build-findings li').length === 4 && document.querySelector('#surla-build-findings summary') !== null");
  if (findings.timed_out) throw new Error("the user findings did not render");
  // Findings initially open. Close and reopen them with native keyboard
  // activation so the test observes the disclosure's actual initial state.
  await evaluate(views, "document.querySelector('#surla-build-findings summary').focus(), true");
  await views.dt.send("Input.dispatchKeyEvent", { type: "keyDown", key: "Enter", code: "Enter", windowsVirtualKeyCode: 13, text: "\r", unmodifiedText: "\r" }, views.sessionId);
  await views.dt.send("Input.dispatchKeyEvent", { type: "keyUp", key: "Enter", code: "Enter", windowsVirtualKeyCode: 13 }, views.sessionId);
  const collapsed = await until(views, "document.querySelector('#surla-build-findings details').open === false");
  if (collapsed.timed_out) throw new Error("Enter did not close the findings disclosure");
  await views.dt.send("Input.dispatchKeyEvent", { type: "keyDown", key: "Enter", code: "Enter", windowsVirtualKeyCode: 13, text: "\r", unmodifiedText: "\r" }, views.sessionId);
  await views.dt.send("Input.dispatchKeyEvent", { type: "keyUp", key: "Enter", code: "Enter", windowsVirtualKeyCode: 13 }, views.sessionId);
  const expanded = await until(views, "document.querySelector('#surla-build-findings details').open === true");
  facts.phone.findings_disclosure_opened = !expanded.timed_out;
  await evaluate(views, "document.getElementById('surla-build-details').open = true");
  facts.phone.findings = await phoneView(views, "#surla-build-section");
  facts.phone.rendered_findings = await evaluate(views, "Array.from(document.querySelectorAll('#surla-build-findings li'), (node) => node.textContent)");
  facts.phone.findings_wrapped = await evaluate(views, "Array.from(document.querySelectorAll('#surla-build-findings li')).every((node) => { const range = document.createRange(); range.selectNodeContents(node); return new Set(Array.from(range.getClientRects(), (box) => box.top)).size > 1; })");
  // Read only the controlled fixture responses through the existing DevTools
  // connection. The findings request is the page's own authenticated user read.
  const findingRequests = views.requests.filter(to(input.origin, "GET", "/changes/phone-findings/build/findings"));
  const buildRequests = views.requests.filter(to(input.origin, "GET", "/changes/phone-findings/build"));
  facts.phone.findings_request_count = findingRequests.length;
  const findingsRequest = findingRequests.at(-1);
  const buildRequest = buildRequests.at(-1);
  if (!findingsRequest || !buildRequest) throw new Error("the findings or build HTTP response is missing");
  facts.phone.findings_status = findingsRequest.status;
  facts.phone.build_status = buildRequest.status;
  const findingBody = await views.dt.send("Network.getResponseBody", { requestId: findingsRequest.id }, views.sessionId);
  const buildBody = await views.dt.send("Network.getResponseBody", { requestId: buildRequest.id }, views.sessionId);
  if (findingBody.base64Encoded || buildBody.base64Encoded) throw new Error("expected JSON response text");
  const responseFindings = JSON.parse(findingBody.body).findings;
  const buildTests = JSON.parse(buildBody.body).build.test_writer.tests;
  facts.phone.response_findings = responseFindings;
  facts.phone.build_finding_count = buildTests.finding_count;
  facts.phone.build_has_findings_field = Object.hasOwn(buildTests, "findings");
  facts.phone.build_has_finding_text = responseFindings.some((text) => buildBody.body.includes(text));
}

// takingScenario inspects both requested viewport classes in one browser run.
// The Go fixture supplies typed presentation facts; this proves no worker ran.
async function takingScenario(input, facts, views) {
  const takingState = input.taking_state === "replay" ? "merged" : input.taking_state === "dropped_stale" ? "dropped" : input.taking_state;
  const ready = await until(views, "document.querySelector('#surla-build-taking[data-state=" + takingState + "]') !== null");
  if (ready.timed_out) {
    const state = await evaluate(views, "({ state: document.getElementById('surla-checkpoint-root')?.dataset.state, text: document.body.innerText.slice(0, 6000) })");
    throw new Error("taking presentation did not load: " + JSON.stringify(state) + "; responses: " +
      JSON.stringify(views.requests.filter(to(input.origin, "GET", "/changes/taking-layout/build")).map((request) => ({ status: request.status, failure: request.failure }))));
  }
  facts.taking = [];
  for (const [name, width, height] of [["desktop", 1280, 900], ["mobile", 375, 800]]) {
    await views.dt.send("Emulation.setDeviceMetricsOverride", {
      width, height, deviceScaleFactor: 1, mobile: width < 600,
    }, views.sessionId);
    await evaluate(views, "new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)))");
    const trigger = input.taking_state === "passed" ? "surla-build-drop" : ["building", "replay"].includes(input.taking_state) ? "surla-build-again" : null;
    let opened = { timed_out: false };
    if (trigger !== null) {
      await evaluate(views, "document.getElementById('" + trigger + "').focus(), true");
      await views.dt.send("Input.dispatchKeyEvent", { type: "keyDown", key: "Enter", code: "Enter", windowsVirtualKeyCode: 13, text: "\r", unmodifiedText: "\r" }, views.sessionId);
      await views.dt.send("Input.dispatchKeyEvent", { type: "keyUp", key: "Enter", code: "Enter", windowsVirtualKeyCode: 13 }, views.sessionId);
      const question = input.taking_state === "passed" ? "surla-build-drop-question" : "surla-build-question";
      opened = await until(views, "document.getElementById('" + question + "')?.contains(document.activeElement)");
    }
    const measured = await evaluate(views, "(" + function measure() {
      const region = document.getElementById("surla-build-section");
      return {
        width: document.documentElement.clientWidth,
        document_width: document.documentElement.scrollWidth,
        commands: Array.from(region.querySelectorAll(".surla-build-merge pre"), (node) => node.textContent),
        commands_usable: Array.from(region.querySelectorAll(".surla-build-merge pre")).every((node) => {
          const style = getComputedStyle(node);
          node.focus();
          return style.whiteSpace === "pre" && ["auto", "scroll"].includes(style.overflowX) &&
            node.tabIndex === 0 && document.activeElement === node && node.getAttribute("role") === "region" && node.getAttribute("aria-label");
        }),
        explanation: document.getElementById("surla-build-confirm-explanation")?.textContent.includes("nothing confines them") === true ||
          (document.getElementById("surla-build-start-explanation")?.textContent.includes("nothing confines them") === true &&
          document.getElementById("surla-build-start")?.getAttribute("aria-describedby")?.split(/\s+/).includes("surla-build-start-explanation") === true),
        no_start: document.getElementById("surla-build-start") === null,
        enabling_step: document.getElementById("surla-build-enabling-step")?.textContent.includes("This agreement was drafted before a build was dropped") === true && document.getElementById("surla-build-again") === null,
        rewrite_warning: region.textContent.includes("commit where this build started") && region.textContent.includes("these merges bring that history back"),
        overflowing: [region, ...region.querySelectorAll("*")].filter((node) => {
          // Exact command lines intentionally scroll inside their focusable pre.
          if (!node.getClientRects().length || node.closest(".surla-build-merge pre")) return false;
          const box = node.getBoundingClientRect();
          return box.left < -1 || box.right > document.documentElement.clientWidth + 1 || node.scrollWidth > node.clientWidth + 1;
        }).map((node) => node.id || node.tagName),
      };
    }.toString() + ")()");
    measured.question_focused = !opened.timed_out;
    if (trigger !== null) await evaluate(views, "document.getElementById('" + (input.taking_state === "passed" ? "surla-build-drop-question" : "surla-build-question") + "').focus(), true");
    const capture = await views.dt.send("Page.captureScreenshot", { format: "png", captureBeyondViewport: true }, views.sessionId);
    measured.screenshot = require("node:path").join(input.capture_directory, input.taking_state + "-" + name + ".png");
    fs.writeFileSync(measured.screenshot, Buffer.from(capture.data, "base64"));
    if (trigger !== null) {
      await views.dt.send("Input.dispatchKeyEvent", { type: "keyDown", key: "Escape", code: "Escape", windowsVirtualKeyCode: 27 }, views.sessionId);
      await views.dt.send("Input.dispatchKeyEvent", { type: "keyUp", key: "Escape", code: "Escape", windowsVirtualKeyCode: 27 }, views.sessionId);
      const question = input.taking_state === "passed" ? "surla-build-drop-question" : "surla-build-question";
      const canceled = await until(views, "document.getElementById('" + question + "') === null && document.activeElement?.id === '" + trigger + "'");
      measured.question_cancelled = !canceled.timed_out;
    }
    facts.taking.push(measured);
  }
}

// approvalScenario captures the real served UI using explicit typed owner
// fixtures. The separate served-page tests exercise real approval persistence.
async function approvalScenario(input, facts, views) {
  await views.dt.send("Page.bringToFront", {}, views.sessionId);
  let ready = await until(views, "document.getElementById('surla-nav-cp5') !== null");
  if (ready.timed_out) throw new Error("approval navigation did not load");
  await evaluate(views, "document.getElementById('surla-nav-cp5').click(), true");
  ready = await until(views, "document.getElementById('surla-approval-stage') !== null && document.querySelector('.surla-flow-evidence tbody tr') !== null && !document.getElementById('surla-cp5-observation-status')");
  if (ready.timed_out) throw new Error("approval evidence did not settle: " + JSON.stringify({
    page: await evaluate(views, "({ hidden: document.hidden, visibility: document.visibilityState, disabled: document.getElementById('surla-cp5-approve')?.disabled, text: document.body.innerText.slice(0, 5000) })"),
    requests: views.requests.filter((r) => r.method === "GET").map((r) => ({ url: r.url, status: r.status, failure: r.failure })),
    console: views.console,
  }));
  facts.approval = [];
  const sizes = [["desktop", 1280, 900, 1], ["laptop", 1024, 768, 1], ["phone", 390, 844, 1]];
  if (input.approval_state === "current") sizes.push(["dark", 1280, 900, 1], ["reflow-200", 640, 450, 2]);
  for (const [name, width, height, scale] of sizes) {
    await views.dt.send("Emulation.setDeviceMetricsOverride", { width, height, deviceScaleFactor: scale, mobile: width < 600 }, views.sessionId);
    await views.dt.send("Emulation.setEmulatedMedia", { features: [{ name: "prefers-color-scheme", value: name === "dark" ? "dark" : "light" }] }, views.sessionId);
    await evaluate(views, "window.scrollTo(0, 0), new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)))");
    // Two paint frames can still fall inside the incumbent 150ms color
    // transition. Measure rest only after finite animations settle, bounded
    // by the same deadline as every other browser observation.
    const settled = await until(views, "document.getAnimations().every((animation) => animation.effect?.getTiming().iterations === Infinity || animation.playState !== 'running')");
    if (settled.timed_out) throw new Error("approval colors did not settle at " + name);
    const primaryAtRest = await evaluate(views, "(" + function measureRestingAction() {
      const action = document.getElementById("surla-cp5-approve");
      if (action === null) return true;
      document.activeElement?.blur();
      const probe = document.createElement("span");
      probe.style.backgroundColor = "var(--primary)";
      probe.style.color = "var(--primary-foreground)";
      probe.style.display = "none";
      document.body.append(probe);
      const expected = getComputedStyle(probe), actual = getComputedStyle(action);
      const matched = actual.backgroundColor === expected.backgroundColor && actual.color === expected.color;
      probe.remove();
      return matched;
    }.toString() + ")()");
    if (input.approval_state === "current") {
      const resting = await views.dt.send("Page.captureScreenshot", { format: "png", captureBeyondViewport: false }, views.sessionId);
      fs.writeFileSync(require("node:path").join(input.capture_directory, "current-" + name + "-resting.png"), Buffer.from(resting.data, "base64"));
    }
    let reached = false;
    if (input.approval_state === "current") {
      await evaluate(views, "document.querySelector('.surla-skip-link').focus(), true");
      for (let step = 0; step < 24; step++) {
        await views.dt.send("Input.dispatchKeyEvent", { type: "keyDown", key: "Tab", code: "Tab", windowsVirtualKeyCode: 9 }, views.sessionId);
        await views.dt.send("Input.dispatchKeyEvent", { type: "keyUp", key: "Tab", code: "Tab", windowsVirtualKeyCode: 9 }, views.sessionId);
        reached = await evaluate(views, "document.activeElement?.id === 'surla-cp5-approve'");
        if (reached) break;
      }
    }
    const measured = await evaluate(views, "(" + function measureApproval() {
      const stage = document.getElementById("surla-approval-stage");
      const action = document.getElementById("surla-cp5-approve");
      const footer = document.getElementById("surla-frame-next");
      const phone = matchMedia("(max-width: 720px)").matches;
      const style = action ? getComputedStyle(action) : null;
      const padding = parseFloat(getComputedStyle(document.body).paddingBottom);
      return {
        width: document.documentElement.clientWidth,
        document_width: document.documentElement.scrollWidth,
        heading: document.getElementById("surla-cp5-title").textContent,
        finished_flows: stage.querySelectorAll(".surla-flow-evidence tbody tr").length,
        draft_visible: stage.querySelector(".surla-approval-exclusions").textContent.includes("Draft, no required test"),
        approve_count: document.querySelectorAll("#surla-cp5-approve").length,
        approve_in_next: action !== null && footer.contains(action),
        approve_enabled: action !== null && !action.disabled,
        focus_visible: action !== null && document.activeElement === action && style.outlineStyle !== "none" && parseFloat(style.outlineWidth) >= 2,
        commands: Array.from(stage.querySelectorAll(".surla-approval-basis .surla-build-merge pre"), (node) => node.textContent),
        footer_reserved: !phone || getComputedStyle(footer).position !== "fixed" || padding >= footer.getBoundingClientRect().height,
        overflowing: [stage, ...stage.querySelectorAll("*"), footer].filter((node) => {
          if (!node.getClientRects().length || node.closest("pre") || getComputedStyle(node).clipPath === "inset(50%)") return false;
          const box = node.getBoundingClientRect();
          return box.left < -1 || box.right > document.documentElement.clientWidth + 1 || node.scrollWidth > node.clientWidth + 1;
        }).map((node) => node.id || node.tagName),
      };
    }.toString() + ")()");
    measured.name = name;
    measured.primary_at_rest = primaryAtRest;
    measured.keyboard_reached_action = reached;
    measured.flow_lines_readable = true;
    const capture = await views.dt.send("Page.captureScreenshot", { format: "png", captureBeyondViewport: true }, views.sessionId);
    measured.screenshot = require("node:path").join(input.capture_directory, input.approval_state + "-" + name + ".png");
    fs.writeFileSync(measured.screenshot, Buffer.from(capture.data, "base64"));
    if (name === "phone" && input.approval_state === "current") {
      for (let row = 0; row < measured.finished_flows; row++) {
        const readable = await evaluate(views, "(" + async function inspectFlow(index) {
          const row = document.querySelectorAll(".surla-flow-evidence tbody tr")[index];
          row.scrollIntoView({ block: "center" });
          await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)));
          const footer = document.getElementById("surla-frame-next").getBoundingClientRect();
          return Array.from(row.children).every((cell) => {
            const box = cell.getBoundingClientRect();
            return box.top >= 0 && box.bottom <= footer.top;
          });
        }.toString() + ")(" + row + ")");
        measured.flow_lines_readable &&= readable;
        const viewport = await views.dt.send("Page.captureScreenshot", { format: "png", captureBeyondViewport: false }, views.sessionId);
        fs.writeFileSync(require("node:path").join(input.capture_directory, "current-phone-flow-" + (row + 1) + ".png"), Buffer.from(viewport.data, "base64"));
      }
    }
    facts.approval.push(measured);
  }
}

// testReviewScenario measures the saved build's requirements beside its findings.
async function testReviewScenario(input, facts, views) {
  await views.dt.send("Page.bringToFront", {}, views.sessionId);
  const ready = await until(views, "document.getElementById('surla-nav-build') !== null && document.getElementById('surla-build-test-writer') !== null");
  if (ready.timed_out) throw new Error("test review did not load");
  await evaluate(views, "document.getElementById('surla-nav-build').click(), true");
  if (["needs_changes", "stale"].includes(input.test_review_state)) {
    const findings = await until(views, "document.querySelector('.surla-build-finding-text') !== null");
    if (findings.timed_out) throw new Error("test findings did not load");
  }
  facts.test_review = [];
  for (const [name, width, height, scale] of [["desktop", 1280, 900, 1], ["laptop", 1024, 768, 1],
      ["phone", 390, 844, 1], ["dark", 1280, 900, 1], ["reflow-200", 640, 450, 2]]) {
    await views.dt.send("Emulation.setDeviceMetricsOverride", { width, height, deviceScaleFactor: scale, mobile: width < 600 }, views.sessionId);
    await views.dt.send("Emulation.setEmulatedMedia", { features: [{ name: "prefers-color-scheme", value: name === "dark" ? "dark" : "light" }] }, views.sessionId);
    await evaluate(views, "window.scrollTo(0, 0), new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)))");
    const measured = await evaluate(views, "(" + function measureTestReview() {
      const next = document.getElementById("surla-frame-next");
      const region = document.querySelector(".surla-build-finding-text");
      const box = region?.getBoundingClientRect();
      const phone = matchMedia("(max-width: 720px)").matches;
      return { width: innerWidth, document_width: document.documentElement.scrollWidth,
        required_tests: document.querySelectorAll("#surla-build-required-tests > li").length,
        next: next?.innerText, next_controls: next?.querySelectorAll("button").length,
        next_inline: getComputedStyle(next).position !== "fixed",
        findings_visible: region !== null && box.width > 0,
        findings_bounded: region === null || box.height <= parseFloat(getComputedStyle(document.documentElement).fontSize) * 20 + 1,
        footer_reserved: !phone || getComputedStyle(next).position !== "fixed" || parseFloat(getComputedStyle(document.body).paddingBottom) >= next.getBoundingClientRect().height };
    }.toString() + ")()");
    measured.keyboard_reachable = true;
    if (measured.findings_visible) {
      await evaluate(views, "document.activeElement?.blur(), document.body.focus(), true");
      measured.keyboard_reachable = false;
      for (let attempt = 0; attempt < 100; attempt++) {
        await views.dt.send("Input.dispatchKeyEvent", { type: "keyDown", key: "Tab", code: "Tab", windowsVirtualKeyCode: 9 }, views.sessionId);
        await views.dt.send("Input.dispatchKeyEvent", { type: "keyUp", key: "Tab", code: "Tab", windowsVirtualKeyCode: 9 }, views.sessionId);
        if (await evaluate(views, "document.activeElement?.classList.contains('surla-build-finding-text')")) {
          measured.keyboard_reachable = true;
          break;
        }
      }
    }
    measured.name = name;
    await evaluate(views, "document.activeElement?.blur(), window.scrollTo(0, 0), new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)))");
    const capture = await views.dt.send("Page.captureScreenshot", { format: "png", captureBeyondViewport: true }, views.sessionId);
    measured.screenshot = require("node:path").join(input.capture_directory, input.test_review_state + "-" + name + ".png");
    fs.writeFileSync(measured.screenshot, Buffer.from(capture.data, "base64"));
    facts.test_review.push(measured);
  }
}

// main runs the scenario. Each section names the Go subtest that judges it.
async function main(input, facts) {
  const dt = await DevTools.connect(input.devtools);
  try {
    const version = await dt.send("Browser.getVersion");
    facts.browser = { product: version.product, protocol: version.protocolVersion, user_agent: version.userAgent };
    const server = (r) => r.url.startsWith(input.origin + "/");
    const longPoll = (r) => new URL(r.url).pathname === "/change-updates";

    // (a) Open the page with the link, as `surla` opens it.
    const views = await openTab(dt);
    await navigate(views, input.origin + "/views#link=" + input.link);
    const listed = await until(
      views,
      "(() => { const ids = Array.from(document.querySelectorAll('#surla-change-list [data-change-id]'), (item) => item.dataset.changeId); return ids.length >= " +
        input.change_ids.length +
        " ? ids : null; })()"
    );
    const { currentIndex, entries } = await dt.send("Page.getNavigationHistory", {}, views.sessionId);
    const { targetInfo } = await dt.send("Target.getTargetInfo", { targetId: views.targetId });
    facts.views = {
      change_ids: listed.value || [],
      change_list_timed_out: listed.timed_out,
      href: await evaluate(views, "location.href"),
      history_urls: entries.map((entry) => entry.url),
      current_history_url: entries[currentIndex].url,
      target_url: targetInfo.url,
    };

    if (input.test_review) {
      await testReviewScenario(input, facts, views);
      await dt.send("Target.closeTarget", { targetId: views.targetId });
      return;
    }
    if (input.approval) {
      await approvalScenario(input, facts, views);
      await dt.send("Target.closeTarget", { targetId: views.targetId });
      return;
    }
    if (input.taking) {
      await takingScenario(input, facts, views);
      await dt.send("Target.closeTarget", { targetId: views.targetId });
      return;
    }
    if (input.phone) {
      await phoneScenario(input, facts, views);
      return;
    }

    // (b) Every request the page sent so far, with the headers the browser put on the wire.
    facts.views.requests_settled =
      (await settle(views, to(input.origin, "POST", "/project/browser-session"), 1)) &&
      (await settle(views, to(input.origin, "GET", "/changes"), 1)) &&
      (await settle(views, to(input.origin, "GET", "/change-updates"), 1, longPoll));
    facts.views.requests = views.requests.filter(server).map((r) => ({ ...r }));

    // (c) What the browser kept for the page.
    facts.views.storage = await evaluate(
      views,
      "indexedDB.databases().then((dbs) => ({ local_storage: localStorage.length, session_storage: sessionStorage.length, cookie: document.cookie, indexed_db: dbs.length }))"
    );
    facts.views.storage.browser_cookies = (await dt.send("Storage.getCookies", {})).cookies.length;

    // (d) The page's policy: its own load, an injected inline script, a fetch to another origin.
    facts.views.load_violations = await evaluate(views, "window.__surlaViolations.slice()");
    let count = facts.views.load_violations.length;
    await evaluate(
      views,
      "(() => { const s = document.createElement('script'); s.textContent = 'window.__surlaInjectedRan = true;'; document.body.appendChild(s); return true; })()"
    );
    const injected = await violationsSince(views, count);
    facts.views.injected_script_ran = await evaluate(views, "window.__surlaInjectedRan === true");
    facts.views.injected_violations = injected.value || [];
    count += facts.views.injected_violations.length;
    const probe = input.same_site_origin + "/csp-probe";
    facts.views.foreign_fetch = await evaluate(views, fetchOutcome(probe, "{ mode: 'no-cors' }"));
    const blocked = await violationsSince(views, count);
    facts.views.foreign_fetch_violations = blocked.value || [];
    await settle(views, (r) => r.url === probe, 0);
    facts.views.foreign_fetch_requests = views.requests.filter((r) => r.url === probe).map((r) => ({ ...r }));

    // (e) A page on another origin frames the views page.
    const framing = await openTab(dt);
    await navigate(framing, input.same_site_origin + "/frame");
    await settle(framing, to(input.origin, "GET", "/views"), 1);
    // A frame in another process belongs to its own DevTools session, so
    // every session's frame tree is read, each frame once.
    const frames = [];
    for (const sessionId of [framing.sessionId, ...framing.frameSessions.values()]) {
      const { frameTree } = await dt.send("Page.getFrameTree", {}, sessionId);
      for (const frame of flattenFrames(frameTree)) {
        if (!frames.some((known) => known.id === frame.id)) {
          frame.view = await frameView(framing, frame.id, sessionId);
          frames.push(frame);
        }
      }
    }
    facts.framing = {
      frames,
      navigated: framing.frames,
      contexts: framing.contexts,
      requests: framing.requests.filter(server),
      console: framing.console,
    };

    // (f) Pages on other origins read the change list and confirm a change,
    // each once without credentials, as an image or a form could, and once
    // with the views page's own session, which needs a CORS preflight.
    const session = views.session || "Bearer missing";
    const confirmBody = JSON.stringify(input.confirm_body);
    facts.other_pages = [];
    for (const origin of [input.same_site_origin, input.cross_site_origin]) {
      const other = await openTab(dt);
      await navigate(other, origin + "/blank");
      const plain = await evaluate(other, fetchOutcome(input.origin + "/changes", "{ mode: 'no-cors' }"));
      const withSession = await evaluate(
        other,
        fetchOutcome(input.origin + "/changes", "{ headers: { Authorization: " + JSON.stringify(session) + " } }")
      );
      const confirmPlain = await evaluate(
        other,
        fetchOutcome(input.origin + "/changes/confirm", "{ method: 'POST', mode: 'no-cors', headers: { 'Content-Type': 'text/plain' }, body: " + confirmBody + " }")
      );
      const confirmWithSession = await evaluate(
        other,
        fetchOutcome(
          input.origin + "/changes/confirm",
          "{ method: 'POST', headers: { Authorization: " + JSON.stringify(session) + ", 'Content-Type': 'application/json' }, body: " + confirmBody + " }"
        )
      );
      // Two reads and two confirms, and a preflight for each one that carries the session.
      const settled =
        (await settle(other, to(input.origin, "GET", "/changes"), 2)) &&
        (await settle(other, to(input.origin, "OPTIONS", "/changes"), 1)) &&
        (await settle(other, to(input.origin, "POST", "/changes/confirm"), 2)) &&
        (await settle(other, to(input.origin, "OPTIONS", "/changes/confirm"), 1));
      facts.other_pages.push({
        origin,
        without_credentials: plain,
        with_session: withSession,
        confirm_without_credentials: confirmPlain,
        confirm_with_session: confirmWithSession,
        requests_settled: settled,
        requests: other.requests.filter(server),
        console: other.console,
      });
    }
    facts.views.session_seen = views.session !== null;

    // (h) Open the change saved with a Story, then its Story stage, and
    // measure the long flow's row of steps once the page has made it a tab
    // stop, which it does when it measures that the row overflows.
    const beforeStory = await evaluate(views, "window.__surlaViolations.length");
    await evaluate(views, "document.getElementById(" + JSON.stringify("surla-open-change-" + input.story_change_id) + ").click(), true");
    await until(views, "(document.getElementById('surla-cp1-editor') || { dataset: {} }).dataset.changeId === " + JSON.stringify(input.story_change_id));
    await evaluate(views, "document.getElementById('surla-nav-story').click(), true");
    const row =
      "document.querySelector('#surla-story-change .surla-story-flow[data-flow-key=" +
      JSON.stringify(input.story_flow) +
      "] .surla-story-steps')";
    const shown = await until(views, row + " !== null");
    const tabStop = await until(views, "(" + row + " || {}).tabIndex === 0");
    facts.story = shown.timed_out
      ? { found: false }
      : await evaluate(
          views,
          "(() => { const row = " +
            row +
            "; const start = row.scrollLeft; row.scrollLeft = row.scrollWidth; const scrolled = row.scrollLeft; row.scrollLeft = start;" +
            " return { found: true, steps: row.querySelectorAll('.surla-story-step').length, tab_index: row.tabIndex," +
            " overflow_x: getComputedStyle(row).overflowX, row_scroll_width: row.scrollWidth, row_client_width: row.clientWidth," +
            " scrolled_to: scrolled, row_right: row.getBoundingClientRect().right," +
            " stage_right: document.getElementById('surla-story-stage').getBoundingClientRect().right," +
            " document_scroll_width: document.documentElement.scrollWidth, viewport_width: document.documentElement.clientWidth }; })()"
        );
    facts.story.tab_stop_timed_out = tabStop.timed_out;
    facts.story.violations = await evaluate(views, "window.__surlaViolations.slice(" + beforeStory + ")");

    // (i) Open the Live target view at a phone's width and measure which
    // model form fields are displayed and whether anything widens the page.
    await withDeadline(
      views.dt.send("Emulation.setDeviceMetricsOverride", { width: 375, height: 800, deviceScaleFactor: 1, mobile: true }, views.sessionId),
      "narrow the viewport"
    );
    await evaluate(views, "document.getElementById('surla-nav-live').click(), true");
    const liveShown = await until(
      views,
      "document.getElementById('surla-model-form') !== null && (document.getElementById('surla-live-target') || { dataset: {} }).dataset.state !== 'unread'"
    );
    facts.live = liveShown.timed_out
      ? { found: false }
      : await evaluate(
          views,
          "(() => ({ found: true, live_state: document.getElementById('surla-live-target').dataset.state," +
            " direct_display: getComputedStyle(document.getElementById('surla-model-direct-fields')).display," +
            " gateway_display: getComputedStyle(document.getElementById('surla-model-gateway-fields')).display," +
            " document_scroll_width: document.documentElement.scrollWidth, viewport_width: document.documentElement.clientWidth }))()"
        );
    await withDeadline(views.dt.send("Emulation.clearDeviceMetricsOverride", {}, views.sessionId), "restore the viewport");

    // (g) Reload the views page.
    const beforeReload = views.requests.length;
    await reload(views);
    const signedOut = await until(views, "(document.getElementById('surla-signed-out') || {}).textContent");
    const shownAfterReload = views.requests.slice(beforeReload).map((r) => r.id);
    const probeURL = input.origin + "/change-updates?since=after-reload";
    const status = await evaluate(views, "fetch(" + JSON.stringify(probeURL) + ").then((r) => r.status, (e) => 'refused: ' + e.message)");
    await settle(views, (r) => r.url === probeURL, 1);
    facts.reload = {
      signed_out_text: signedOut.value || "",
      signed_out_timed_out: signedOut.timed_out,
      href: await evaluate(views, "location.href"),
      page_requests: views.requests.filter((r) => shownAfterReload.includes(r.id)),
      data_request_status: status,
      data_request: views.requests.filter((r) => r.url === probeURL),
    };
    facts.views.console = views.console;
  } finally {
    dt.close();
  }
}

const facts = {};
main(JSON.parse(fs.readFileSync(process.argv[2], "utf8")), facts).then(
  () => process.stdout.write(JSON.stringify(facts), () => process.exit(0)),
  (err) => {
    console.error((err && err.stack) || err);
    process.stdout.write(JSON.stringify(facts), () => process.exit(1));
  }
);
