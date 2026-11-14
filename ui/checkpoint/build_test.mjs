// These tests exercise the failure consumer from both the unmodified generated
// server contract and authored schemas for additional composition cases.
import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import test from "node:test";
import vm from "node:vm";
import { fileURLToPath } from "node:url";
import { buildSync } from "esbuild";
import { generateTypesModule } from "./build.mjs";

const dirname = path.dirname(fileURLToPath(import.meta.url));
const openApiPath = path.resolve(dirname, "../../gen/http/openapi3.json");
const refusal = {
  name: "previous_change_open",
  message: "Merge or drop the open build before building another change.",
  change_id: "refunds",
  build: { started_at: "2026-11-14T08:00:00Z" },
};
const standardError = {
  name: "conflict", id: "test-conflict", message: "The request was refused.",
  fault: false, temporary: false, timeout: false,
};

// consumer builds the generated module from an authored failure composition.
// The real server contract is exercised separately without these substitutions.
function consumer(extraTyped = false) {
  const openApi = JSON.parse(fs.readFileSync(openApiPath, "utf8"));
  openApi.components.schemas.OpenBuildRefusal = {
    type: "object",
    properties: {
      name: { type: "string" }, message: { type: "string" }, change_id: { type: "string" },
      build: {
        type: "object", properties: { started_at: { type: "string", format: "date-time" } },
        required: ["started_at"],
      },
    },
    required: ["name", "message", "change_id", "build"],
  };
  openApi.components.schemas.OtherRefusal = {
    type: "object",
    properties: {
      name: { type: "string" }, message: { type: "string" }, path: { type: "string" },
    },
    required: ["name", "message", "path"],
  };
  let start;
  let build;
  for (const methods of Object.values(openApi.paths)) {
    for (const operation of Object.values(methods)) {
      if (operation.operationId === "surla#start_build") start = operation;
      if (operation.operationId === "surla#get_build") build = operation;
    }
  }
  assert.ok(start && build, "the page's build operations must be declared");
  const typed = { $ref: "#/components/schemas/OpenBuildRefusal" };
  start.responses["409"] = {
    description: "Typed and standard failures share this status.",
    content: {
      "application/json": {
        schema: { anyOf: [typed, { $ref: `#/components/schemas/${extraTyped ? "OtherRefusal" : "Error"}` }] },
      },
    },
  };
  // C1 introduces drop_build. Keep this generator unit fixture usable before
  // and after its real Goa contract is generated.
  openApi.paths["/changes/{change_id}/build/drop"] = {
    post: { ...build, operationId: "surla#drop_build" },
  };
  return generatedConsumer(openApi);
}

// generatedConsumer bundles the actual emitted validators and route table.
function generatedConsumer(openApi) {
  const generated = generateTypesModule(openApi);
  const bundle = buildSync({
    stdin: { contents: generated, sourcefile: "types.ts", loader: "ts", resolveDir: dirname },
    bundle: true, platform: "node", format: "cjs", write: false,
  });
  const module = { exports: {} };
  vm.runInNewContext(bundle.outputFiles[0].text, { module, exports: module.exports, URL, console });
  return module.exports;
}

test("declared typed failures preserve the exact build identity", () => {
  const generated = consumer();
  const body = structuredClone(refusal);
  const before = structuredClone(body);
  assert.equal(generated.ROUTES.start_build.decodeFailure(409, body), true);
  assert.equal(generated.isOpenBuildRefusal(body), true);
  assert.deepEqual(body, before);
  assert.equal(generated.ROUTES.start_build.decodeFailure(422, refusal), false);
  assert.equal(generated.ROUTES.get_build.decodeFailure(409, refusal), false);
});

test("malformed typed bodies never become usable build refusals", () => {
  const generated = consumer();
  const malformed = [
    { ...refusal, build: undefined },
    { ...refusal, build: { started_at: "yesterday" } },
    { ...refusal, change_id: 5 },
    { ...refusal, extra: true },
  ];
  for (const body of malformed) {
    assert.equal(generated.ROUTES.start_build.decodeFailure(409, body), false);
  }
});

test("same-media anyOf retains distinct typed failure bodies", () => {
  const generated = consumer(true);
  const other = { name: "design_conflict", message: "Correct the file.", path: "design/types.go" };
  assert.equal(generated.ROUTES.start_build.decodeFailure(409, refusal), true);
  assert.equal(generated.ROUTES.start_build.decodeFailure(409, other), true);
  assert.equal(generated.ROUTES.start_build.decodeFailure(409, { ...other, path: undefined }), false);
});

test("standard faults and session errors retain their existing fallback", () => {
  const generated = consumer();
  for (const status of [409, 401, 500]) {
    assert.equal(generated.ROUTES.start_build.decodeFailure(status, standardError), true);
  }
  assert.equal(generated.ROUTES.start_build.decodeFailure(500, { message: "unvalidated" }), false);
});

for (const [operation, name] of [["start_build", "previous_change_open"], ["drop_build", "build_changed"]]) {
  test(`the unmodified server contract retains ${operation}'s typed build refusal`, () => {
    const generated = generatedConsumer(JSON.parse(fs.readFileSync(openApiPath, "utf8")));
    const body = {
      name,
      message: "The request names another build; use this build's exact identity.",
      change_id: "refunds",
      build: {
        request_revision: "agreed-revision",
        started_at: "2026-11-14T08:00:00Z",
        in_force: true,
        builder: { read: false },
        test_writer: { tests: { state: "assigned" }, read: false, report: 0 },
        required_test_count: 1,
      },
    };
    const before = structuredClone(body);
    assert.equal(generated.ROUTES[operation].decodeFailure(409, body), true);
    assert.equal(generated.isOpenBuildRefusal(body), true);
    assert.deepEqual(body, before);
    assert.equal(generated.ROUTES[operation].decodeFailure(422, body), false);
    assert.equal(generated.ROUTES.get_build.decodeFailure(409, body), false);
    const malformed = structuredClone(body);
    delete malformed.build.request_revision;
    assert.equal(generated.ROUTES[operation].decodeFailure(409, malformed), false);
    assert.equal(generated.ROUTES[operation].decodeFailure(409, { ...body, build: undefined }), false);
  });
}
