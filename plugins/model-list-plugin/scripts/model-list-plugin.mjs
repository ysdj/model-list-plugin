#!/usr/bin/env node

/*
 * model-list-plugin
 *
 * This file deliberately uses only Node's standard library.  The host agent
 * supplies Node when it runs a plugin hook, so installing this plugin does not
 * install a daemon, package manager dependency, or a local young-router.
 */

import crypto from "node:crypto";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

export const VERSION = "0.1.4";
export const PLUGIN_NAME = "model-list-plugin";

const DEFAULT_CONFIG = {
  enabled: false,
  targets: { codex: true, claude: true },
  provider: {
    baseUrl: "",
    kind: "auto",
    authEnv: "",
    metadataUrl: "",
    anthropicVersion: "2023-06-01",
  },
  claudeAliasMode: "auto",
  syncIntervalSeconds: 60,
  autoUpdate: true,
  updateCheckHours: 24,
};

const REASONING_LEVELS = new Set([
  "none",
  "minimal",
  "low",
  "medium",
  "high",
  "xhigh",
  "max",
  "ultra",
]);

const REASONING_DESCRIPTIONS = {
  none: "No reasoning",
  minimal: "Minimal reasoning",
  low: "Lighter reasoning",
  medium: "Balanced reasoning",
  high: "Deeper reasoning",
  xhigh: "Extra deep reasoning",
  max: "Maximum reasoning",
  ultra: "Maximum reasoning with delegation",
};

const CONTEXT_KEYS = [
  "context_window",
  "contextWindow",
  "context_length",
  "contextLength",
  "max_context_window",
  "maxContextWindow",
  "max_context_tokens",
  "maxContextTokens",
  "max_input_tokens",
  "maxInputTokens",
  "input_token_limit",
  "inputTokenLimit",
  "context",
  "input",
];

function clone(value) {
  return value === undefined ? undefined : JSON.parse(JSON.stringify(value));
}

function isObject(value) {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function nonEmptyString(...values) {
  for (const value of values) {
    if (typeof value === "string" && value.trim()) return value.trim();
  }
  return "";
}

function positiveNumber(value) {
  if (typeof value === "number" && Number.isFinite(value) && value > 0) {
    return Math.round(value);
  }
  if (typeof value === "string" && value.trim()) {
    const parsed = Number(value.replaceAll(",", "").trim());
    if (Number.isFinite(parsed) && parsed > 0) return Math.round(parsed);
  }
  return 0;
}

function nestedObjects(raw) {
  const result = [raw];
  for (const key of [
    "metadata",
    "model_info",
    "modelInfo",
    "capabilities",
    "context",
    "model_context",
    "modelContext",
    "limit",
    "limits",
  ]) {
    if (isObject(raw?.[key])) result.push(raw[key]);
  }
  return result;
}

function firstPositiveNumber(raw, keys) {
  for (const object of nestedObjects(raw)) {
    for (const key of keys) {
      const value = positiveNumber(object?.[key]);
      if (value) return value;
    }
  }
  return 0;
}

function asArray(value) {
  if (Array.isArray(value)) return value;
  if (typeof value === "string" && value.trim()) {
    return value.split(/[,\s]+/).filter(Boolean);
  }
  return [];
}

function normalizeReasoning(raw) {
  const candidates = [];
  for (const object of nestedObjects(raw)) {
    const reasoning = object?.reasoning;
    if (isObject(reasoning)) {
      candidates.push(...asArray(reasoning.supported));
      candidates.push(...asArray(reasoning.supported_efforts));
      candidates.push(...asArray(reasoning.supportedEfforts));
      candidates.push(...asArray(reasoning.efforts));
      candidates.push(...asArray(reasoning.levels));
      candidates.push(...asArray(reasoning.supported_levels));
      candidates.push(...asArray(reasoning.supportedLevels));
      candidates.push(...asArray(reasoning.effort));
      candidates.push(...asArray(reasoning.level));
    } else {
      candidates.push(...asArray(reasoning));
    }
    candidates.push(...asArray(object?.supported_efforts));
    candidates.push(...asArray(object?.supportedEfforts));
    candidates.push(...asArray(object?.supported_reasoning_levels));
    candidates.push(...asArray(object?.supportedReasoningLevels));
    candidates.push(...asArray(object?.reasoning_efforts));
    candidates.push(...asArray(object?.reasoningEfforts));
    candidates.push(...asArray(object?.reasoning_effort));
    candidates.push(...asArray(object?.reasoningEffort));
    candidates.push(...asArray(object?.supported_reasoning_efforts));
    candidates.push(...asArray(object?.supportedReasoningEfforts));
    candidates.push(...asArray(object?.reasoning_levels));
    candidates.push(...asArray(object?.reasoningLevels));
    if (object?.reasoning === true || object?.supports_reasoning === true) {
      candidates.push("low", "medium", "high");
    }
  }

  const levels = [];
  for (const candidate of candidates) {
    const value = isObject(candidate)
      ? nonEmptyString(candidate.effort, candidate.level, candidate.name, candidate.id, candidate.value)
      : String(candidate);
    const normalized = value.trim().toLowerCase();
    if (REASONING_LEVELS.has(normalized) && !levels.includes(normalized)) levels.push(normalized);
  }

  const order = ["none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"];
  levels.sort((left, right) => order.indexOf(left) - order.indexOf(right));
  let defaultLevel = "";
  for (const object of nestedObjects(raw)) {
    const reasoning = isObject(object?.reasoning) ? object.reasoning : {};
    defaultLevel = nonEmptyString(
      object?.default_reasoning_level,
      object?.defaultReasoningLevel,
      object?.default_effort,
      object?.defaultEffort,
      reasoning.default,
      reasoning.default_effort,
      reasoning.defaultEffort,
      reasoning.effort,
      reasoning.level,
    ).toLowerCase();
    if (defaultLevel) break;
  }
  if (!REASONING_LEVELS.has(defaultLevel)) defaultLevel = levels[0] || "";
  return { supported: levels, default: defaultLevel };
}

function extractRows(payload) {
  return extractRowsAtDepth(payload, 0);
}

function extractRowsAtDepth(payload, depth) {
  if (Array.isArray(payload)) return payload.filter(isObject);
  if (!isObject(payload) || depth > 4) return [];
  for (const key of ["data", "models", "items", "results", "model_catalog", "modelCatalog", "catalog"]) {
    const value = payload[key];
    if (Array.isArray(value)) return value.filter(isObject);
    if (isObject(value)) {
      const nested = extractRowsAtDepth(value, depth + 1);
      if (nested.length) return nested;
      return Object.entries(value).map(([id, entry]) => ({
        ...(isObject(entry) ? entry : {}),
        id: rowId(entry) || id,
      }));
    }
  }
  if (isObject(payload.modelsById)) {
    return Object.entries(payload.modelsById).map(([id, value]) => ({
      ...(isObject(value) ? value : {}),
      id: rowId(value) || id,
    }));
  }
  return rowId(payload) ? [payload] : [];
}

function rowId(raw) {
  return nonEmptyString(raw?.id, raw?.model, raw?.slug, raw?.public_name, raw?.publicName, raw?.name);
}

function generationMode(raw) {
  const values = [];
  for (const object of nestedObjects(raw)) {
    values.push(...asArray(object?.mode));
    values.push(...asArray(object?.modes));
    values.push(...asArray(object?.task));
    values.push(...asArray(object?.type));
    values.push(...asArray(object?.model_type));
    values.push(...asArray(object?.modelType));
    values.push(...asArray(object?.generation_type));
    values.push(...asArray(object?.generationType));
    values.push(...asArray(object?.output_modalities));
    values.push(...asArray(object?.outputModalities));
    if (object?.image_generation === true || object?.imageGeneration === true) values.push("image");
    if (object?.video_generation === true || object?.videoGeneration === true) values.push("video");
  }
  return [...new Set(values.map((value) => String(value).toLowerCase()))];
}

export function normalizeModel(raw) {
  if (!isObject(raw)) return null;
  const id = rowId(raw);
  if (!id) return null;
  const contextWindow = firstPositiveNumber(raw, CONTEXT_KEYS);
  const maxContextWindow = firstPositiveNumber(raw, [
    "max_context_window",
    "maxContextWindow",
    "max_context_tokens",
    "maxContextTokens",
    "context_window",
    "contextWindow",
  ]) || contextWindow;
  const effectivePercent = positiveNumber(
    raw.effective_context_window_percent ?? raw.effectiveContextWindowPercent ?? raw.metadata?.effective_context_window_percent,
  );
  const reasoning = normalizeReasoning(raw);
  const modes = generationMode(raw);
  const lowerId = id.toLowerCase();
  const oneMillion =
    /\[1m\]|1m(?:[-_ ]?context)?/.test(lowerId) || contextWindow >= 900_000 || maxContextWindow >= 900_000;
  const label = nonEmptyString(raw.display_name, raw.displayName, raw.label, raw.name, raw.metadata?.display_name, id);
  const description = nonEmptyString(raw.description, raw.metadata?.description);
  const alias = nonEmptyString(
    raw.claude_alias,
    raw.claudeAlias,
    raw.anthropic_alias,
    raw.anthropicAlias,
    raw.alias,
    raw.metadata?.claude_alias,
    raw.metadata?.claudeAlias,
  );
  const behavesAs = nonEmptyString(raw.behaves_as, raw.behavesAs, raw.metadata?.behaves_as, raw.metadata?.behavesAs);
  return {
    id,
    label,
    description,
    ownedBy: nonEmptyString(raw.owned_by, raw.ownedBy),
    modes,
    generation: modes.some((mode) => mode.includes("image") || mode.includes("video")),
    imageGeneration: modes.some((mode) => mode.includes("image")),
    videoGeneration: modes.some((mode) => mode.includes("video")),
    contextWindow,
    maxContextWindow,
    effectiveContextWindowPercent: effectivePercent,
    reasoning,
    claudeAlias: alias,
    behavesAs,
    oneMillion,
    source: clone(raw),
  };
}

export function normalizeModels(rows) {
  const byId = new Map();
  for (const row of rows || []) {
    const model = normalizeModel(row);
    if (model && !byId.has(model.id)) byId.set(model.id, model);
  }
  return [...byId.values()];
}

export function mergeModelRows(listRows, metadataRows) {
  const list = normalizeModels(listRows);
  const metadata = normalizeModels(metadataRows);
  if (!metadata.length) return list;
  const metadataById = new Map(metadata.map((model) => [model.id, model]));
  const metadataByName = new Map();
  for (const model of metadata) {
    for (const key of [model.id, model.label]) metadataByName.set(key.toLowerCase(), model);
  }
  const merged = list.map((model) => {
    const match = metadataById.get(model.id) || metadataByName.get(model.id.toLowerCase());
    if (!match) return model;
    return normalizeModel({
      ...model.source,
      ...match.source,
      id: model.id,
      display_name: match.label || model.label,
      description: match.description || model.description,
      context_window: match.contextWindow || model.contextWindow,
      max_context_window: match.maxContextWindow || model.maxContextWindow,
      effective_context_window_percent:
        match.effectiveContextWindowPercent || model.effectiveContextWindowPercent,
      supported_efforts: match.reasoning.supported.length ? match.reasoning.supported : model.reasoning.supported,
      default_reasoning_level: match.reasoning.default || model.reasoning.default,
      claude_alias: match.claudeAlias || model.claudeAlias,
      behaves_as: match.behavesAs || model.behavesAs,
      mode: match.modes.length ? match.modes : model.modes,
    });
  });
  const listIds = new Set(list.map((model) => model.id));
  for (const model of metadata) if (!listIds.has(model.id)) merged.push(model);
  return merged;
}

export function normalizeModelsResponse(payload, metadataPayload = null) {
  return mergeModelRows(extractRows(payload), extractRows(metadataPayload));
}

export function pathsFor(env = process.env) {
  const claudeConfigRoot = path.resolve(
    nonEmptyString(env.CLAUDE_CONFIG_DIR, path.join(os.homedir(), ".claude")),
  );
  const dataRoot = path.resolve(
    nonEmptyString(
      env.MODEL_LIST_PLUGIN_DATA,
      env.CLAUDE_PLUGIN_DATA,
      env.PLUGIN_DATA,
      process.platform === "win32"
        ? path.join(env.APPDATA || os.homedir(), PLUGIN_NAME)
        : process.platform === "darwin"
          ? path.join(os.homedir(), "Library", "Application Support", PLUGIN_NAME)
          : path.join(env.XDG_CONFIG_HOME || path.join(os.homedir(), ".config"), PLUGIN_NAME),
    ),
  );
  return {
    dataRoot,
    config: path.resolve(nonEmptyString(env.MODEL_LIST_PLUGIN_CONFIG, path.join(dataRoot, "config.json"))),
    state: path.join(dataRoot, "state.json"),
    lock: path.join(dataRoot, "sync.lock"),
    codexHome: path.resolve(nonEmptyString(env.CODEX_HOME, path.join(os.homedir(), ".codex"))),
    codexConfig: path.join(path.resolve(nonEmptyString(env.CODEX_HOME, path.join(os.homedir(), ".codex"))), "config.toml"),
    codexCatalog: path.join(path.resolve(nonEmptyString(env.CODEX_HOME, path.join(os.homedir(), ".codex"))), `${PLUGIN_NAME}-catalog.json`),
    claudeSettings: path.resolve(
      nonEmptyString(
        env.MODEL_LIST_PLUGIN_CLAUDE_SETTINGS,
        path.join(claudeConfigRoot, "settings.json"),
      ),
    ),
  };
}

function ensureDirectory(filePath) {
  fs.mkdirSync(path.dirname(filePath), { recursive: true });
}

function readJson(filePath, fallback) {
  try {
    return JSON.parse(fs.readFileSync(filePath, "utf8"));
  } catch {
    return clone(fallback);
  }
}

function writeJson(filePath, payload) {
  ensureDirectory(filePath);
  const temporary = `${filePath}.${process.pid}.${Date.now()}.tmp`;
  fs.writeFileSync(temporary, `${JSON.stringify(payload, null, 2)}\n`, { mode: 0o600 });
  fs.renameSync(temporary, filePath);
}

function readText(filePath) {
  try {
    return fs.readFileSync(filePath, "utf8");
  } catch {
    return null;
  }
}

function writeText(filePath, text) {
  ensureDirectory(filePath);
  const temporary = `${filePath}.${process.pid}.${Date.now()}.tmp`;
  fs.writeFileSync(temporary, text, { mode: 0o600 });
  fs.renameSync(temporary, filePath);
}

function removeFile(filePath) {
  try {
    fs.unlinkSync(filePath);
  } catch (error) {
    if (error.code !== "ENOENT") throw error;
  }
}

function captureFiles(filePaths) {
  return filePaths.map((filePath) => {
    try {
      return { filePath, exists: true, contents: fs.readFileSync(filePath) };
    } catch (error) {
      if (error.code === "ENOENT") return { filePath, exists: false, contents: null };
      throw error;
    }
  });
}

function restoreFiles(snapshots) {
  for (const snapshot of snapshots) {
    if (snapshot.exists) {
      ensureDirectory(snapshot.filePath);
      fs.writeFileSync(snapshot.filePath, snapshot.contents, { mode: 0o600 });
    } else {
      removeFile(snapshot.filePath);
    }
  }
}

export function defaultConfig() {
  return clone(DEFAULT_CONFIG);
}

export function loadConfig(filePath = pathsFor().config) {
  const value = readJson(filePath, DEFAULT_CONFIG);
  const config = {
    ...clone(DEFAULT_CONFIG),
    ...value,
    targets: { ...DEFAULT_CONFIG.targets, ...(isObject(value?.targets) ? value.targets : {}) },
    provider: { ...DEFAULT_CONFIG.provider, ...(isObject(value?.provider) ? value.provider : {}) },
  };
  config.enabled = config.enabled === true;
  config.syncIntervalSeconds = Math.max(0, positiveNumber(config.syncIntervalSeconds) || DEFAULT_CONFIG.syncIntervalSeconds);
  config.updateCheckHours = Math.max(1, positiveNumber(config.updateCheckHours) || DEFAULT_CONFIG.updateCheckHours);
  config.autoUpdate = config.autoUpdate !== false;
  if (!["auto", "raw", "derived"].includes(config.claudeAliasMode)) config.claudeAliasMode = "auto";
  return config;
}

function saveConfig(config, filePath = pathsFor().config) {
  writeJson(filePath, config);
}

function loadState(filePath = pathsFor().state) {
  return readJson(filePath, { version: VERSION, managed: {} });
}

function saveState(state, filePath = pathsFor().state) {
  writeJson(filePath, state);
}

export function normalizeBaseUrl(input) {
  const value = String(input || "").trim();
  if (!value) return "";
  let url;
  try {
    url = new URL(value);
  } catch {
    throw new Error("baseUrl must be an absolute http(s) URL");
  }
  if (!/^https?:$/.test(url.protocol)) throw new Error("baseUrl must use http or https");
  url.search = "";
  url.hash = "";
  url.pathname = url.pathname.replace(/\/+$/, "");
  if (url.pathname.endsWith("/models")) url.pathname = url.pathname.slice(0, -"/models".length);
  return url.toString().replace(/\/$/, "");
}

export function modelsUrl(baseUrl) {
  const base = normalizeBaseUrl(baseUrl);
  if (!base) return "";
  return `${base}${base.endsWith("/v1") ? "" : "/v1"}/models`;
}

function safeUrlForError(value) {
  try {
    const url = new URL(value);
    url.search = "";
    url.hash = "";
    return url.toString();
  } catch {
    return "configured endpoint";
  }
}

function hostSettingEnv(name) {
  const direct = nonEmptyString(process.env[name]);
  if (direct) return direct;
  const settings = readJson(pathsFor().claudeSettings, {});
  return nonEmptyString(settings?.env?.[name]);
}

function resolveProvider(config) {
  const provider = config.provider || {};
  const configuredKind = nonEmptyString(provider.kind, "auto").toLowerCase();
  const baseUrl = normalizeBaseUrl(
    provider.baseUrl ||
      process.env.MODEL_LIST_PLUGIN_BASE_URL ||
      hostSettingEnv(configuredKind === "anthropic" ? "ANTHROPIC_BASE_URL" : "OPENAI_BASE_URL") ||
      hostSettingEnv("OPENAI_BASE_URL") ||
      hostSettingEnv("ANTHROPIC_BASE_URL") ||
      process.env.YOUNG_ROUTER_BASE_URL ||
      "",
  );
  const inferredAnthropic = configuredKind === "anthropic" ||
    (configuredKind === "auto" && Boolean(hostSettingEnv("ANTHROPIC_AUTH_TOKEN") || hostSettingEnv("ANTHROPIC_API_KEY")) && !hostSettingEnv("OPENAI_API_KEY"));
  const defaultAuthEnv = inferredAnthropic
    ? (hostSettingEnv("ANTHROPIC_AUTH_TOKEN") ? "ANTHROPIC_AUTH_TOKEN" : "ANTHROPIC_API_KEY")
    : "OPENAI_API_KEY";
  const authEnv = nonEmptyString(
    provider.authEnv,
    process.env.MODEL_LIST_PLUGIN_AUTH_ENV,
    defaultAuthEnv,
  );
  const kind = configuredKind;
  const isAnthropic = kind === "anthropic" ||
    (kind === "auto" && (/anthropic/i.test(authEnv) || inferredAnthropic));
  return {
    baseUrl,
    modelsUrl: modelsUrl(baseUrl),
    metadataUrl: nonEmptyString(provider.metadataUrl, process.env.MODEL_LIST_PLUGIN_METADATA_URL),
    kind: isAnthropic ? "anthropic" : "openai",
    authEnv,
    token: hostSettingEnv(authEnv),
    anthropicVersion: nonEmptyString(provider.anthropicVersion, "2023-06-01"),
  };
}

async function fetchJson(endpoint, provider, timeoutMs = 10_000) {
  if (!endpoint) throw new Error("provider baseUrl is not configured");
  const headers = { Accept: "application/json" };
  if (provider.token) {
    if (provider.kind === "anthropic") {
      headers["x-api-key"] = provider.token;
      headers["anthropic-version"] = provider.anthropicVersion;
    } else {
      headers.Authorization = `Bearer ${provider.token}`;
    }
  }
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);
  try {
    const response = await fetch(endpoint, { headers, signal: controller.signal, redirect: "error" });
    const text = await response.text();
    let payload;
    try {
      payload = JSON.parse(text);
    } catch {
      throw new Error(`endpoint returned non-JSON (${response.status})`);
    }
    if (!response.ok) throw new Error(`endpoint returned HTTP ${response.status}`);
    return payload;
  } catch (error) {
    if (error?.name === "AbortError") throw new Error(`request timed out: ${safeUrlForError(endpoint)}`);
    if (error instanceof Error && /endpoint returned|provider/.test(error.message)) throw error;
    throw new Error(`request failed: ${safeUrlForError(endpoint)}`);
  } finally {
    clearTimeout(timer);
  }
}

function sameOrigin(left, right) {
  try {
    return new URL(left).origin === new URL(right).origin;
  } catch {
    return false;
  }
}

export async function fetchModels(config) {
  const provider = resolveProvider(config);
  if (!provider.modelsUrl && !provider.metadataUrl) throw new Error("configure provider.baseUrl before enabling the plugin");
  const listPayload = provider.modelsUrl ? await fetchJson(provider.modelsUrl, provider) : null;
  const metadataProvider =
    !provider.modelsUrl || sameOrigin(provider.modelsUrl, provider.metadataUrl)
      ? provider
      : { ...provider, token: "" };
  const metadataPayload = provider.metadataUrl
    ? await fetchJson(provider.metadataUrl, metadataProvider)
    : null;
  const models = normalizeModelsResponse(listPayload, metadataPayload);
  if (!models.length) throw new Error("provider returned no usable models");
  return { models, provider };
}

function nativeCodexCandidates() {
  const candidates = [];
  const add = (value) => {
    if (value && !candidates.includes(value)) candidates.push(value);
  };
  add(process.env.YOUNG_ROUTER_CODEX_EXECUTABLE);
  add("codex");
  add("codex-cli");
  if (process.platform === "win32") {
    for (const root of [process.env.LOCALAPPDATA, process.env.APPDATA, process.env.ProgramFiles, process.env["ProgramFiles(x86)"]]) {
      if (!root) continue;
      add(path.join(root, "Programs", "ChatGPT", "resources", "codex.exe"));
      add(path.join(root, "ChatGPT", "resources", "codex.exe"));
      add(path.join(root, "Codex", "codex.exe"));
    }
  } else if (process.platform === "darwin") {
    for (const root of ["/Applications", path.join(os.homedir(), "Applications")]) {
      add(path.join(root, "ChatGPT.app", "Contents", "Resources", "codex"));
      add(path.join(root, "Codex.app", "Contents", "Resources", "codex"));
    }
  }
  return candidates;
}

export function readNativeCodexCatalog() {
  for (const executable of nativeCodexCandidates()) {
    try {
      const result = spawnSync(executable, ["debug", "models", "--bundled"], {
        encoding: "utf8",
        timeout: 2500,
        maxBuffer: 16 * 1024 * 1024,
        windowsHide: true,
      });
      if (result.status !== 0 || !result.stdout) continue;
      const payload = JSON.parse(result.stdout);
      if (Array.isArray(payload?.models)) return payload.models.filter(isObject);
    } catch {
      // A native catalog is an enhancement.  The fallback profile below keeps
      // the plugin usable on machines that have no Codex executable on PATH.
    }
  }
  return [];
}

function firstNativeProfile(nativeModels) {
  return nativeModels.find((model) => {
    const hasInstructions = Boolean(model?.base_instructions || model?.model_messages?.instructions_template);
    return hasInstructions && String(model.visibility || "list").toLowerCase() === "list";
  }) || nativeModels.find(isObject) || null;
}

function nativeProfileFor(id, nativeModels, base) {
  const lowered = id.toLowerCase();
  const candidates = [lowered];
  for (const separator of ["/", "@"]) {
    if (lowered.includes(separator)) candidates.push(lowered.split(separator).at(-1));
  }
  return nativeModels.find((model) => candidates.includes(String(model.slug || "").toLowerCase())) || base;
}

function fallbackCodexProfile(id) {
  return {
    slug: id,
    display_name: id,
    description: "Model exposed by the configured OpenAI/Anthropic compatible endpoint.",
    default_reasoning_level: "medium",
    supported_reasoning_levels: ["low", "medium", "high"].map((effort) => ({ effort, description: REASONING_DESCRIPTIONS[effort] })),
    shell_type: "shell_command",
    tool_mode: "direct",
    visibility: "list",
    supported_in_api: true,
    base_instructions: "You are Codex, a coding agent.",
    support_verbosity: true,
    supports_parallel_tool_calls: true,
  };
}

function reasoningObjects(model) {
  return model.reasoning.supported.map((effort) => ({
    effort,
    description: REASONING_DESCRIPTIONS[effort] || `${effort} reasoning`,
  }));
}

export function buildCodexCatalog(models, nativeModels = []) {
  const base = firstNativeProfile(nativeModels);
  return {
    models: models.filter((model) => !model.generation).map((model, index) => {
      const exact = nativeProfileFor(model.id, nativeModels, base);
      const profile = clone(exact || fallbackCodexProfile(model.id));
      profile.slug = model.id;
      profile.display_name = model.label || model.id;
      profile.description = model.description || profile.description || "Model exposed by the configured endpoint.";
      profile.priority = index + 1;
      profile.visibility = "list";
      profile.supported_in_api = true;
      profile.supports_parallel_tool_calls = profile.supports_parallel_tool_calls !== false;
      if (!exact || exact.slug !== model.id) {
        profile.shell_type = "shell_command";
        profile.tool_mode = "direct";
        delete profile.multi_agent_reasoning_effort;
      }
      if (model.contextWindow) profile.context_window = model.contextWindow;
      if (model.maxContextWindow) profile.max_context_window = model.maxContextWindow;
      if (model.effectiveContextWindowPercent) profile.effective_context_window_percent = model.effectiveContextWindowPercent;
      if (model.generation) {
        profile.supported_reasoning_levels = [{ effort: "none", description: REASONING_DESCRIPTIONS.none }];
        profile.default_reasoning_level = "none";
      } else if (model.reasoning.supported.length) {
        profile.supported_reasoning_levels = reasoningObjects(model);
        profile.default_reasoning_level = model.reasoning.default || model.reasoning.supported[0];
      }
      return profile;
    }),
  };
}

function tomlQuote(value) {
  return `"${String(value).replaceAll("\\", "\\\\").replaceAll('"', '\\"')}"`;
}

function tomlUnquote(value) {
  const match = String(value || "").trim().match(/^"((?:\\.|[^"\\])*)"|'([^']*)'/);
  if (!match) return "";
  return (match[1] ?? match[2]).replaceAll('\\"', '"').replaceAll("\\\\", "\\");
}

export function readModelCatalogPointer(contents) {
  const match = String(contents || "").match(/^[ \t]*model_catalog_json[ \t]*=[ \t]*([^\r\n#]+)(?:[ \t]+#.*)?$/m);
  return match ? tomlUnquote(match[1]) : "";
}

export function writeModelCatalogPointer(contents, pointer) {
  const line = `model_catalog_json = ${tomlQuote(pointer)}`;
  const source = String(contents || "");
  const pattern = /^[ \t]*model_catalog_json[ \t]*=[^\r\n]*(?:\r?\n|$)/m;
  if (pattern.test(source)) return source.replace(pattern, line);
  return `${source.replace(/\s*$/, "")}\n${line}\n`;
}

export function removeModelCatalogPointer(contents, managedPath, originalPointer = "") {
  const source = String(contents || "");
  const current = readModelCatalogPointer(source);
  if (current && path.resolve(current) !== path.resolve(managedPath)) return source;
  if (originalPointer) return writeModelCatalogPointer(source, originalPointer);
  return source.replace(/^[ \t]*model_catalog_json[ \t]*=[^\r\n]*(?:\r?\n|$)/m, "");
}

function aliasFor(model, used, mode = "auto") {
  if (mode === "raw") return model.id;
  const explicit = mode === "derived" ? "" : model.claudeAlias.replace(/\[1m\]$/, "").trim();
  let base = explicit || `claude-mythos-${crypto.createHash("sha256").update(model.id).digest("hex").slice(0, 6)}`;
  if (!/^claude-[a-z0-9-]+$/i.test(base)) base = `claude-mythos-${crypto.createHash("sha256").update(model.id).digest("hex").slice(0, 6)}`;
  let candidate = base + (model.oneMillion ? "[1m]" : "");
  if (used.has(candidate)) {
    base = `claude-mythos-${crypto.createHash("sha256").update(model.id).digest("hex").slice(0, 10)}`;
    candidate = base + (model.oneMillion ? "[1m]" : "");
  }
  used.add(candidate);
  return candidate;
}

function behavesAsFor(model) {
  if (model.behavesAs) return model.behavesAs;
  const lower = model.id.toLowerCase();
  if (lower.includes("opus")) return "claude-opus-5-5";
  if (lower.includes("sonnet")) return "claude-sonnet-4-6";
  if (model.generation) return "claude-sonnet-4-6";
  const highest = model.reasoning.supported.at(-1);
  return highest === "high" || highest === "xhigh" || highest === "max" || highest === "ultra"
    ? "claude-opus-5-5"
    : "claude-sonnet-4-6";
}

export function buildClaudeProjection(models, options = {}) {
  const aliasMode = ["auto", "raw", "derived"].includes(options.aliasMode) ? options.aliasMode : "auto";
  const used = new Set();
  const pickerOptions = [];
  const modelSettings = {};
  const aliases = [];
  for (const model of models.filter((candidate) => !candidate.generation)) {
    const alias = aliasFor(model, used, aliasMode);
    aliases.push(alias);
    const option = {
      model: alias,
      label: model.label || model.id,
      description: model.description || `Remote model ${model.id}`,
      behavesAs: behavesAsFor(model),
    };
    pickerOptions.push(option);
    const context = model.oneMillion ? Math.max(model.contextWindow, 1_000_000) : model.contextWindow;
    if (context) modelSettings[alias] = { autoCompactWindow: context };
  }
  return {
    modelPicker: { replaceBuiltInOptions: true, options: pickerOptions },
    modelSettings,
    aliases,
  };
}

function settingsProjection(current, projection, state) {
  const next = isObject(current) ? clone(current) : {};
  if (!state.managed?.claudeSnapshot) state.managed = { ...(state.managed || {}), claudeSnapshot: clone(current || {}) };
  next.modelPicker = projection.modelPicker;
  const previous = isObject(next.modelSettings) ? next.modelSettings : {};
  next.modelSettings = { ...previous, ...projection.modelSettings };
  for (const alias of state.managed?.claudeAliases || []) {
    if (!projection.modelSettings[alias]) delete next.modelSettings[alias];
  }
  return next;
}

function restoreClaudeSettings(current, state) {
  const next = isObject(current) ? clone(current) : {};
  const snapshot = state.managed?.claudeSnapshot;
  if (snapshot) {
    if (snapshot.modelPicker === undefined) delete next.modelPicker;
    else next.modelPicker = clone(snapshot.modelPicker);
    if (snapshot.modelSettings === undefined) delete next.modelSettings;
    else next.modelSettings = clone(snapshot.modelSettings);
    if (snapshot.model === undefined) delete next.model;
    else next.model = snapshot.model;
  } else {
    delete next.modelPicker;
    if (isObject(next.modelSettings)) {
      for (const alias of state.managed?.claudeAliases || []) delete next.modelSettings[alias];
    }
  }
  return next;
}

function applyCodexProjection(models, locations, state) {
  const existingConfig = readText(locations.codexConfig);
  const hadConfig = existingConfig !== null;
  const source = existingConfig || "";
  const currentPointer = readModelCatalogPointer(source);
  if (!state.managed?.codex) {
    state.managed = {
      ...(state.managed || {}),
      codex: { originalPointer: currentPointer, hadConfig, managedPath: locations.codexCatalog },
    };
  }
  const native = readNativeCodexCatalog();
  const catalog = buildCodexCatalog(models, native);
  if (!catalog.models.length) throw new Error("provider returned no conversation-capable models");
  writeJson(locations.codexCatalog, catalog);
  writeText(locations.codexConfig, writeModelCatalogPointer(source, locations.codexCatalog));
}

function disableCodexProjection(locations, state) {
  const managed = state.managed?.codex;
  if (!managed) {
    removeFile(locations.codexCatalog);
    return;
  }
  const current = readText(locations.codexConfig);
  if (current !== null) {
    const next = removeModelCatalogPointer(current, managed.managedPath || locations.codexCatalog, managed.originalPointer || "");
    if (next.trim()) writeText(locations.codexConfig, next);
    else if (!managed.hadConfig) removeFile(locations.codexConfig);
  }
  removeFile(managed.managedPath || locations.codexCatalog);
}

function applyClaudeProjection(models, locations, state, config) {
  const current = readJson(locations.claudeSettings, {});
  const projection = buildClaudeProjection(models, { aliasMode: config.claudeAliasMode });
  if (!projection.modelPicker.options.length) throw new Error("provider returned no conversation-capable models");
  const next = settingsProjection(current, projection, state);
  state.managed = { ...(state.managed || {}), claudeAliases: projection.aliases };
  writeJson(locations.claudeSettings, next);
}

function disableClaudeProjection(locations, state) {
  const current = readJson(locations.claudeSettings, {});
  writeJson(locations.claudeSettings, restoreClaudeSettings(current, state));
}

function acquireLock(lockPath) {
  ensureDirectory(lockPath);
  try {
    const descriptor = fs.openSync(lockPath, "wx");
    fs.writeFileSync(descriptor, `${process.pid}\n`);
    return descriptor;
  } catch (error) {
    if (error.code !== "EEXIST") throw error;
    try {
      const age = Date.now() - fs.statSync(lockPath).mtimeMs;
      if (age > 120_000) {
        removeFile(lockPath);
        const descriptor = fs.openSync(lockPath, "wx");
        fs.writeFileSync(descriptor, `${process.pid}\n`);
        return descriptor;
      }
    } catch {
      // Another hook may be creating or removing the lock.  Treat it as busy.
    }
    return null;
  }
}

function releaseLock(lockPath, descriptor) {
  if (descriptor === null || descriptor === undefined) return;
  try { fs.closeSync(descriptor); } catch {}
  removeFile(lockPath);
}

function targetEnabled(config, target) {
  return config.targets?.[target] !== false;
}

function hostConfig(config, host) {
  if (host !== "codex" && host !== "claude") return config;
  return {
    ...config,
    targets: {
      codex: host === "codex" && targetEnabled(config, "codex"),
      claude: host === "claude" && targetEnabled(config, "claude"),
    },
  };
}

export async function syncOnce(config, locations = pathsFor(), state = loadState(locations.state)) {
  const descriptor = acquireLock(locations.lock);
  if (descriptor === null) return { skipped: true, reason: "sync already running", state };
  try {
    const { models, provider } = await fetchModels(config);
    const previousState = clone(state);
    const fileSnapshots = captureFiles([
      locations.codexCatalog,
      locations.codexConfig,
      locations.claudeSettings,
    ]);
    try {
      if (targetEnabled(config, "codex")) applyCodexProjection(models, locations, state);
      if (targetEnabled(config, "claude")) applyClaudeProjection(models, locations, state, config);
    } catch (error) {
      // A failed projection must not replace a previously usable catalog.  The
      // endpoint was already validated and the per-file writes are atomic, so
      // keeping the old state is the safest recovery path for the next hook.
      Object.assign(state, previousState);
      restoreFiles(fileSnapshots);
      throw error;
    }
    state.version = VERSION;
    state.lastSyncAt = new Date().toISOString();
    state.lastSyncAtByTarget = { ...(state.lastSyncAtByTarget || {}) };
    for (const target of ["codex", "claude"]) {
      if (targetEnabled(config, target)) state.lastSyncAtByTarget[target] = state.lastSyncAt;
    }
    state.lastError = "";
    state.modelIds = models.map((model) => model.id);
    state.provider = { baseUrl: provider.baseUrl, kind: provider.kind, metadataUrl: provider.metadataUrl };
    saveState(state, locations.state);
    return { models, state };
  } catch (error) {
    state.lastError = error instanceof Error ? error.message : String(error);
    saveState(state, locations.state);
    throw error;
  } finally {
    releaseLock(locations.lock, descriptor);
  }
}

export function disable(config, locations = pathsFor(), state = loadState(locations.state)) {
  // Cleanup follows the state this plugin wrote, so changing `targets` before
  // disabling cannot strand a catalog or settings projection.
  disableCodexProjection(locations, state);
  disableClaudeProjection(locations, state);
  state.managed = {};
  state.lastError = "";
  saveState(state, locations.state);
}

function status(config, locations, state) {
  return {
    version: VERSION,
    enabled: config.enabled,
    targets: config.targets,
    provider: {
      baseUrl: resolveProvider(config).baseUrl,
      metadataUrl: config.provider.metadataUrl || "",
      kind: resolveProvider(config).kind,
      authConfigured: Boolean(resolveProvider(config).token),
    },
    lastSyncAt: state.lastSyncAt || "",
    lastError: state.lastError || "",
    modelCount: Array.isArray(state.modelIds) ? state.modelIds.length : 0,
    modelIds: Array.isArray(state.modelIds) ? state.modelIds : [],
    paths: {
      config: locations.config,
      dataRoot: locations.dataRoot,
      codexCatalog: locations.codexCatalog,
      codexConfig: locations.codexConfig,
      claudeSettings: locations.claudeSettings,
    },
  };
}

function parseBoolean(value) {
  if (value === true || value === false) return value;
  const normalized = String(value || "").toLowerCase();
  if (["1", "true", "yes", "on", "enabled"].includes(normalized)) return true;
  if (["0", "false", "no", "off", "disabled"].includes(normalized)) return false;
  throw new Error(`invalid boolean: ${value}`);
}

function parseArgs(argv) {
  const args = { _: [] };
  for (let index = 0; index < argv.length; index += 1) {
    const value = argv[index];
    if (!value.startsWith("--")) { args._.push(value); continue; }
    const [key, inline] = value.slice(2).split("=", 2);
    if (inline !== undefined) args[key] = inline;
    else if (argv[index + 1] && !argv[index + 1].startsWith("--")) args[key] = argv[++index];
    else args[key] = true;
  }
  return args;
}

function usage() {
  return `model-list-plugin ${VERSION}\n\n` +
    "Usage: node scripts/model-list-plugin.mjs <command> [options]\n\n" +
    "Commands: install, configure, enable, disable, sync, status, update, uninstall, hook\n" +
    "Options: --base-url URL --kind auto|openai|anthropic --auth-env NAME --metadata-url URL\n" +
    "         --targets codex,claude --interval SECONDS --claude-alias-mode auto|raw|derived\n" +
    "         --auto-update true|false --json\n";
}

function print(value, asJson = false) {
  if (asJson || typeof value === "object") process.stdout.write(`${JSON.stringify(value, null, 2)}\n`);
  else process.stdout.write(`${value}\n`);
}

function updateCommands(host) {
  if (host === "codex") {
    return [
      ["codex", ["plugin", "marketplace", "upgrade", PLUGIN_NAME]],
      ["codex", ["plugin", "add", `${PLUGIN_NAME}@${PLUGIN_NAME}`]],
    ];
  }
  return [
    ["claude", ["plugin", "marketplace", "update", PLUGIN_NAME]],
    ["claude", ["plugin", "update", PLUGIN_NAME]],
  ];
}

function runUpdate(host) {
  const results = [];
  for (const [command, commandArgs] of updateCommands(host)) {
    const result = spawnSync(command, commandArgs, { stdio: "ignore", timeout: 60_000, windowsHide: true });
    results.push({ command: `${command} ${commandArgs.join(" ")}`, status: result.status, error: result.error?.code || "" });
    if (result.status !== 0) break;
  }
  return results;
}

function configureFromArgs(config, args) {
  const next = clone(config);
  if (args["base-url"] !== undefined) next.provider.baseUrl = normalizeBaseUrl(args["base-url"]);
  if (args.kind !== undefined) {
    const kind = String(args.kind).toLowerCase();
    if (!["auto", "openai", "anthropic"].includes(kind)) throw new Error("kind must be auto, openai, or anthropic");
    next.provider.kind = kind;
  }
  if (args["auth-env"] !== undefined) next.provider.authEnv = String(args["auth-env"]);
  if (args["metadata-url"] !== undefined) next.provider.metadataUrl = String(args["metadata-url"]);
  if (args.interval !== undefined) next.syncIntervalSeconds = Math.max(0, positiveNumber(args.interval));
  if (args["auto-update"] !== undefined) next.autoUpdate = parseBoolean(args["auto-update"]);
  if (args["update-hours"] !== undefined) next.updateCheckHours = Math.max(1, positiveNumber(args["update-hours"]));
  if (args["claude-alias-mode"] !== undefined) {
    const mode = String(args["claude-alias-mode"]).toLowerCase();
    if (!["auto", "raw", "derived"].includes(mode)) {
      throw new Error("claude-alias-mode must be auto, raw, or derived");
    }
    next.claudeAliasMode = mode;
  }
  if (args.targets !== undefined) {
    const targets = String(args.targets).split(",").map((item) => item.trim().toLowerCase()).filter(Boolean);
    next.targets = { codex: targets.includes("codex"), claude: targets.includes("claude") };
    if (!targets.length) throw new Error("targets must contain codex or claude");
  }
  return next;
}

async function main(argv = process.argv.slice(2)) {
  const args = parseArgs(argv);
  const command = args._[0] || "status";
  if (["help", "--help", "-h"].includes(command)) { process.stdout.write(usage()); return 0; }
  const locations = pathsFor();
  const config = loadConfig(locations.config);
  const state = loadState(locations.state);
  const asJson = Boolean(args.json);

  if (command === "install") {
    ensureDirectory(locations.dataRoot);
    if (readText(locations.config) === null) saveConfig(config, locations.config);
    print({ installed: true, enabled: config.enabled, config: locations.config }, asJson || true);
    return 0;
  }
  if (command === "configure") {
    const next = configureFromArgs(config, args);
    saveConfig(next, locations.config);
    print(status(next, locations, state), asJson || true);
    return 0;
  }
  if (command === "status") { print(status(config, locations, state), asJson || true); return 0; }
  if (command === "enable") {
    const next = { ...config, enabled: true };
    saveConfig(next, locations.config);
    try {
      const result = await syncOnce(next, locations, state);
      print({ enabled: true, modelCount: result.models?.length || 0, status: status(next, locations, result.state) }, asJson || true);
      return 0;
    } catch (error) {
      print({ enabled: true, error: error.message, status: status(next, locations, state) }, true);
      return 1;
    }
  }
  if (command === "disable") {
    const next = { ...config, enabled: false };
    disable(next, locations, state);
    saveConfig(next, locations.config);
    print({ enabled: false, status: status(next, locations, state) }, asJson || true);
    return 0;
  }
  if (command === "sync") {
    if (!config.enabled && !args.force) throw new Error("plugin is disabled, use enable or --force");
    const result = await syncOnce(config, locations, state);
    print({ synced: !result.skipped, reason: result.reason || "", modelCount: result.models?.length || 0, status: status(config, locations, result.state) }, asJson || true);
    return 0;
  }
  if (command === "update") {
    const host = String(args.host || process.env.MODEL_LIST_PLUGIN_HOST || "claude").toLowerCase();
    const results = runUpdate(host);
    print({ host, results }, asJson || true);
    return results.every((result) => result.status === 0) ? 0 : 1;
  }
  if (command === "uninstall") {
    const next = { ...config, enabled: false };
    disable(next, locations, state);
    removeFile(locations.config);
    removeFile(locations.state);
    removeFile(locations.lock);
    print({ uninstalled: true, message: "Local projections and plugin data removed. Remove the host plugin with its plugin manager command." }, asJson || true);
    return 0;
  }
  if (command === "hook") {
    if (!config.enabled) return 0;
    const hookHost = String(args.host || "").toLowerCase();
    const hookConfig = hostConfig(config, hookHost);
    const last = Date.parse(
      state.lastSyncAtByTarget?.[hookHost] || state.lastSyncAt || "",
    );
    const elapsed = Number.isFinite(last) ? (Date.now() - last) / 1000 : Infinity;
    if (!args.force && elapsed < config.syncIntervalSeconds) return 0;
    try {
      await syncOnce(hookConfig, locations, state);
    } catch {
      // Hooks must never block the host agent.  The error is retained in state
      // and is visible through `status`.
    }
    if (config.autoUpdate) {
      const checked = Date.parse(state.lastUpdateCheckAt || "");
      const hours = Number.isFinite(checked) ? (Date.now() - checked) / 3_600_000 : Infinity;
      if (hours >= config.updateCheckHours) {
        state.lastUpdateCheckAt = new Date().toISOString();
        saveState(state, locations.state);
        try { runUpdate(String(args.host || process.env.MODEL_LIST_PLUGIN_HOST || "claude")); } catch {}
      }
    }
    return 0;
  }
  throw new Error(`unknown command: ${command}`);
}

function isMainModule() {
  if (!process.argv[1]) return false;
  const entrypoint = path.resolve(process.argv[1]);
  const modulePath = path.resolve(fileURLToPath(import.meta.url));
  if (entrypoint === modulePath) return true;
  try {
    return fs.realpathSync(entrypoint) === fs.realpathSync(modulePath);
  } catch {
    return false;
  }
}

if (isMainModule()) {
  main().catch((error) => {
    process.stderr.write(`model-list-plugin: ${error instanceof Error ? error.message : String(error)}\n`);
    process.exitCode = 1;
  });
}
