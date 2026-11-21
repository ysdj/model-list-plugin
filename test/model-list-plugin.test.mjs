import assert from "node:assert/strict";
import { createServer } from "node:http";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";

import {
  buildClaudeProjection,
  buildCodexCatalog,
  defaultConfig,
  disable,
  mergeModelRows,
  modelsUrl,
  normalizeBaseUrl,
  normalizeModelsResponse,
  readModelCatalogPointer,
  removeModelCatalogPointer,
  syncOnce,
  writeModelCatalogPointer,
} from "../plugins/model-list-plugin/scripts/model-list-plugin.mjs";

test("normalizes provider base URLs to /v1/models", () => {
  assert.equal(normalizeBaseUrl("https://example.test/v1/models"), "https://example.test/v1");
  assert.equal(normalizeBaseUrl("https://example.test/v1/"), "https://example.test/v1");
  assert.equal(modelsUrl("https://example.test"), "https://example.test/v1/models");
  assert.equal(modelsUrl("https://example.test/v1"), "https://example.test/v1/models");
  assert.throws(() => normalizeBaseUrl("example.test/v1"), /absolute http/);
});

test("normalizes OpenAI and Anthropic rows and overlays metadata", () => {
  const models = normalizeModelsResponse(
    {
      object: "list",
      data: [
        { id: "deepseek-v4.1-flash", owned_by: "relay" },
        { id: "claude-opus-5-5", display_name: "Claude Opus 5.5" },
      ],
    },
    {
      models: [
        {
          id: "deepseek-v4.1-flash",
          display_name: "DeepSeek Flash",
          context_window: 1048576,
          supported_efforts: ["low", "medium", "high"],
          default_reasoning_level: "medium",
          claude_alias: "claude-mythos-flash",
          behaves_as: "claude-opus-5-5",
          mode: "responses",
        },
      ],
    },
  );
  assert.equal(models.length, 2);
  assert.equal(models[0].label, "DeepSeek Flash");
  assert.equal(models[0].contextWindow, 1048576);
  assert.deepEqual(models[0].reasoning.supported, ["low", "medium", "high"]);
  assert.equal(models[0].oneMillion, true);
  assert.equal(models[0].claudeAlias, "claude-mythos-flash");
  assert.equal(models[1].label, "Claude Opus 5.5");
});

test("accepts a metadata map keyed by public model id", () => {
  const models = normalizeModelsResponse(null, {
    models: {
      "map-model": {
        display_name: "Mapped model",
        contextWindow: 256000,
        reasoning: { supported: ["medium", "max"] },
      },
    },
  });
  assert.equal(models[0].id, "map-model");
  assert.equal(models[0].contextWindow, 256000);
  assert.deepEqual(models[0].reasoning.supported, ["medium", "max"]);
});

test("builds a native-shaped Codex catalog and a Claude picker projection", () => {
  const models = mergeModelRows(
    [{ id: "relay-model" }],
    [{ id: "relay-model", display_name: "Relay Model", context_window: 1048576, supported_efforts: ["medium", "high"] }],
  );
  const native = [{
    slug: "gpt-native",
    display_name: "Native GPT",
    base_instructions: "native instructions",
    shell_type: "unified_exec",
    visibility: "list",
    supported_reasoning_levels: [{ effort: "low" }],
  }];
  const catalog = buildCodexCatalog(models, native);
  assert.equal(catalog.models[0].slug, "relay-model");
  assert.equal(catalog.models[0].display_name, "Relay Model");
  assert.equal(catalog.models[0].context_window, 1048576);
  assert.equal(catalog.models[0].shell_type, "shell_command");
  assert.deepEqual(catalog.models[0].supported_reasoning_levels.map((item) => item.effort), ["medium", "high"]);

  const projection = buildClaudeProjection(models);
  assert.equal(projection.modelPicker.replaceBuiltInOptions, true);
  assert.equal(projection.modelPicker.options[0].label, "Relay Model");
  assert.match(projection.modelPicker.options[0].model, /^claude-mythos-[0-9a-f]+\[1m\]$/);
  assert.equal(projection.modelSettings[projection.aliases[0]].autoCompactWindow, 1048576);
});

test("keeps media generators out of conversation pickers", () => {
  const models = normalizeModelsResponse({
    data: [
      { id: "chat-model", mode: "responses" },
      { id: "image-model", mode: "image_generation" },
    ],
  });
  assert.deepEqual(buildCodexCatalog(models).models.map((model) => model.slug), ["chat-model"]);
  assert.deepEqual(buildClaudeProjection(models).modelPicker.options.map((option) => option.label), ["chat-model"]);
});

test("updates and restores the Codex pointer without losing unrelated TOML", () => {
  const original = "model = \"relay-model\"\n# keep this\n";
  const updated = writeModelCatalogPointer(original, "/tmp/catalog.json");
  assert.equal(readModelCatalogPointer(updated), "/tmp/catalog.json");
  const restored = removeModelCatalogPointer(updated, "/tmp/catalog.json", "");
  assert.equal(restored, original);
  assert.match(updated, /# keep this/);
});

test("syncs a generic local /v1/models provider into isolated target paths", async (t) => {
  const server = createServer((request, response) => {
    if (request.url === "/v1/models") {
      response.setHeader("content-type", "application/json");
      response.end(JSON.stringify({ data: [{ id: "local-model", context_window: 128000, supported_efforts: ["low", "high"] }] }));
      return;
    }
    response.statusCode = 404;
    response.end("{}");
  });
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  t.after(() => server.close());

  const root = fs.mkdtempSync(path.join(os.tmpdir(), "model-list-plugin-test-"));
  const locations = {
    dataRoot: path.join(root, "data"),
    config: path.join(root, "data", "config.json"),
    state: path.join(root, "data", "state.json"),
    lock: path.join(root, "data", "sync.lock"),
    codexHome: path.join(root, "codex"),
    codexConfig: path.join(root, "codex", "config.toml"),
    codexCatalog: path.join(root, "codex", "model-list-plugin-catalog.json"),
    claudeSettings: path.join(root, "claude", "settings.json"),
  };
  const port = server.address().port;
  const config = {
    ...defaultConfig(),
    enabled: true,
    targets: { codex: true, claude: true },
    provider: { ...defaultConfig().provider, baseUrl: `http://127.0.0.1:${port}/v1`, kind: "openai" },
  };
  const result = await syncOnce(config, locations, { version: "0.1.0", managed: {} });
  assert.equal(result.models.length, 1);
  assert.match(fs.readFileSync(locations.codexCatalog, "utf8"), /local-model/);
  const claude = JSON.parse(fs.readFileSync(locations.claudeSettings, "utf8"));
  assert.equal(claude.modelPicker.options.length, 1);
  assert.equal(JSON.parse(fs.readFileSync(locations.state, "utf8")).modelIds[0], "local-model");
});

test("disable restores both host settings after a projection", async (t) => {
  const server = createServer((request, response) => {
    response.setHeader("content-type", "application/json");
    response.end(JSON.stringify({ data: [{ id: "restore-model", context_window: 128000 }] }));
  });
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  t.after(() => server.close());
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "model-list-plugin-restore-"));
  const locations = {
    dataRoot: path.join(root, "data"),
    config: path.join(root, "data", "config.json"),
    state: path.join(root, "data", "state.json"),
    lock: path.join(root, "data", "sync.lock"),
    codexHome: path.join(root, "codex"),
    codexConfig: path.join(root, "codex", "config.toml"),
    codexCatalog: path.join(root, "codex", "model-list-plugin-catalog.json"),
    claudeSettings: path.join(root, "claude", "settings.json"),
  };
  const originalClaude = { model: "claude-sonnet-4-6", modelPicker: { replaceBuiltInOptions: false }, env: { KEEP: "1" } };
  fs.mkdirSync(path.dirname(locations.claudeSettings), { recursive: true });
  fs.writeFileSync(locations.claudeSettings, JSON.stringify(originalClaude));
  const config = {
    ...defaultConfig(),
    enabled: true,
    provider: { ...defaultConfig().provider, baseUrl: `http://127.0.0.1:${server.address().port}/v1` },
  };
  const result = await syncOnce(config, locations, { version: "0.1.2", managed: {} });
  disable({ ...config, enabled: false }, locations, result.state);
  assert.deepEqual(JSON.parse(fs.readFileSync(locations.claudeSettings, "utf8")), originalClaude);
  assert.equal(fs.existsSync(locations.codexCatalog), false);
  assert.equal(fs.existsSync(locations.codexConfig), false);
});
