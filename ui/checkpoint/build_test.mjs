// These tests exercise the generated failure consumer with authored OpenAPI
// inputs. Integration tests also exercise the real generated server contract.
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

// consumer builds the actual generated module from a fixture that changes only
// the relevant failure response. A duplicated branch tests oneOf ambiguity.
function consumer(ambiguous = false) {
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
        schema: { oneOf: [typed, ambiguous ? typed : { $ref: "#/components/schemas/Error" }] },
      },
    },
  };
  // C1 introduces drop_build. Keep this generator unit fixture usable before
  // and after its real Goa contract is generated.
  openApi.paths["/changes/{change_id}/build/drop"] = {
    post: { ...build, operationId: "surla#drop_build" },
  };
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

test("oneOf rejects a body that matches two declared alternatives", () => {
  const generated = consumer(true);
  assert.equal(generated.ROUTES.start_build.decodeFailure(409, refusal), false);
});

test("standard faults and session errors retain their existing fallback", () => {
  const generated = consumer();
  for (const status of [409, 401, 500]) {
    assert.equal(generated.ROUTES.start_build.decodeFailure(status, standardError), true);
  }
  assert.equal(generated.ROUTES.start_build.decodeFailure(500, { message: "unvalidated" }), false);
});
