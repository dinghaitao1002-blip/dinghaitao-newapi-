// chatgpt2api background image generation over the OpenAI Responses protocol.
//
// Upstream endpoints (chatgpt2api, Background Responses):
//   POST {baseUrl}/v1/responses        body {..., background: true}
//                                      -> 200 queued Response {id: "resp_..."}
//   POST {baseUrl}/v1/responses/batch  body {ids: [...]} (at most 1000)
//                                      -> {data: [Response, ...], missing_ids: [...]}
//
// The plugin claims only the `<model>-background` aliases, so the plain model
// names keep their synchronous and streaming relay path on the gateway.
// The delivery form is decided at submit time from the image_generation tool's
// `response_format` (url by default, b64_json on request) and travels with the
// task; chatgpt2api echoes it in the terminal Response's
// `metadata.response_format`. renderFinal hands it to the host as
// `image_response_format`, and the host downloads and inlines base64 only when
// asked. Images stay on chatgpt2api's public image URLs; nothing is proxied.

const IMAGE_MODELS = [
  "gpt-image-2",
  "gpt-image-auto",
  "codex-gpt-image-2",
  "plus-codex-gpt-image-2",
  "team-codex-gpt-image-2",
  "pro-codex-gpt-image-2",
  "gpt-image-2.5",
  "gpt-image-2.5-flare",
  "gpt-image-2.5-sunburst",
];

export const meta = {
  apiVersion: 1,
  key: "chatgpt2api",
  name: "ChatGPT2API",
  icon: "text:C2",
  version: "1.0.0",
  author: { name: "keyirobot" },
  description: {
    en: "Background image generation through a chatgpt2api gateway",
    zh: "通过 chatgpt2api 后台生图",
  },
  models: IMAGE_MODELS.map(function (model) {
    return model + "-background";
  }),
  fetchMode: "batch",
  upstreams: ["vendor"],
  usageSchema: {
    // One Response produces one image; settlement counts completed
    // image_generation_call items in the terminal Response.
    image_count: {
      type: "number",
      unit: "count",
      unitLabel: { en: "image", zh: "张" },
      description: { en: "Image generation unit price", zh: "图片生成单价" },
    },
  },
  usageExamples: [{ label: "1 image", facts: { image_count: 1 } }],
  // Only the background form: a request without `background: true` on an
  // alias model is rejected by the host before any hook runs.
  protocols: [{ name: "openai_responses", supports: ["background"] }],
};

const STATUS_MAP = { queued: "QUEUED", in_progress: "IN_PROGRESS", completed: "SUCCESS", failed: "FAILURE" };
const MISSING_REASON = "response not found or expired";

function trimmed(value) {
  return String(value || "").trim();
}

function isObject(value) {
  return !!value && typeof value === "object" && !Array.isArray(value);
}

function imageTool(tools) {
  if (!Array.isArray(tools)) return null;
  for (const tool of tools) {
    if (isObject(tool) && tool.type === "image_generation") return tool;
  }
  return null;
}

function authHeaders(ctx) {
  return { "Content-Type": "application/json", Authorization: "Bearer " + ctx.apiKey };
}

function completedImageCount(data) {
  const output = isObject(data) && Array.isArray(data.output) ? data.output : [];
  let count = 0;
  for (const item of output) {
    if (isObject(item) && item.type === "image_generation_call" && item.status === "completed") count += 1;
  }
  return count;
}

// chatgpt2api answers errors with {error: {code, message}} and a non-2xx
// status; both hooks surface that public text instead of a shape error so a
// queue_full, invalid_response_format or auth failure is visible to the
// caller (submit) or counted by the poller (batch).
function upstreamError(status, body) {
  const error = isObject(body) && isObject(body.error) ? body.error : null;
  const ok = Number(status) >= 200 && Number(status) < 300;
  if (ok && !error) return null;
  if (error) {
    const code = trimmed(error.code);
    const message = trimmed(error.message) || "upstream error";
    return new Error(code ? code + ": " + message : message);
  }
  return new Error("upstream HTTP " + String(status));
}

function responseResult(item) {
  const status = STATUS_MAP[item.status];
  if (!status) return { taskId: item.id, status: "UNKNOWN", reason: "unrecognized status " + String(item.status), data: item };
  const result = { taskId: item.id, status: status, data: item };
  if (status === "FAILURE") result.reason = (isObject(item.error) && trimmed(item.error.message)) || "image generation failed";
  return result;
}

export function buildSubmitRequest(ctx) {
  const req = ctx.requestBody || {};
  const body = { model: ctx.upstreamModel || ctx.model, input: req.input, tools: req.tools, background: true };
  if (req.instructions !== undefined) body.instructions = req.instructions;
  const headers = authHeaders(ctx);
  const sizeFit = trimmed(ctx.requestHeaders && ctx.requestHeaders["X-Size-Fit"]);
  if (sizeFit) headers["X-Size-Fit"] = sizeFit;
  return { url: ctx.baseUrl + "/v1/responses", method: "POST", headers: headers, body: body };
}

export function parseSubmitResponse(ctx, resp) {
  const failure = upstreamError(resp.statusCode, resp.body);
  if (failure) throw failure;
  const body = isObject(resp.body) ? resp.body : {};
  const taskId = trimmed(body.id);
  if (taskId.indexOf("resp_") !== 0) throw new Error("upstream did not return a resp_ id");
  const format = (ctx.requestBody && ctx.requestBody.response_format) || "url";
  return { taskId: taskId, taskData: body, state: { response_format: format } };
}

export function extractUsage() {
  return { image_count: 1 };
}

export function extractUsageOnComplete(_ctx, _result, data) {
  return { image_count: completedImageCount(data) };
}

export function buildBatchQueryRequest(ctx, tasks) {
  const ids = (tasks || []).map(function (task) {
    return task.taskId;
  });
  return { url: ctx.baseUrl + "/v1/responses/batch", method: "POST", headers: authHeaders(ctx), body: { ids: ids } };
}

export function buildQueryRequest(ctx) {
  return buildBatchQueryRequest(ctx, [ctx]);
}

export function parseBatchResult(_ctx, body, response) {
  const failure = upstreamError(response && response.status !== undefined ? response.status : 200, body);
  if (failure) throw failure;
  if (!isObject(body) || !Array.isArray(body.data)) throw new Error("batch reply has no data array");
  const results = [];
  const data = body.data;
  for (const item of data) {
    if (isObject(item) && trimmed(item.id)) results.push(responseResult(item));
  }
  // The host leaves tasks absent from a batch reply untouched, so a purged
  // Response must be reported explicitly or it would poll until the sweep.
  const missing = isObject(body) && Array.isArray(body.missing_ids) ? body.missing_ids : [];
  for (const id of missing) {
    if (trimmed(id)) results.push({ taskId: trimmed(id), status: "FAILURE", reason: MISSING_REASON });
  }
  return results;
}

export function parseTaskResult(ctx, body, response) {
  const failure = upstreamError(response && response.status !== undefined ? response.status : 200, body);
  if (failure) throw failure;
  const results = parseBatchResult(ctx, { data: [body], missing_ids: [] }, { status: 200 });
  if (results.length) return results[0];
  return { status: "UNKNOWN", reason: "response body is not a Response object" };
}

export const protocols = {
  openai_responses: {
    decodeRequest: function (ctx) {
      if (!ctx.body || ctx.body.kind !== "json" || !isObject(ctx.body.value)) throw new Error("JSON object required");
      const req = ctx.body.value;
      const tool = imageTool(req.tools);
      if (!tool) throw new Error("background image generation needs an image_generation tool");
      if (typeof req.input !== "string" && !Array.isArray(req.input)) throw new Error("input must be a string or array");
      const format = tool.response_format === undefined ? "url" : tool.response_format;
      if (format !== "url" && format !== "b64_json") throw new Error("response_format must be url or b64_json");
      const requestBody = { model: ctx.model, input: req.input, tools: req.tools, response_format: format };
      if (req.instructions !== undefined) requestBody.instructions = req.instructions;
      return { kind: "submit", model: ctx.model, action: "image", requestBody: requestBody };
    },
    renderFinal: function (_ctx, task) {
      const data = isObject(task.data) ? task.data : {};
      const format = isObject(data.metadata) && data.metadata.response_format === "b64_json" ? "b64_json" : "url";
      return { output: Array.isArray(data.output) ? data.output : [], image_response_format: format };
    },
  },
};
