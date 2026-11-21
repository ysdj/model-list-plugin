import assert from "node:assert/strict";
import { createServer } from "node:http";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import test from "node:test";

import {
  buildClaudeProjection,
  buildCodexCatalog,
  defaultConfig,
  disable,
  fetchModels,
  loadConfig,
  mergeModelRows,
  modelsUrl,
  normalizeBaseUrl,
  normalizeModelsResponse,
  pathsFor,
  readModelCatalogPointer,
  removeModelCatalogPointer,
  syncOnce,
  writeModelCatalogPointer,
} from "../plugins/model-list-plugin/scripts/model-list-plugin.mjs";

const SCRIPT_PATH = fileURLToPath(new URL("../plugins/model-list-plugin/scripts/model-list-plugin.mjs", import.meta.url));

test("normalizes provider base URLs to /v1/models", () => {
  assert.equal(normalizeBaseUrl("https://example.test/v1/models"), "https://example.test/v1");
  assert.equal(normalizeBaseUrl("https://example.test/v1/"), "https://example.test/v1");
  assert.equal(modelsUrl("https://example.test"), "https://example.test/v1/models");
  assert.equal(modelsUrl("https://example.test/v1"), "https://example.test/v1/models");
  assert.throws(() => normalizeBaseUrl("example.test/v1"), /absolute http/);
});

test("honors Claude's custom config root for its user settings", () => {
  const locations = pathsFor({
    MODEL_LIST_PLUGIN_DATA: "/tmp/model-list-data",
    CLAUDE_CONFIG_DIR: "/tmp/custom-claude",
    CODEX_HOME: "/tmp/custom-codex",
  });
  assert.equal(locations.claudeSettings, path.resolve("/tmp/custom-claude/settings.json"));
  assert.equal(locations.codexConfig, path.resolve("/tmp/custom-codex/config.toml"));
});

test("preserves an explicit zero sync interval", () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "model-list-plugin-config-"));
  const configPath = path.join(root, "config.json");
  fs.writeFileSync(configPath, JSON.stringify({ syncIntervalSeconds: 0, claudeAliasMode: "invalid" }));
  const config = loadConfig(configPath);
  assert.equal(config.syncIntervalSeconds, 0);
  assert.equal(config.claudeAliasMode, "auto");
});

test("runs its CLI entry point when invoked through a filesystem alias", () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "model-list-plugin-cli-"));
  const aliasPath = path.join(root, "alias.mjs");
  try {
    fs.symlinkSync(SCRIPT_PATH, aliasPath);
  } catch (error) {
    if (error.code === "EPERM" || error.code === "EACCES") return;
    throw error;
  }
  const result = spawnSync(process.execPath, [aliasPath, "status", "--json"], {
    encoding: "utf8",
    env: {
      ...process.env,
      MODEL_LIST_PLUGIN_DATA: path.join(root, "data"),
      MODEL_LIST_PLUGIN_CONFIG: path.join(root, "data", "config.json"),
    },
  });
  assert.equal(result.status, 0, result.stderr);
  assert.equal(JSON.parse(result.stdout).enabled, false);
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

test("unwraps nested catalog payloads without treating image input as generation", () => {
  const models = normalizeModelsResponse({
    modelCatalog: {
      models: [
        { id: "vision-chat", modalities: ["text", "image"] },
        { id: "image-maker", mode: "image_generation" },
      ],
    },
  });
  assert.deepEqual(models.map((model) => [model.id, model.generation]), [
    ["vision-chat", false],
    ["image-maker", true],
  ]);
  assert.deepEqual(buildCodexCatalog(models).models.map((model) => model.slug), ["vision-chat"]);
});

test("does not forward provider credentials to a cross-origin metadata URL", async (t) => {
  let modelAuthorization = "";
  let metadataAuthorization = "";
  const modelServer = createServer((request, response) => {
    modelAuthorization = request.headers.authorization || "";
    response.setHeader("content-type", "application/json");
    response.end(JSON.stringify({ data: [{ id: "remote-model" }] }));
  });
  const metadataServer = createServer((request, response) => {
    metadataAuthorization = request.headers.authorization || "";
    response.setHeader("content-type", "application/json");
    response.end(JSON.stringify({ data: [{ id: "remote-model", context_window: 128000 }] }));
  });
  await Promise.all([
    new Promise((resolve) => modelServer.listen(0, "127.0.0.1", resolve)),
    new Promise((resolve) => metadataServer.listen(0, "127.0.0.1", resolve)),
  ]);
  t.after(() => {
    modelServer.close();
    metadataServer.close();
  });

  const previousKey = process.env.OPENAI_API_KEY;
  process.env.OPENAI_API_KEY = "test-secret";
  try {
    const models = await fetchModels({
      ...defaultConfig(),
      provider: {
        ...defaultConfig().provider,
        kind: "openai",
        authEnv: "OPENAI_API_KEY",
        baseUrl: `http://127.0.0.1:${modelServer.address().port}/v1`,
        metadataUrl: `http://127.0.0.1:${metadataServer.address().port}/modelcatalog/list`,
      },
    });
    assert.equal(models.models[0].contextWindow, 128000);
  } finally {
    if (previousKey === undefined) delete process.env.OPENAI_API_KEY;
    else process.env.OPENAI_API_KEY = previousKey;
  }
  assert.equal(modelAuthorization, "Bearer test-secret");
  assert.equal(metadataAuthorization, "");
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
  assert.equal(buildClaudeProjection(models, { aliasMode: "raw" }).aliases[0], "relay-model");
  assert.equal(
    buildClaudeProjection(models, { aliasMode: "derived" }).aliases[0],
    projection.aliases[0],
  );

  const explicitAlias = normalizeModelsResponse({
    data: [{ id: "relay-model", claude_alias: "claude-custom-model" }],
  });
  assert.equal(buildClaudeProjection(explicitAlias).aliases[0], "claude-custom-model");
  assert.match(buildClaudeProjection(explicitAlias, { aliasMode: "derived" }).aliases[0], /^claude-mythos-/);
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
