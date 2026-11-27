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
    Object.defineProperty(window, "__highbayViolations", { value: seen });
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
  return until(tab, "window.__highbayViolations.length > " + count + " ? window.__highbayViolations.slice(" + count + ") : null");
}

// frameView reads what one frame of tab shows through sessionId, in an
// isolated world so the frame's own policy and script cannot affect the
// reading.
async function frameView(tab, frameId, sessionId) {
  try {
    const { executionContextId } = await tab.dt.send(
      "Page.createIsolatedWorld",
      { frameId, worldName: "highbay-browser-test" },
      sessionId
    );
    return await evaluate(
      tab,
      "({ href: location.href, origin: location.origin, has_views_root: document.getElementById('highbay-checkpoint-root') !== null, text: (document.body ? document.body.innerText : '').slice(0, 300) })",
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
    const relevant = [document.getElementById("highbay-team-attention"), region].filter(Boolean);
    const nodes = relevant.flatMap((node) => [node, ...node.querySelectorAll("*")]).filter(visible);
    return {
      found: region !== null,
      document_scroll_width: document.documentElement.scrollWidth,
      viewport_width: document.documentElement.clientWidth,
      viewport_height: window.innerHeight,
      attention_roles: Array.from(document.querySelectorAll("#highbay-team-attention > div"), (node) => node.id.replace("highbay-team-attention-", "")),
      role_states: Array.from(document.querySelectorAll("#highbay-team-roles > li"), (node) => node.dataset.state),
      controls: nodes.filter((node) => node.matches("button") && !node.disabled).map((node) => node.id),
      overflowing: nodes.filter((node) => {
        // Fully clipped accessibility text can exceed its own box without widening the page.
        if (getComputedStyle(node).clipPath === "inset(50%)") return false;
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
  await evaluate(views, "document.getElementById('highbay-nav-team').click(), true");
  await until(views, "document.querySelectorAll('#highbay-team-roles > li[data-state=needs_you]').length === 3");
  await evaluate(views, "document.querySelectorAll('#highbay-team-stage details').forEach((node) => { node.open = true; }), true");
  facts.phone = { team: await phoneView(views, "#highbay-team-stage") };
  await evaluate(views, "document.getElementById('highbay-nav-changes').click(), true");
  await until(views, "document.getElementById('highbay-open-change-phone-build') !== null");
  await evaluate(views, "document.getElementById('highbay-open-change-phone-build').click(), true");
  const blockedBuild = await until(views, "document.querySelector('#highbay-build-test-writer[data-state=could_not_run]') !== null && document.getElementById('highbay-build-again')?.disabled === false");
  if (blockedBuild.timed_out) throw new Error("the recorded build verdict did not render");
  await evaluate(views, "document.getElementById('highbay-build-details').open = true");
  facts.phone.build = await phoneView(views, "#highbay-build-section");
  await evaluate(views, "document.getElementById('highbay-build-again').focus(), true");
  await views.dt.send("Input.dispatchKeyEvent", { type: "keyDown", key: "Enter", code: "Enter", windowsVirtualKeyCode: 13, text: "\r", unmodifiedText: "\r" }, views.sessionId);
  await views.dt.send("Input.dispatchKeyEvent", { type: "keyUp", key: "Enter", code: "Enter", windowsVirtualKeyCode: 13 }, views.sessionId);
  const question = await until(views, "document.getElementById('highbay-build-question')?.contains(document.activeElement)");
  facts.phone.confirmation_opened = !question.timed_out;
  await views.dt.send("Input.dispatchKeyEvent", { type: "keyDown", key: "Escape", code: "Escape", windowsVirtualKeyCode: 27 }, views.sessionId);
  await views.dt.send("Input.dispatchKeyEvent", { type: "keyUp", key: "Escape", code: "Escape", windowsVirtualKeyCode: 27 }, views.sessionId);
  const cancelled = await until(views, "document.getElementById('highbay-build-question') === null && document.activeElement?.id === 'highbay-build-again'");
  facts.phone.confirmation_cancelled = !cancelled.timed_out;
  await evaluate(views, "document.getElementById('highbay-nav-changes').click(), true");
  await until(views, "document.getElementById('highbay-open-change-phone-findings') !== null");
  await evaluate(views, "document.getElementById('highbay-open-change-phone-findings').click(), true");
  const findings = await until(views, "document.querySelectorAll('#highbay-build-findings li').length === 4 && document.querySelector('#highbay-build-findings summary') !== null");
  if (findings.timed_out) throw new Error("the user findings did not render");
  // Findings initially open. Close and reopen them with native keyboard
  // activation so the test observes the disclosure's actual initial state.
  await evaluate(views, "document.querySelector('#highbay-build-findings summary').focus(), true");
  await views.dt.send("Input.dispatchKeyEvent", { type: "keyDown", key: "Enter", code: "Enter", windowsVirtualKeyCode: 13, text: "\r", unmodifiedText: "\r" }, views.sessionId);
  await views.dt.send("Input.dispatchKeyEvent", { type: "keyUp", key: "Enter", code: "Enter", windowsVirtualKeyCode: 13 }, views.sessionId);
  const collapsed = await until(views, "document.querySelector('#highbay-build-findings details').open === false");
  if (collapsed.timed_out) throw new Error("Enter did not close the findings disclosure");
  await views.dt.send("Input.dispatchKeyEvent", { type: "keyDown", key: "Enter", code: "Enter", windowsVirtualKeyCode: 13, text: "\r", unmodifiedText: "\r" }, views.sessionId);
  await views.dt.send("Input.dispatchKeyEvent", { type: "keyUp", key: "Enter", code: "Enter", windowsVirtualKeyCode: 13 }, views.sessionId);
  const expanded = await until(views, "document.querySelector('#highbay-build-findings details').open === true");
  facts.phone.findings_disclosure_opened = !expanded.timed_out;
  await evaluate(views, "document.getElementById('highbay-build-details').open = true");
  facts.phone.findings = await phoneView(views, "#highbay-build-section");
  facts.phone.rendered_findings = await evaluate(views, "Array.from(document.querySelectorAll('#highbay-build-findings li'), (node) => node.textContent)");
  facts.phone.findings_wrapped = await evaluate(views, "Array.from(document.querySelectorAll('#highbay-build-findings li')).every((node) => { const range = document.createRange(); range.selectNodeContents(node); return new Set(Array.from(range.getClientRects(), (box) => box.top)).size > 1; })");
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
  const ready = await until(views, "document.querySelector('#highbay-build-taking[data-state=" + takingState + "]') !== null");
  if (ready.timed_out) {
    const state = await evaluate(views, "({ state: document.getElementById('highbay-checkpoint-root')?.dataset.state, text: document.body.innerText.slice(0, 6000) })");
    throw new Error("taking presentation did not load: " + JSON.stringify(state) + "; responses: " +
      JSON.stringify(views.requests.filter(to(input.origin, "GET", "/changes/taking-layout/build")).map((request) => ({ status: request.status, failure: request.failure }))));
  }
  facts.taking = [];
  for (const [name, width, height] of [["desktop", 1280, 900], ["mobile", 375, 800]]) {
    await views.dt.send("Emulation.setDeviceMetricsOverride", {
      width, height, deviceScaleFactor: 1, mobile: width < 600,
    }, views.sessionId);
    await evaluate(views, "new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)))");
    const trigger = input.taking_state === "passed" ? "highbay-build-drop" : ["building", "replay"].includes(input.taking_state) ? "highbay-build-again" : null;
    let opened = { timed_out: false };
    if (trigger !== null) {
      await evaluate(views, "document.getElementById('" + trigger + "').focus(), true");
      await views.dt.send("Input.dispatchKeyEvent", { type: "keyDown", key: "Enter", code: "Enter", windowsVirtualKeyCode: 13, text: "\r", unmodifiedText: "\r" }, views.sessionId);
      await views.dt.send("Input.dispatchKeyEvent", { type: "keyUp", key: "Enter", code: "Enter", windowsVirtualKeyCode: 13 }, views.sessionId);
      const question = input.taking_state === "passed" ? "highbay-build-drop-question" : "highbay-build-question";
      opened = await until(views, "document.getElementById('" + question + "')?.contains(document.activeElement)");
    }
    const measured = await evaluate(views, "(" + function measure() {
      const region = document.getElementById("highbay-build-section");
      return {
        width: window.innerWidth,
        document_width: document.documentElement.scrollWidth,
        commands: Array.from(region.querySelectorAll(".highbay-build-merge pre"), (node) => node.textContent),
        commands_usable: Array.from(region.querySelectorAll(".highbay-build-merge pre")).every((node) => {
          const style = getComputedStyle(node);
          node.focus();
          return style.whiteSpace === "pre" && ["auto", "scroll"].includes(style.overflowX) &&
            node.tabIndex === 0 && document.activeElement === node && node.getAttribute("role") === "region" && node.getAttribute("aria-label");
        }),
        explanation: document.getElementById("highbay-build-confirm-explanation")?.textContent.includes("nothing confines them") === true ||
          (document.getElementById("highbay-build-start-explanation")?.textContent.includes("nothing confines them") === true &&
          document.getElementById("highbay-build-start")?.getAttribute("aria-describedby")?.split(/\s+/).includes("highbay-build-start-explanation") === true),
        no_start: document.getElementById("highbay-build-start") === null,
        enabling_step: document.getElementById("highbay-build-enabling-step")?.textContent.includes("This agreement was drafted before a build was dropped") === true && document.getElementById("highbay-build-again") === null,
        rewrite_warning: region.textContent.includes("commit where this build started") && region.textContent.includes("these merges bring that history back"),
        overflowing: [document.documentElement, region, ...region.querySelectorAll("*")].filter((node) => {
          if (node === document.documentElement) return node.scrollWidth > node.clientWidth;
          // Exact command lines intentionally scroll inside their focusable pre.
          if (!node.getClientRects().length || node.closest(".highbay-build-merge pre")) return false;
          // Measure visible descendants here. The document check still catches page widening.
          if (getComputedStyle(node).clipPath === "inset(50%)") return false;
          const box = node.getBoundingClientRect();
          return box.left < -1 || box.right > document.documentElement.clientWidth + 1 || node.scrollWidth > node.clientWidth + 1;
        }).map((node) => node.id || node.tagName),
      };
    }.toString() + ")()");
    measured.question_focused = !opened.timed_out;
    if (trigger !== null) await evaluate(views, "document.getElementById('" + (input.taking_state === "passed" ? "highbay-build-drop-question" : "highbay-build-question") + "').focus(), true");
    const capture = await views.dt.send("Page.captureScreenshot", { format: "png", captureBeyondViewport: true }, views.sessionId);
    measured.screenshot = require("node:path").join(input.capture_directory, input.taking_state + "-" + name + ".png");
    fs.writeFileSync(measured.screenshot, Buffer.from(capture.data, "base64"));
    if (trigger !== null) {
      await views.dt.send("Input.dispatchKeyEvent", { type: "keyDown", key: "Escape", code: "Escape", windowsVirtualKeyCode: 27 }, views.sessionId);
      await views.dt.send("Input.dispatchKeyEvent", { type: "keyUp", key: "Escape", code: "Escape", windowsVirtualKeyCode: 27 }, views.sessionId);
      const question = input.taking_state === "passed" ? "highbay-build-drop-question" : "highbay-build-question";
      const canceled = await until(views, "document.getElementById('" + question + "') === null && document.activeElement?.id === '" + trigger + "'");
      measured.question_cancelled = !canceled.timed_out;
    }
    facts.taking.push(measured);
  }
}

// experienceScenario exercises the changed inline worker and approved-merge
// controls through the existing isolated browser and real served page.
async function experienceScenario(input, facts, views) {
  const variant = input.experience_view;
  const [width, height] = variant === "phone" ? [375, 800] : variant === "reflow" ? [640, 450] : [1280, 900];
  await views.dt.send("Emulation.setDeviceMetricsOverride", { width, height, deviceScaleFactor: 1, mobile: width < 600 }, views.sessionId);
  await views.dt.send("Emulation.setEmulatedMedia", { features: [{ name: "prefers-color-scheme", value: variant === "dark" ? "dark" : "light" }] }, views.sessionId);
  await until(views, "document.getElementById('highbay-nav-build') !== null");
  const merge = ["merge", "build_merge", "merge_conflict", "dirty_worktree"].includes(input.experience);
  const mergeId = input.experience === "build_merge" ? "highbay-build-merge" : "highbay-cp5-merge";
  await evaluate(views, "document.getElementById('highbay-nav-" + (merge && input.experience !== "build_merge" ? "cp5" : "build") + "').click(), true");
  const root = merge && input.experience !== "build_merge" ? "highbay-approval-stage" : "highbay-build-section";
  const prefix = "highbay-build-agents";
  if (input.experience === "build_merge") {
    const available = await until(views, "Array.from(document.querySelectorAll('#highbay-frame-next button')).some((node) => node.textContent === 'Check current approval') || document.getElementById('highbay-build-merge')?.disabled === false");
    if (available.timed_out) throw new Error("Build did not offer its exact approved merge state");
    await evaluate(views, "Array.from(document.querySelectorAll('#highbay-frame-next button')).find((node) => node.textContent === 'Check current approval')?.click(), true");
  }
  let ready = await until(views, merge
    ? "document.getElementById('" + mergeId + "')?.disabled === false"
    : "document.getElementById('highbay-build-agents-builder-client')?.value.length > 0");
  if (ready.timed_out) throw new Error("experience did not load: " + await evaluate(views, "document.body.innerText.slice(0, 6500)"));
  const measured = { name: variant, background_gated: true, draft_preserved: true, notice_dismissed: true, client_changed: true,
    initial_clients: [], manual_secondary: true, merge_count: 0, action_text: "", action_recovered: true };
  measured.no_auto_launch = !views.requests.some((r) => r.method === "POST" && /\/(build|agents|merge)$/.test(new URL(r.url).pathname));
  if (!merge) {
    measured.initial_clients = await evaluate(views, "Array.from(document.querySelectorAll('#" + prefix + " select[id$=-client]'), (node) => JSON.parse(node.value)[0])");
    if (["picker", "builder_only"].includes(input.experience)) {
      await evaluate(views, "(" + function changeClient() {
        const client = document.getElementById("highbay-build-agents-builder-client");
        client.value = JSON.stringify(["claude", "/fixture/claude"]);
        client.dispatchEvent(new Event("change", { bubbles: true }));
      }.toString() + ")()");
      ready = await until(views, "document.getElementById('highbay-build-agents-builder-client').value === JSON.stringify(['claude', '/fixture/claude'])");
      measured.client_changed = !ready.timed_out;
      await evaluate(views, "(" + function restoreClient() {
        const client = document.getElementById("highbay-build-agents-builder-client");
        client.value = JSON.stringify(["codex", "/fixture/codex"]);
        client.dispatchEvent(new Event("change", { bubbles: true }));
      }.toString() + ")()");
      await until(views, "document.getElementById('highbay-build-agents-builder-client').value === JSON.stringify(['codex', '/fixture/codex'])");
    }
    await evaluate(views, "(" + function setDraft() {
      const model = document.getElementById("highbay-build-agents-builder-model");
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value").set.call(model, "custom/browser-model");
      model.dispatchEvent(new Event("input", { bubbles: true }));
      const effort = document.getElementById("highbay-build-agents-builder-thinking");
      if (effort) { effort.value = "high"; effort.dispatchEvent(new Event("change", { bubbles: true })); }
    }.toString() + ")()");
    await evaluate(views, "document.getElementById('highbay-build-agents-refresh').click(), true");
    await until(views, "document.getElementById('highbay-build-agents-refresh')?.disabled === false");
    await evaluate(views, "document.getElementById('highbay-nav-team').click(), true");
    ready = await until(views, "document.getElementById('highbay-team-agents-builder-model') !== null");
    if (ready.timed_out) throw new Error("Team picker did not load");
    measured.draft_preserved = await evaluate(views, "document.getElementById('highbay-team-agents-builder-model').value === 'custom/browser-model'");
    if (input.experience === "single") {
      await evaluate(views, "document.getElementById('highbay-team-agents-notice-single_agent_family-dismiss').click(), true");
      ready = await until(views, "document.getElementById('highbay-team-agents-notice-single_agent_family') === null");
      measured.notice_dismissed = !ready.timed_out;
      await evaluate(views, "document.getElementById('highbay-team-refresh').click(), true");
      await until(views, "document.getElementById('highbay-team-refresh')?.disabled === false");
      measured.notice_dismissed &&= await evaluate(views, "document.getElementById('highbay-team-agents-notice-single_agent_family') === null");
    }
    measured.no_auto_launch &&= !views.requests.some((r) => r.method === "POST" && /\/(build|agents|merge)$/.test(new URL(r.url).pathname));
    await evaluate(views, "document.getElementById('highbay-team-start').click(), true");
    ready = await until(views, "document.getElementById('highbay-team-start-receipt') !== null");
    if (ready.timed_out) throw new Error("explicit Team launch did not return a receipt");
    await evaluate(views, "document.getElementById('highbay-nav-build').click(), true");
    await until(views, "document.getElementById('highbay-build-agents-builder-model') !== null");
    measured.draft_preserved &&= await evaluate(views, "document.getElementById('highbay-build-agents-builder-model').value === 'custom/browser-model'");
  } else {
    measured.merge_count = await evaluate(views, "document.querySelectorAll('#" + mergeId + "').length");
    measured.manual_secondary = await evaluate(views, "Array.from(document.querySelectorAll('#" + root + " details')).some((node) => node.querySelector('summary')?.textContent === 'Manual merge instructions' && !node.open)");
  }
  // Native Tab events prove that the controls remain in the keyboard order and
  // receive the stylesheet's visible focus outline at every tested width.
  const keyboard = [];
  let focusVisible = false;
  await evaluate(views, "document.activeElement?.blur(), window.scrollTo(0, 0), true");
  for (let count = 0; count < 120; count++) {
    await views.dt.send("Input.dispatchKeyEvent", { type: "keyDown", key: "Tab", code: "Tab", windowsVirtualKeyCode: 9 }, views.sessionId);
    await views.dt.send("Input.dispatchKeyEvent", { type: "keyUp", key: "Tab", code: "Tab", windowsVirtualKeyCode: 9 }, views.sessionId);
    const active = await evaluate(views, "({ id: document.activeElement?.id ?? '', visible: (() => { const style = getComputedStyle(document.activeElement); return style.outlineStyle !== 'none' && parseFloat(style.outlineWidth) >= 2; })() })");
    if (!keyboard.includes(active.id)) keyboard.push(active.id);
    if (active.id === (merge ? mergeId : "highbay-build-agents-builder-client")) focusVisible = active.visible;
    if (active.id === (merge ? mergeId : "highbay-build-again")) break;
  }
  measured.keyboard_controls = keyboard;
  measured.focus_visible = focusVisible;
  Object.assign(measured, await evaluate(views, "(" + function measure(root) {
    const region = document.getElementById(root);
    const controls = Array.from(region.querySelectorAll("button, input, select, summary")).filter((node) => node.getClientRects().length);
    return { width: document.documentElement.clientWidth, document_width: document.documentElement.scrollWidth,
      controls_fit: controls.every((node) => { const box = node.getBoundingClientRect(); return box.width > 0 && box.left >= -1 && box.right <= document.documentElement.clientWidth + 1; }) };
  }.toString() + ")(" + JSON.stringify(root) + ")"));
  await evaluate(views, "document.getElementById('" + (merge ? mergeId : "highbay-build-agents") + "').scrollIntoView({block: 'center'}), new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)))");
  measured.screenshot = require("node:path").join(input.capture_directory, input.experience + "-" + variant + ".png");
  const capture = await views.dt.send("Page.captureScreenshot", { format: "png", captureBeyondViewport: false }, views.sessionId);
  fs.writeFileSync(measured.screenshot, Buffer.from(capture.data, "base64"));
  if (merge) {
    await evaluate(views, "Array.from(document.querySelectorAll('#" + root + " details > summary')).find((node) => node.textContent === 'Manual merge instructions').focus(), true");
    await views.dt.send("Input.dispatchKeyEvent", { type: "keyDown", key: "Enter", code: "Enter", windowsVirtualKeyCode: 13, text: "\r", unmodifiedText: "\r" }, views.sessionId);
    await views.dt.send("Input.dispatchKeyEvent", { type: "keyUp", key: "Enter", code: "Enter", windowsVirtualKeyCode: 13 }, views.sessionId);
    measured.manual_secondary &&= await evaluate(views, "(" + function inspectManual(root) {
      const details = Array.from(document.querySelectorAll("#" + root + " details")).find((node) => node.querySelector("summary")?.textContent === "Manual merge instructions");
      const commands = Array.from(details.querySelectorAll("pre"));
      return details.open && commands.length === 2 && commands.every((node) => {
        node.focus();
        const style = getComputedStyle(node);
        return node.tabIndex === 0 && document.activeElement === node && style.whiteSpace === "pre" && ["auto", "scroll"].includes(style.overflowX);
      }) && commands[0].textContent === "git merge " + "1".repeat(40) && commands[1].textContent === "git merge " + "2".repeat(40);
    }.toString() + ")(" + JSON.stringify(root) + ")");
    await evaluate(views, "Array.from(document.querySelectorAll('#" + root + " details')).find((node) => node.querySelector('summary')?.textContent === 'Manual merge instructions').open = false, true");
    await evaluate(views, "document.getElementById('" + mergeId + "').click(), true");
    const feedback = input.experience === "build_merge" ? "highbay-build-merge-feedback" : "highbay-cp5-merge-feedback";
    const refused = !["merge", "build_merge"].includes(input.experience);
    ready = await until(views, !refused
      ? "document.getElementById('" + mergeId + "') === null && document.body.innerText.includes('feature/orders') && document.body.innerText.includes('3333333') && /(?:approved build|verified commits) (?:is|are) merged/.test(document.body.innerText)"
      : "document.getElementById('" + feedback + "-error') !== null");
    measured.action_recovered = !ready.timed_out;
    measured.action_text = await evaluate(views, "document.getElementById('" + root + "').innerText");
    if (refused) {
      measured.action_recovered &&= await evaluate(views, "document.getElementById('" + mergeId + "')?.disabled === true");
      await evaluate(views, "document.querySelector('#" + feedback + "-error details').open = true, true");
      measured.action_recovered &&= await evaluate(views, "document.getElementById('" + feedback + "-error').innerText.includes(" + JSON.stringify(input.experience === "dirty_worktree" ? "Save or commit tracked changes" : "original branch and tracked files were restored") + ")");
    }
    await evaluate(views, "document.getElementById('" + feedback + (refused ? "-error" : "-receipt") + "')?.scrollIntoView({block: 'center'}), new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)))");
    const resultCapture = await views.dt.send("Page.captureScreenshot", { format: "png", captureBeyondViewport: false }, views.sessionId);
    fs.writeFileSync(require("node:path").join(input.capture_directory, input.experience + "-" + variant + "-result.png"), Buffer.from(resultCapture.data, "base64"));
    if (refused) {
      await evaluate(views, "document.getElementById('" + feedback + "-refresh').click(), true");
      ready = await until(views, "document.getElementById('" + mergeId + "')?.disabled === false");
      measured.action_recovered &&= !ready.timed_out;
    }
  } else {
    await evaluate(views, "document.getElementById('highbay-build-again').click(), true");
    ready = await until(views, "document.getElementById('highbay-build-confirm')?.disabled === false");
    if (ready.timed_out) throw new Error("explicit Build confirmation did not open");
    await evaluate(views, "document.getElementById('highbay-build-confirm').click(), true");
    ready = await until(views, "document.getElementById('highbay-build-start-receipts') !== null");
    if (ready.timed_out) throw new Error("explicit Build launch did not return a receipt");
  }
  facts.experience = [measured];
}

// approvalScenario captures the real served UI using explicit typed owner
// fixtures. The separate served-page tests exercise real approval persistence.
async function approvalScenario(input, facts, views) {
  await views.dt.send("Page.bringToFront", {}, views.sessionId);
  let ready = await until(views, "document.getElementById('highbay-nav-cp5') !== null");
  if (ready.timed_out) throw new Error("approval navigation did not load");
  await evaluate(views, "document.getElementById('highbay-nav-cp5').click(), true");
  ready = await until(views, "document.getElementById('highbay-approval-stage') !== null && document.querySelector('.highbay-flow-evidence tbody tr') !== null && !document.getElementById('highbay-cp5-observation-status')");
  if (ready.timed_out) throw new Error("approval evidence did not settle: " + JSON.stringify({
    page: await evaluate(views, "({ hidden: document.hidden, visibility: document.visibilityState, disabled: document.getElementById('highbay-cp5-approve')?.disabled, text: document.body.innerText.slice(0, 5000) })"),
    requests: views.requests.filter((r) => r.method === "GET").map((r) => ({ url: r.url, status: r.status, failure: r.failure })),
    console: views.console,
  }));
  facts.approval = [];
  const sizes = [["desktop", 1280, 900, 1], ["laptop", 1024, 768, 1], ["phone", 390, 844, 1]];
  if (input.approval_state === "current") sizes.push(["dark", 1280, 900, 1], ["reflow-200", 640, 450, 2]);
  for (const [name, width, height, scale] of sizes) {
    await views.dt.send("Emulation.setDeviceMetricsOverride", { width, height, deviceScaleFactor: scale, mobile: width < 600 }, views.sessionId);
    await views.dt.send("Emulation.setEmulatedMedia", { features: [{ name: "prefers-color-scheme", value: name === "dark" ? "dark" : "light" }] }, views.sessionId);
    let coverageLinked = false;
    if (input.approval_state === "current") {
      await evaluate(views, "document.getElementById('highbay-nav-build').click(), true");
      const buildReady = await until(views, "document.getElementById('highbay-build-coverage') !== null");
      if (buildReady.timed_out) throw new Error("accepted Build coverage did not appear");
      await evaluate(views, "(" + function revealBuildCoverage() {
        for (let parent = document.getElementById("highbay-build-coverage").parentElement; parent; parent = parent.parentElement) {
          if (parent.tagName === "DETAILS") parent.open = true;
        }
      }.toString() + ")()");
      const buildVisible = await until(views, "document.getElementById('highbay-build-coverage').getBoundingClientRect().height > 0");
      if (buildVisible.timed_out) throw new Error("accepted Build coverage stayed hidden");
      const coverageFacts = function coverageFacts(id) {
        const section = document.getElementById(id);
        return {
          requirements: Array.from(section.querySelectorAll('dt'), (node) => node.textContent),
          scenarios: Array.from(section.querySelectorAll('li code'), (node) => node.textContent),
          explanations: Array.from(section.querySelectorAll('li > p:last-child'), (node) => node.textContent),
        };
      };
      const builtCoverage = await evaluate(views, "(" + coverageFacts.toString() + ")('highbay-build-coverage')");
      if (name === "desktop" || name === "phone") {
        await evaluate(views, "document.getElementById('highbay-build-coverage').scrollIntoView({block: 'start'}), new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)))");
        const shown = await views.dt.send("Page.captureScreenshot", { format: "png", captureBeyondViewport: false }, views.sessionId);
        fs.writeFileSync(require("node:path").join(input.capture_directory, "current-" + name + "-build-coverage.png"), Buffer.from(shown.data, "base64"));
      }
      await evaluate(views, "document.getElementById('highbay-nav-cp4').click(), true");
      const checked = await until(views, "document.getElementById('highbay-cp4-coverage') !== null && !document.getElementById('highbay-cp4-verification').hasAttribute('aria-busy')");
      if (checked.timed_out) throw new Error("Check did not load the selected run's saved coverage");
      const checkedCoverage = await evaluate(views, "(" + coverageFacts.toString() + ")('highbay-cp4-coverage')");
      if (JSON.stringify(builtCoverage) !== JSON.stringify(checkedCoverage)) {
        throw new Error("Check changed the build's recorded coverage statements");
      }
      const checkedLimits = await evaluate(views, "document.getElementById('highbay-cp4-evidence-limits').textContent");
      if (!/no separate.*check_project.*attached/i.test(checkedLimits)) {
        throw new Error("Check lost the separate builder-check evidence limit");
      }
      await evaluate(views, "document.getElementById('highbay-nav-cp5').click(), true");
      const reviewed = await until(views, "document.getElementById('highbay-approval-coverage') !== null && !document.getElementById('highbay-cp5-observation-status')");
      if (reviewed.timed_out) throw new Error("recorded approval coverage did not appear");
      const recordedCoverage = await evaluate(views, "(" + coverageFacts.toString() + ")('highbay-approval-coverage')");
      coverageLinked = builtCoverage.requirements.length > 0 && builtCoverage.scenarios.length > 0 &&
        JSON.stringify(builtCoverage) === JSON.stringify(recordedCoverage);
      if (name === "desktop" || name === "phone") {
        await evaluate(views, "document.getElementById('highbay-approval-coverage').scrollIntoView({block: 'start'}), new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)))");
        const shown = await views.dt.send("Page.captureScreenshot", { format: "png", captureBeyondViewport: false }, views.sessionId);
        fs.writeFileSync(require("node:path").join(input.capture_directory, "current-" + name + "-approval-coverage.png"), Buffer.from(shown.data, "base64"));
      }
    }
    await evaluate(views, "window.scrollTo(0, 0), new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)))");
    // Two paint frames can still fall inside the incumbent 150ms color
    // transition. Measure rest only after finite animations settle, bounded
    // by the same deadline as every other browser observation.
    const settled = await until(views, "document.getAnimations().every((animation) => animation.effect?.getTiming().iterations === Infinity || animation.playState !== 'running')");
    if (settled.timed_out) throw new Error("approval colors did not settle at " + name);
    const primaryAtRest = await evaluate(views, "(" + function measureRestingAction() {
      const action = document.getElementById("highbay-cp5-approve");
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
      await evaluate(views, "document.querySelector('.highbay-skip-link').focus(), true");
      for (let step = 0; step < 24; step++) {
        await views.dt.send("Input.dispatchKeyEvent", { type: "keyDown", key: "Tab", code: "Tab", windowsVirtualKeyCode: 9 }, views.sessionId);
        await views.dt.send("Input.dispatchKeyEvent", { type: "keyUp", key: "Tab", code: "Tab", windowsVirtualKeyCode: 9 }, views.sessionId);
        reached = await evaluate(views, "document.activeElement?.id === 'highbay-cp5-approve'");
        if (reached) break;
      }
    }
    const measured = await evaluate(views, "(" + function measureApproval() {
      const stage = document.getElementById("highbay-approval-stage");
      const action = document.getElementById("highbay-cp5-approve");
      const footer = document.getElementById("highbay-frame-next");
      const phone = matchMedia("(max-width: 720px)").matches;
      const style = action ? getComputedStyle(action) : null;
      const padding = parseFloat(getComputedStyle(document.body).paddingBottom);
      return {
        width: document.documentElement.clientWidth,
        document_width: document.documentElement.scrollWidth,
        heading: document.getElementById("highbay-cp5-title").textContent,
        finished_flows: stage.querySelectorAll(".highbay-flow-evidence tbody tr").length,
        draft_visible: stage.querySelector(".highbay-approval-exclusions").textContent.includes("Draft, no required test"),
        approve_count: document.querySelectorAll("#highbay-cp5-approve").length,
        approve_in_next: action !== null && footer.contains(action),
        approve_enabled: action !== null && !action.disabled,
        focus_visible: action !== null && document.activeElement === action && style.outlineStyle !== "none" && parseFloat(style.outlineWidth) >= 2,
        commands: Array.from(stage.querySelectorAll(".highbay-build-merge pre"), (node) => node.textContent),
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
    measured.coverage_linked = coverageLinked;
    measured.keyboard_reached_action = reached;
    measured.flow_lines_readable = true;
    const capture = await views.dt.send("Page.captureScreenshot", { format: "png", captureBeyondViewport: true }, views.sessionId);
    measured.screenshot = require("node:path").join(input.capture_directory, input.approval_state + "-" + name + ".png");
    fs.writeFileSync(measured.screenshot, Buffer.from(capture.data, "base64"));
    if (name === "phone" && input.approval_state === "current") {
      for (let row = 0; row < measured.finished_flows; row++) {
        const readable = await evaluate(views, "(" + async function inspectFlow(index) {
          const row = document.querySelectorAll(".highbay-flow-evidence tbody tr")[index];
          row.scrollIntoView({ block: "center" });
          await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)));
          const footer = document.getElementById("highbay-frame-next").getBoundingClientRect();
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
  const ready = await until(views, "document.getElementById('highbay-nav-build') !== null && document.getElementById('highbay-build-test-writer') !== null");
  if (ready.timed_out) throw new Error("test review did not load");
  await evaluate(views, "document.getElementById('highbay-nav-build').click(), true");
  if (["needs_changes", "stale"].includes(input.test_review_state)) {
    const findings = await until(views, "document.querySelector('.highbay-build-finding-text') !== null");
    if (findings.timed_out) throw new Error("test findings did not load");
  }
  facts.test_review = [];
  for (const [name, width, height, scale] of [["desktop", 1280, 900, 1], ["laptop", 1024, 768, 1],
      ["phone", 390, 844, 1], ["dark", 1280, 900, 1], ["reflow-200", 640, 450, 2]]) {
    await views.dt.send("Emulation.setDeviceMetricsOverride", { width, height, deviceScaleFactor: scale, mobile: width < 600 }, views.sessionId);
    await views.dt.send("Emulation.setEmulatedMedia", { features: [{ name: "prefers-color-scheme", value: name === "dark" ? "dark" : "light" }] }, views.sessionId);
    await evaluate(views, "window.scrollTo(0, 0), new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)))");
    const measured = await evaluate(views, "(" + function measureTestReview() {
      const next = document.getElementById("highbay-frame-next");
      const region = document.querySelector(".highbay-build-finding-text");
      const box = region?.getBoundingClientRect();
      const phone = matchMedia("(max-width: 720px)").matches;
      return { width: innerWidth, document_width: document.documentElement.scrollWidth,
        required_tests: document.querySelectorAll("#highbay-build-required-tests > li").length,
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
        if (await evaluate(views, "document.activeElement?.classList.contains('highbay-build-finding-text')")) {
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

// agreementScenario measures one long saved summary and its existing reading
// destination, using native Tab/Enter defaults and one browser for both widths.
async function agreementScenario(input, facts, views) {
  const ready = await until(views, "document.getElementById('highbay-confirm-submit') !== null");
  if (ready.timed_out) throw new Error("agreement form did not load");
  if (await evaluate(views, "document.getElementById('highbay-read-full-summary') !== null")) {
    throw new Error("unconfirmed proposal offers an agreed summary");
  }
  await evaluate(views, "document.getElementById('highbay-confirm-submit').click(), true");
  const confirmed = await until(views, "document.getElementById('highbay-read-full-summary') !== null");
  if (confirmed.timed_out) throw new Error("confirmation did not expose summary reading");
  facts.agreement = [];
  for (const [name, width, height] of [["desktop", 1280, 900], ["phone", 390, 844]]) {
    await views.dt.send("Emulation.setDeviceMetricsOverride", { width, height, deviceScaleFactor: 1, mobile: width < 600 }, views.sessionId);
    await evaluate(views, "document.getElementById('highbay-nav-cp1').click(), document.getElementById('highbay-refine-agreement').open = true, true");
    await evaluate(views, "(" + function focusDraft() {
      const field = document.querySelector('textarea[data-field="expected_outcome"]');
      field.focus();
      field.setSelectionRange(0, field.value.length);
      return true;
    }.toString() + ")()");
    await views.dt.send("Input.insertText", { text: "Unsaved example for " + name }, views.sessionId);
    await evaluate(views, "document.getElementById('highbay-agreement-details').open = false, document.getElementById('highbay-nav-changes').click(), window.scrollTo(0, 0), new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)))");
    const measured = await evaluate(views, "(" + function measurePreview() {
      const preview = document.querySelector('.highbay-frame-summary');
      return {
        width: document.documentElement.clientWidth,
        document_width: document.documentElement.scrollWidth,
        preview_height: preview.getBoundingClientRect().height,
        line_height: parseFloat(getComputedStyle(preview).lineHeight),
        preview_clipped: preview.scrollHeight > preview.clientHeight,
      };
    }.toString() + ")()");
    measured.name = name;
    measured.preview_screenshot = require("node:path").join(input.capture_directory, name + "-summary-preview.png");
    const preview = await views.dt.send("Page.captureScreenshot", { format: "png", captureBeyondViewport: false }, views.sessionId);
    fs.writeFileSync(measured.preview_screenshot, Buffer.from(preview.data, "base64"));
    await evaluate(views, "document.querySelector('.highbay-skip-link').focus(), true");
    measured.keyboard_reachable = false;
    for (let step = 0; step < 32; step++) {
      await views.dt.send("Input.dispatchKeyEvent", { type: "keyDown", key: "Tab", code: "Tab", windowsVirtualKeyCode: 9 }, views.sessionId);
      await views.dt.send("Input.dispatchKeyEvent", { type: "keyUp", key: "Tab", code: "Tab", windowsVirtualKeyCode: 9 }, views.sessionId);
      measured.keyboard_reachable = await evaluate(views, "document.activeElement?.id === 'highbay-read-full-summary'");
      if (measured.keyboard_reachable) break;
    }
    measured.focus_visible = await evaluate(views, "(" + function focusVisible() {
      const style = getComputedStyle(document.getElementById('highbay-read-full-summary'));
      return style.outlineStyle !== 'none' && parseFloat(style.outlineWidth) >= 2;
    }.toString() + ")()");
    if (!measured.keyboard_reachable) throw new Error("Read full summary is unreachable by keyboard at " + name);
    await views.dt.send("Input.dispatchKeyEvent", { type: "keyDown", key: "Enter", code: "Enter", windowsVirtualKeyCode: 13, text: "\r" }, views.sessionId);
    await views.dt.send("Input.dispatchKeyEvent", { type: "keyUp", key: "Enter", code: "Enter", windowsVirtualKeyCode: 13 }, views.sessionId);
    await evaluate(views, "new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)))");
    Object.assign(measured, await evaluate(views, "(" + function measureSummary(draft) {
      const summary = document.getElementById('highbay-agreement-summary');
      const box = summary.getBoundingClientRect();
      const frame = document.getElementById('highbay-checkpoint-nav');
      return {
        full_text: summary.textContent,
        details_open: document.getElementById('highbay-agreement-details').open,
        summary_focused: document.activeElement === summary,
        summary_visible: box.top >= (getComputedStyle(frame).position === 'sticky' ? frame.getBoundingClientRect().bottom : 0) && box.top < innerHeight,
        draft_preserved: document.querySelector('textarea[data-field="expected_outcome"]').value === draft,
      };
    }.toString() + ")(" + JSON.stringify("Unsaved example for " + name) + ")"));
    measured.summary_screenshot = require("node:path").join(input.capture_directory, name + "-summary-reading.png");
    const reading = await views.dt.send("Page.captureScreenshot", { format: "png", captureBeyondViewport: false }, views.sessionId);
    fs.writeFileSync(measured.summary_screenshot, Buffer.from(reading.data, "base64"));
    const end = await evaluate(views, "(" + async function reachSummaryEnd() {
      const summary = document.getElementById('highbay-agreement-summary');
      summary.scrollIntoView({ block: 'end' });
      await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)));
      const box = summary.getBoundingClientRect();
      const footer = document.getElementById('highbay-frame-next');
      const bottom = getComputedStyle(footer).position === 'fixed' ? footer.getBoundingClientRect().top : innerHeight;
      return { end: box.bottom, bottom, scrollY, overflow: getComputedStyle(summary).overflowY };
    }.toString() + ")()");
    measured.end_reachable = end.end > 0 && end.end <= end.bottom + 1 && end.overflow === "visible";
    if (!measured.end_reachable) throw new Error("The end of the agreement is outside the readable viewport at " + name + ": " + JSON.stringify(end));
    facts.agreement.push(measured);
  }
}

// main runs the scenario. Each section names the Go subtest that judges it.
// onboardingScenario follows an empty project into its first proposal and
// captures each surface on both viewport widths and color schemes.
async function onboardingScenario(input, views) {
  const variants = [
    ["desktop-light", 1280, 900, "light"], ["desktop-dark", 1280, 900, "dark"],
    ["phone-light", 375, 800, "light"], ["phone-dark", 375, 800, "dark"],
  ];
  const requireView = async (expression, message) => {
    if ((await until(views, expression)).timed_out) throw new Error(message);
  };
  const press = async (key, windowsVirtualKeyCode) => {
    const params = { key, code: key, windowsVirtualKeyCode };
    if (key === "Enter") await views.dt.send("Input.dispatchKeyEvent", { ...params, type: "keyDown", text: "\r" }, views.sessionId);
    else await views.dt.send("Input.dispatchKeyEvent", { ...params, type: "keyDown" }, views.sessionId);
    await views.dt.send("Input.dispatchKeyEvent", { ...params, type: "keyUp" }, views.sessionId);
  };
  const capture = async (view) => {
    for (const [variant, width, height, theme] of variants) {
      await views.dt.send("Emulation.setDeviceMetricsOverride", { width, height, deviceScaleFactor: 1, mobile: width < 600 }, views.sessionId);
      await views.dt.send("Emulation.setEmulatedMedia", { features: [{ name: "prefers-color-scheme", value: theme }] }, views.sessionId);
      await evaluate(views, "window.scrollTo(0, 0), new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))).then(() => Promise.all(document.getAnimations().filter(animation => animation instanceof CSSTransition).map(animation => animation.finished.catch(() => {}))))");
      await requireView("document.documentElement.scrollWidth <= document.documentElement.clientWidth",
        view + " widened the page at " + variant);
      if (width < 600) {
        await requireView("document.querySelector('.highbay-frame-project-menu > summary').getBoundingClientRect().height >= 44",
          "Inspect's phone target is too small");
      }
      const screenshot = await views.dt.send("Page.captureScreenshot", { format: "png", captureBeyondViewport: true }, views.sessionId);
      fs.writeFileSync(require("node:path").join(input.capture_directory, view + "-" + variant + ".png"), Buffer.from(screenshot.data, "base64"));
    }
  };

  await requireView("document.querySelector('.highbay-welcome-card h2')?.textContent.includes('Describe what you want to build') && !document.querySelector('.highbay-frame-slots, .highbay-frame-journey, .highbay-frame-context')",
    "the connected empty project did not settle on the welcome card");
  await capture("welcome");
  await evaluate(views, "document.querySelector('.highbay-frame-project-menu > summary').focus(), true");
  await press("Enter", 13);
  await requireView("document.querySelector('.highbay-frame-project-menu').open", "Enter did not open Inspect");
  await press("Tab", 9);
  await requireView("document.activeElement.id === 'highbay-nav-team'", "Tab did not reach Team in Inspect");
  await press("Escape", 27);
  const escaped = await evaluate(views, "({ open: document.querySelector('.highbay-frame-project-menu').open, summaryFocused: document.activeElement.matches('.highbay-frame-project-menu > summary'), focused: document.activeElement?.id || document.activeElement?.tagName })");
  if (escaped.open || !escaped.summaryFocused) {
    throw new Error("Escape did not dismiss Inspect and restore focus: " + JSON.stringify(escaped));
  }
  await press("Enter", 13);
  const point = await evaluate(views, "(() => { const box = document.querySelector('.highbay-welcome-card h2').getBoundingClientRect(); return {x: box.left + 10, y: box.top + 10}; })()");
  await views.dt.send("Input.dispatchMouseEvent", { ...point, type: "mousePressed", button: "left", clickCount: 1 }, views.sessionId);
  await views.dt.send("Input.dispatchMouseEvent", { ...point, type: "mouseReleased", button: "left", clickCount: 1 }, views.sessionId);
  await requireView("!document.querySelector('.highbay-frame-project-menu').open", "outside pointer press did not dismiss Inspect");
  await evaluate(views, "document.querySelector('.highbay-frame-project-menu > summary').focus(), true");
  await press("Enter", 13);
  await press("Tab", 9);
  await press("Enter", 13);
  await requireView("document.querySelector('.highbay-frame-project-menu > summary').textContent === 'Inspect · Team' && !document.querySelector('.highbay-frame-project-menu').open",
    "choosing Team did not close Inspect and name the current view");
  await evaluate(views, "document.getElementById('highbay-nav-changes').click(), true");
  await requireView("Boolean(document.querySelector('.highbay-welcome-card'))", "returning from Team did not show the welcome card");

  const proposed = await fetch(input.origin + "/changes/propose", {
    method: "POST",
    headers: { Authorization: "Bearer " + input.onboarding.guide, "Content-Type": "application/json" },
    body: JSON.stringify(input.onboarding.proposal),
  });
  if (!proposed.ok) throw new Error("fixture proposal was refused with HTTP " + proposed.status);
  const opened = await until(views, "Boolean(document.querySelector('#highbay-openui-workspace .shadcn-choice-group') && document.querySelector('#highbay-cp1-form-card .shadcn-choice-group'))");
  if (opened.timed_out) {
    const displayed = await evaluate(views, "document.body.innerText.slice(0, 1800)");
    throw new Error("the first proposal did not open both its decision aid and its remaining question: " + displayed);
  }
  await capture("agree");

  await evaluate(views, "document.getElementById('highbay-cp1-next-question').focus(), true");
  await press("Enter", 13);
  await requireView("document.activeElement.dataset.openuiAmbiguityId === 'refund-rules-ambiguity-01'", "Next did not focus the first question in OpenUI");
  await requireView("document.activeElement.getBoundingClientRect().bottom <= document.getElementById('highbay-frame-next').getBoundingClientRect().top",
    "the fixed Next action covered the focused answer on the phone");
  await press("ArrowDown", 40);
  await requireView("document.activeElement.getAttribute('aria-pressed') === 'true' && document.activeElement.tabIndex === 0 && document.activeElement.dataset.openuiOption.includes('full amount')",
    "arrow navigation did not focus and select the next option");
  await press("Tab", 9);
  await requireView("!document.activeElement.dataset.openuiAmbiguityId", "Tab stayed inside a single question's options");
  await evaluate(views, "document.getElementById('highbay-cp1-next-question').focus(), true");
  await press("Enter", 13);
  await requireView("document.activeElement.dataset.ambiguityId === 'refund-rules-ambiguity-02'", "Next did not advance to the fixed question");
  await press("Enter", 13);
  await requireView("document.getElementById('highbay-cp1-next-confirm')?.disabled === false", "Next did not offer confirmation after the last answer");
}

// workspaceScenario exercises UpdateBanner (draft-safe session-preserving restart),
// ConversationRail message posting and Markdown rendering, and TerminalDrawer
// (@xterm/xterm WebSocket attach, byte-credit flow control, Ctrl+Shift+Escape
// keyboard focus escape, and one-click server-owned role Restart) in Chromium.
async function workspaceScenario(input, facts, views) {
  await views.dt.send("Page.bringToFront", {}, views.sessionId);
  const bannerShown = await until(
    views,
    "document.getElementById('highbay-update-banner') !== null && document.getElementById('highbay-update-install') !== null && document.getElementById('highbay-composer-input') !== null"
  );
  if (bannerShown.timed_out) {
    throw new Error("UpdateBanner or ConversationRail did not render: " + await evaluate(views, "document.body.innerText.slice(0, 2000)"));
  }

  const releaseHref = await evaluate(views, "document.getElementById('highbay-update-release')?.getAttribute('href') || ''");
  await evaluate(views, "document.getElementById('highbay-composer-input').focus(), true");
  await views.dt.send("Input.insertText", { text: "Build a refund assistant" }, views.sessionId);

  await evaluate(views, "document.getElementById('highbay-update-install').click(), true");
  const updateReady = await until(
    views,
    "document.getElementById('highbay-update-reload') !== null && document.getElementById('highbay-update-message')?.textContent.includes('v0.5.0 is running')"
  );
  if (updateReady.timed_out) {
    throw new Error("UpdateBanner did not reach ready state: " + await evaluate(views, "document.getElementById('highbay-update-banner')?.innerText || ''"));
  }
  const draftSurvivedUpdate = await evaluate(
    views,
    "document.getElementById('highbay-composer-input').value === 'Build a refund assistant' && document.getElementById('highbay-update-unsaved-hint') !== null"
  );

  await evaluate(views, "document.getElementById('highbay-composer-send').click(), true");
  const replyRendered = await until(
    views,
    "document.querySelector('#highbay-conversation-messages strong')?.textContent === 'Draft ready'"
  );
  if (replyRendered.timed_out) {
    throw new Error("ConversationRail did not render Markdown reply: " + await evaluate(views, "document.getElementById('highbay-conversation-messages')?.innerText || ''"));
  }

  const attentionBtn = await until(views, "document.getElementById('highbay-team-attention-terminal-builder') !== null");
  if (attentionBtn.timed_out) {
    throw new Error("Builder attention terminal button did not render");
  }
  await evaluate(views, "document.getElementById('highbay-team-attention-terminal-builder').click(), true");
  const connected = await until(
    views,
    "document.getElementById('highbay-terminal-drawer')?.dataset.status === 'connected' && document.getElementById('highbay-terminal-viewport')?.textContent.includes('Allow tool?')"
  );
  if (connected.timed_out) {
    throw new Error("TerminalDrawer did not connect and render PTY output: " + await evaluate(views, "document.getElementById('highbay-terminal-drawer')?.innerText || ''"));
  }

  // Type inside xterm: plain Escape reaches the PTY without closing the drawer,
  // while Ctrl+Shift+Escape moves focus to Close terminal.
  await evaluate(views, "document.querySelector('#highbay-terminal-viewport textarea')?.focus(), true");
  await views.dt.send("Input.insertText", { text: "y" }, views.sessionId);
  await views.dt.send("Input.dispatchKeyEvent", { type: "keyDown", key: "Escape", code: "Escape", windowsVirtualKeyCode: 27 }, views.sessionId);
  await views.dt.send("Input.dispatchKeyEvent", { type: "keyUp", key: "Escape", code: "Escape", windowsVirtualKeyCode: 27 }, views.sessionId);
  const plainEscapeInsideKeptOpen = await evaluate(views, "document.getElementById('highbay-terminal-drawer') !== null");
  await views.dt.send(
    "Input.dispatchKeyEvent",
    {
      type: "keyDown",
      key: "Escape",
      code: "Escape",
      windowsVirtualKeyCode: 27,
      modifiers: 2 | 8, // Ctrl (2) | Shift (8)
    },
    views.sessionId
  );
  await views.dt.send(
    "Input.dispatchKeyEvent",
    {
      type: "keyUp",
      key: "Escape",
      code: "Escape",
      windowsVirtualKeyCode: 27,
      modifiers: 2 | 8,
    },
    views.sessionId
  );
  const focusEscaped = await evaluate(views, "document.activeElement?.id === 'highbay-terminal-close'");

  // Restart another role (guide) while builder's TerminalDrawer stays open, and
  // verify builder's stream neither disconnects nor requests a second ticket.
  await evaluate(views, "document.getElementById('highbay-nav-team').click(), true");
  const guideRestartBtn = await until(views, "document.getElementById('highbay-team-restart-guide') !== null");
  if (guideRestartBtn.timed_out) {
    throw new Error("Guide restart button did not render on Team stage");
  }
  await evaluate(views, "document.getElementById('highbay-team-restart-guide').click(), true");
  await settle(views, to(input.origin, "POST", "/team/roles/guide/restart"), 1);
  const builderTicketsAfterGuideRestart = views.requests.filter(to(input.origin, "POST", "/team/roles/builder/terminal-ticket")).length;
  const builderKeptAcrossGuideRestart =
    builderTicketsAfterGuideRestart === 1 &&
    (await evaluate(
      views,
      "document.getElementById('highbay-terminal-drawer')?.dataset.role === 'builder' && document.getElementById('highbay-terminal-drawer')?.dataset.status === 'connected' && document.getElementById('highbay-terminal-viewport')?.textContent.includes('Allow tool?')"
    ));

  // Click one-click Restart in TerminalDrawer and verify reconnect to restarted stream.
  await evaluate(views, "document.getElementById('highbay-terminal-viewport').scrollIntoView({ block: 'center' }), true");
  await evaluate(views, "document.getElementById('highbay-terminal-restart').click(), true");
  const restarted = await until(
    views,
    "document.getElementById('highbay-terminal-viewport')?.textContent.includes('Restarted builder session')"
  );
  if (restarted.timed_out) {
    throw new Error("TerminalDrawer did not reconnect after role restart");
  }

  // Plain Escape on a drawer header control outside xterm closes the drawer.
  await evaluate(views, "document.getElementById('highbay-terminal-close')?.focus(), true");
  await views.dt.send("Input.dispatchKeyEvent", { type: "keyDown", key: "Escape", code: "Escape", windowsVirtualKeyCode: 27 }, views.sessionId);
  await views.dt.send("Input.dispatchKeyEvent", { type: "keyUp", key: "Escape", code: "Escape", windowsVirtualKeyCode: 27 }, views.sessionId);
  const closedByPlainEscape = await until(views, "document.getElementById('highbay-terminal-drawer') === null");

  facts.workspace = {
    release_href: releaseHref,
    update_ready: !updateReady.timed_out,
    draft_survived_update: draftSurvivedUpdate,
    reply_rendered: !replyRendered.timed_out,
    terminal_connected: !connected.timed_out,
    plain_escape_inside_kept_open: plainEscapeInsideKeptOpen,
    focus_escaped: focusEscaped,
    builder_kept_across_guide_restart: builderKeptAcrossGuideRestart,
    terminal_restarted: !restarted.timed_out,
    plain_escape_outside_closed: !closedByPlainEscape.timed_out,
    browser_cookies: (await views.dt.send("Storage.getCookies", {})).cookies.length,
  };
}

async function main(input, facts) {
  const dt = await DevTools.connect(input.devtools);
  try {
    const version = await dt.send("Browser.getVersion");
    facts.browser = { product: version.product, protocol: version.protocolVersion, user_agent: version.userAgent };
    const server = (r) => r.url.startsWith(input.origin + "/");
    const longPoll = (r) => new URL(r.url).pathname === "/change-updates";

    // (a) Open the page with the link, as `highbay` opens it.
    const views = await openTab(dt);
    await navigate(views, input.origin + "/views#link=" + input.link);
    const listed = await until(
      views,
      "(() => { const ids = Array.from(document.querySelectorAll('#highbay-change-list [data-change-id]'), (item) => item.dataset.changeId); return ids.length >= " +
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

    if (input.onboarding) {
      await onboardingScenario(input, views);
      await dt.send("Target.closeTarget", { targetId: views.targetId });
      return;
    }
    if (input.agreement) {
      await agreementScenario(input, facts, views);
      await dt.send("Target.closeTarget", { targetId: views.targetId });
      return;
    }
    if (input.experience) {
      await experienceScenario(input, facts, views);
      await dt.send("Target.closeTarget", { targetId: views.targetId });
      return;
    }
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
    if (input.workspace) {
      await workspaceScenario(input, facts, views);
      await dt.send("Target.closeTarget", { targetId: views.targetId });
      return;
    }

    // (b) Every request the page sent so far, with the headers the browser put on the wire.
    facts.views.requests_settled =
      (await settle(views, to(input.origin, "POST", "/project/browser-session"), 1)) &&
      (await settle(views, to(input.origin, "GET", "/changes"), 1)) &&
      (await settle(views, to(input.origin, "GET", "/team"), 1)) &&
      (await settle(views, to(input.origin, "GET", "/team/conversation"), 2)) &&
      (await settle(views, to(input.origin, "GET", "/change-updates"), 1, longPoll));
    facts.views.requests = views.requests.filter(server).map((r) => ({ ...r }));

    // (c) What the browser kept for the page.
    facts.views.storage = await evaluate(
      views,
      "indexedDB.databases().then((dbs) => ({ local_storage: localStorage.length, session_storage: sessionStorage.length, cookie: document.cookie, indexed_db: dbs.length }))"
    );
    facts.views.storage.browser_cookies = (await dt.send("Storage.getCookies", {})).cookies.length;

    // (d) The page's policy: its own load, an injected inline script, a fetch to another origin.
    facts.views.load_violations = await evaluate(views, "window.__highbayViolations.slice()");
    let count = facts.views.load_violations.length;
    await evaluate(
      views,
      "(() => { const s = document.createElement('script'); s.textContent = 'window.__highbayInjectedRan = true;'; document.body.appendChild(s); return true; })()"
    );
    const injected = await violationsSince(views, count);
    facts.views.injected_script_ran = await evaluate(views, "window.__highbayInjectedRan === true");
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
    const beforeStory = await evaluate(views, "window.__highbayViolations.length");
    await evaluate(views, "document.getElementById(" + JSON.stringify("highbay-open-change-" + input.story_change_id) + ").click(), true");
    await until(views, "(document.getElementById('highbay-cp1-editor') || { dataset: {} }).dataset.changeId === " + JSON.stringify(input.story_change_id));
    await evaluate(views, "document.getElementById('highbay-nav-story').click(), true");
    const row =
      "document.querySelector('#highbay-story-change .highbay-story-flow[data-flow-key=" +
      JSON.stringify(input.story_flow) +
      "] .highbay-story-steps')";
    const shown = await until(views, row + " !== null");
    const tabStop = await until(views, "(" + row + " || {}).tabIndex === 0");
    facts.story = shown.timed_out
      ? { found: false }
      : await evaluate(
          views,
          "(() => { const row = " +
            row +
            "; const start = row.scrollLeft; row.scrollLeft = row.scrollWidth; const scrolled = row.scrollLeft; row.scrollLeft = start;" +
            " return { found: true, steps: row.querySelectorAll('.highbay-story-step').length, tab_index: row.tabIndex," +
            " overflow_x: getComputedStyle(row).overflowX, row_scroll_width: row.scrollWidth, row_client_width: row.clientWidth," +
            " scrolled_to: scrolled, row_right: row.getBoundingClientRect().right," +
            " stage_right: document.getElementById('highbay-story-stage').getBoundingClientRect().right," +
            " document_scroll_width: document.documentElement.scrollWidth, viewport_width: document.documentElement.clientWidth }; })()"
        );
    facts.story.tab_stop_timed_out = tabStop.timed_out;
    facts.story.violations = await evaluate(views, "window.__highbayViolations.slice(" + beforeStory + ")");

    // (i) Open the Live target view at a phone's width and measure which
    // model form fields are displayed and whether anything widens the page.
    await withDeadline(
      views.dt.send("Emulation.setDeviceMetricsOverride", { width: 375, height: 800, deviceScaleFactor: 1, mobile: true }, views.sessionId),
      "narrow the viewport"
    );
    await evaluate(views, "document.getElementById('highbay-nav-live').click(), true");
    const liveShown = await until(
      views,
      "document.getElementById('highbay-model-form') !== null && (document.getElementById('highbay-live-target') || { dataset: {} }).dataset.state !== 'unread'"
    );
    facts.live = liveShown.timed_out
      ? { found: false }
      : await evaluate(
          views,
          "(() => ({ found: true, live_state: document.getElementById('highbay-live-target').dataset.state," +
            " direct_display: getComputedStyle(document.getElementById('highbay-model-direct-fields')).display," +
            " gateway_display: getComputedStyle(document.getElementById('highbay-model-gateway-fields')).display," +
            " document_scroll_width: document.documentElement.scrollWidth, viewport_width: document.documentElement.clientWidth }))()"
        );
    await withDeadline(views.dt.send("Emulation.clearDeviceMetricsOverride", {}, views.sessionId), "restore the viewport");

    // (g) Reload and sibling tab stay signed in while the server runs, other loopback
    // ports do not share localStorage, and an unhanded-off restart signs out cleanly.
    const beforeLiveReload = views.requests.length;
    await reload(views);
    const liveReloaded = await until(
      views,
      "document.querySelectorAll('#highbay-change-list [data-change-id]').length >= " + input.change_ids.length
    );
    await settle(views, to(input.origin, "GET", "/project/browser-session"), 1);
    const liveRequests = views.requests.slice(beforeLiveReload).filter(server).map((r) => ({ ...r }));

    const sibling = await openTab(dt);
    await navigate(sibling, input.origin + "/views");
    const siblingListed = await until(
      sibling,
      "document.querySelectorAll('#highbay-change-list [data-change-id]').length >= " + input.change_ids.length
    );
    await dt.send("Target.closeTarget", { targetId: sibling.targetId });

    const otherPortTab = await openTab(dt);
    await navigate(otherPortTab, input.same_site_origin + "/blank");
    const otherPortStorage = await evaluate(otherPortTab, "localStorage.length");
    await dt.send("Target.closeTarget", { targetId: otherPortTab.targetId });

    const restartResp = await fetch(input.origin + "/__test/restart", { method: "POST" });
    if (!restartResp.ok) throw new Error("restarting project server: " + restartResp.status);

    const beforeReload = views.requests.length;
    await reload(views);
    const signedOut = await until(views, "(document.getElementById('highbay-signed-out') || {}).textContent");
    const postRestartStorage = await evaluate(views, "localStorage.length");
    const shownAfterReload = views.requests.slice(beforeReload).map((r) => r.id);
    const probeURL = input.origin + "/change-updates?since=after-reload";
    const status = await evaluate(views, "fetch(" + JSON.stringify(probeURL) + ").then((r) => r.status, (e) => 'refused: ' + e.message)");
    await settle(views, (r) => r.url === probeURL, 1);
    facts.reload = {
      stayed_signed_in: !liveReloaded.timed_out,
      sibling_signed_in: !siblingListed.timed_out,
      other_port_storage: otherPortStorage,
      live_requests: liveRequests,
      signed_out_text: signedOut.value || "",
      signed_out_timed_out: signedOut.timed_out,
      post_restart_storage: postRestartStorage,
      href: await evaluate(views, "location.href"),
      page_requests: views.requests.filter((r) => shownAfterReload.includes(r.id)).filter(server).map((r) => ({ ...r })),
      data_request_status: status,
      data_request: views.requests.filter((r) => r.url === probeURL),
    };

    // (i) Native same-tab fragment navigation must trigger the page's reload,
    // with the original bootstrap owning exchange and used-link rejection.
    const mint = async () => {
      const response = await fetch(input.origin + "/__test/link", { method: "POST" });
      if (!response.ok) throw new Error("minting a replacement link: " + response.status);
      return response.text();
    };
    const expectReload = () => dt.waitForEvent(
      (message) => message.sessionId === views.sessionId && message.method === "Page.loadEventFired",
      "the page's same-tab link reload"
    );
    let fresh = await mint();
    let since = views.requests.length;
    await evaluate(views, "window.__highbayOldDocument = true");
    let loaded = expectReload();
    await dt.send("Page.navigate", { url: input.origin + "/views#link=" + fresh }, views.sessionId);
    await loaded;
    await until(views, "document.getElementById('highbay-change-list')");
    await settle(views, to(input.origin, "POST", "/project/browser-session"), 2);
    facts.link_recovery = {
      clean_reload: await evaluate(views, "window.__highbayOldDocument === undefined && location.hash === ''"),
      clean_requests: views.requests.slice(since).filter(server).map((r) => ({ ...r })),
      notice_widths: [], notice_fits: [], notice_visible: [], link_announced: [], edit_visible: [],
    };
    await evaluate(views, "document.getElementById('highbay-open-change-chg-refunds').click()");
    await until(views, "document.querySelector('textarea[data-field=expected_outcome]')");
    await evaluate(views, "document.querySelector('textarea[data-field=expected_outcome]').focus()");
    await dt.send("Input.insertText", { text: "Unsent replacement-link edit" }, views.sessionId);
    await evaluate(views, "document.querySelector('textarea[data-field=expected_outcome]').setSelectionRange(2, 8), window.__highbayOldDocument = true");
    fresh = await mint();
    since = views.requests.length;
    await dt.send("Page.navigate", { url: input.origin + "/views#link=" + fresh }, views.sessionId);
    await until(views, "document.getElementById('highbay-pending-link') && location.hash === ''");
    facts.link_recovery.deferred = await evaluate(views, "window.__highbayOldDocument === true && document.getElementById('highbay-open-pending-link').disabled");
    facts.link_recovery.draft_preserved = await evaluate(views, "(() => { const field = document.querySelector('textarea[data-field=expected_outcome]'); return field.value.includes('Unsent replacement-link edit') && document.activeElement === field && field.selectionStart === 2 && field.selectionEnd === 8; })()");
    facts.link_recovery.no_early_exchange = !views.requests.slice(since).some(to(input.origin, "POST", "/project/browser-session"));
    await dt.send("Target.activateTarget", { targetId: views.targetId });
    for (const width of [1280, 375]) {
      await dt.send("Emulation.setDeviceMetricsOverride", { width, height: 800, deviceScaleFactor: 1, mobile: false }, views.sessionId);
      // Start where the person is editing, then paste the link. Only the page
      // may reveal its notice; scrolling to the top here would hide a defect.
      await evaluate(views, "(() => { const field = document.querySelector('textarea[data-field=expected_outcome]'); field.scrollIntoView({ block: 'center' }); field.focus({ preventScroll: true }); field.setSelectionRange(2, 8); })()");
      facts.link_recovery.edit_visible.push(await evaluate(views, "(() => { const box = document.activeElement.getBoundingClientRect(); return box.top >= 0 && box.bottom <= innerHeight; })()"));
      fresh = await mint();
      await dt.send("Page.navigate", { url: input.origin + "/views#link=" + fresh }, views.sessionId);
      await until(views, "document.getElementById('highbay-pending-link') && location.hash === ''");
      await evaluate(views, "new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)))");
      facts.link_recovery.notice_widths.push(width);
      facts.link_recovery.notice_fits.push(await evaluate(views, "(() => { const notice = document.getElementById('highbay-pending-link').getBoundingClientRect(); const button = document.getElementById('highbay-open-pending-link').getBoundingClientRect(); return document.documentElement.scrollWidth <= innerWidth && button.left >= notice.left && button.right <= notice.right; })()"));
      facts.link_recovery.notice_visible.push(await evaluate(views, "(() => { const box = document.getElementById('highbay-pending-link').getBoundingClientRect(); const nav = document.getElementById('highbay-checkpoint-nav'); const top = getComputedStyle(nav).position === 'sticky' ? nav.getBoundingClientRect().bottom : 0; const field = document.querySelector('textarea[data-field=expected_outcome]'); return box.top >= top && box.bottom <= innerHeight && document.activeElement === field && field.selectionStart === 2 && field.selectionEnd === 8; })()"));
      facts.link_recovery.link_announced.push(await evaluate(views, "document.getElementById('highbay-checkpoint-status').textContent.includes('You opened a new Highbay link')"));
      const screenshot = await dt.send("Page.captureScreenshot", { format: "png", captureBeyondViewport: false }, views.sessionId);
      fs.writeFileSync(require("node:path").join(input.capture_directory, "pending-link-" + width + ".png"), Buffer.from(screenshot.data, "base64"));
    }
    await evaluate(views, "document.getElementById('highbay-discard-draft').click()");
    await until(views, "!document.getElementById('highbay-open-pending-link').disabled");
    since = views.requests.length;
    loaded = expectReload();
    await evaluate(views, "document.getElementById('highbay-open-pending-link').click()");
    await loaded;
    await until(views, "document.getElementById('highbay-change-list')");
    await settle(views, to(input.origin, "POST", "/project/browser-session"), 3);
    facts.link_recovery.resolved_reload = await evaluate(views, "window.__highbayOldDocument === undefined && location.hash === ''");
    facts.link_recovery.resolved_requests = views.requests.slice(since).filter(server).map((r) => ({ ...r }));
    loaded = expectReload();
    await dt.send("Page.navigate", { url: input.origin + "/views#link=" + input.link }, views.sessionId);
    await loaded;
    await until(views, "document.getElementById('highbay-link-used')");
    facts.link_recovery.used_link_reported = await evaluate(views, "location.hash === '' && document.getElementById('highbay-link-used').textContent.includes('already used')");
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
