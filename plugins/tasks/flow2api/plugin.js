// flow2api video generation over the OpenAI video protocol.
//
// flow2api (a Google Flow gateway) serves /v1/videos with the OpenAI task
// shape: POST {baseUrl}/v1/videos -> {id, status: "queued"}, then
// GET {baseUrl}/v1/videos/{id} until status is completed / failed. A completed
// task carries the public CDN `url` of the clip (signed, expires after about
// six hours). That link is the only download path: the plugin projects no
// artifacts and leaves the task result URL unset, so the gateway's id-based
// content routes (/v1/videos/{id}/content, /v1/tasks/{id}/artifacts/...)
// answer "gone" instead of proxying the clip. Clients read `url` from
// GET /v1/videos/{id}, and the Responses renderers embed the same link.
//
// The request body is forwarded verbatim: flow2api reads `duration` / `seconds`,
// `aspect_ratio`, `resolution` (720p / 1080p) or a Sora-style `size`, and
// `reference_images` / `reference_videos`. The generic task path would drop
// the flow-specific fields, which is why these models need their own plugin.
//
// Billing: flow2api charges per second of requested video (720p and 1080p
// cost the same), so `seconds` is the usage quantity; `resolution` is kept
// as an enum condition for operators who want to price 1080p separately.

const VIDEO_MODELS = ["veo-3.1-lite", "veo-3.1-fast", "veo-3.1-quality", "omni-1.1-flash"];
const DEFAULT_SECONDS = 8;
const MAX_SECONDS = 3600;

export const meta = {
  apiVersion: 1,
  key: "flow2api",
  name: "Flow2API",
  icon: "text:F2",
  version: "1.1.0",
  author: { name: "keyirobot" },
  description: {
    en: "Google Flow video generation through a flow2api gateway (Veo 3.1, Omni 1.1 Flash)",
    zh: "通过 flow2api 网关生成 Google Flow 视频（Veo 3.1、Omni 1.1 Flash）",
  },
  models: VIDEO_MODELS,
  fetchMode: "per_task",
  upstreams: ["vendor"],
  usageSchema: {
    // Requested clip length; flow2api defaults to 8 seconds.
    seconds: {
      type: "number",
      unit: "second",
      description: { en: "Video generation unit price", zh: "视频生成单价" },
    },
    // Output resolution requested from flow2api (720p native, 1080p upscaled).
    resolution: {
      enum: ["720p", "1080p"],
      enumLabels: {
        "720p": { en: "720p", zh: "720p" },
        "1080p": { en: "1080p", zh: "1080p" },
      },
      description: { en: "Output video resolution", zh: "输出视频分辨率" },
    },
  },
  protocols: [{ name: "openai_responses", supports: ["stream", "sync", "background"] }, "openai_video"],
};

function trimmed(value) {
  return String(value || "").trim();
}

function isObject(value) {
  return !!value && typeof value === "object" && !Array.isArray(value);
}

function authHeaders(ctx) {
  return { Authorization: "Bearer " + ctx.apiKey };
}

// Seconds come from `seconds` or `duration` ("8", "8s", 8); absent means flow2api's default.
function requestedSeconds(req) {
  const raw = req.seconds !== undefined && req.seconds !== null && req.seconds !== "" ? req.seconds : req.duration;
  if (raw === undefined || raw === null || raw === "") return DEFAULT_SECONDS;
  const seconds = Number(String(raw).replace(/s$/i, ""));
  if (!Number.isFinite(seconds) || seconds <= 0) return DEFAULT_SECONDS;
  return Math.min(seconds, MAX_SECONDS);
}

// Resolution from `resolution` (720p / 1080p and aliases) or a Sora-style `size`; anything else bills as 720p.
function requestedResolution(req) {
  const token = trimmed(req.resolution).toLowerCase();
  if (["1080p", "1080", "fhd"].includes(token)) return "1080p";
  if (token) return "720p";
  const size = trimmed(req.size).toLowerCase().match(/^(\d{2,5})\s*[x×]\s*(\d{2,5})$/);
  if (size && Math.min(Number(size[1]), Number(size[2])) >= 1080) return "1080p";
  return "720p";
}

function validateSeconds(req) {
  const raw = req.seconds === undefined ? req.duration : req.seconds;
  if (raw === undefined || raw === null || raw === "") return;
  const seconds = Number(String(raw).replace(/s$/i, ""));
  if (!Number.isFinite(seconds) || seconds <= 0 || seconds > MAX_SECONDS) throw new Error("seconds must be between 1 and " + MAX_SECONDS);
}

function hasReferenceMedia(req) {
  return ["reference_images", "input_reference", "images", "image", "reference_videos"].some(function (key) {
    const value = req[key];
    return Array.isArray(value) ? value.length > 0 : !!trimmed(value) || isObject(value);
  });
}

function responsesInput(req) {
  const texts = [],
    images = [];
  const input = req.input;
  if (typeof input === "string") texts.push(input);
  else if (Array.isArray(input)) {
    for (const item of input) {
      if (typeof item === "string") {
        texts.push(item);
        continue;
      }
      if (!isObject(item)) continue;
      const content = item.content === undefined ? [item] : Array.isArray(item.content) ? item.content : [item.content];
      for (const part of content) {
        if (typeof part === "string") {
          texts.push(part);
          continue;
        }
        if (!isObject(part)) continue;
        if (["input_text", "text"].includes(part.type) && typeof part.text === "string") texts.push(part.text);
        if (["input_image", "image_url"].includes(part.type)) {
          let image = part.image_url;
          if (image && typeof image === "object") image = image.url;
          if (trimmed(image)) images.push(trimmed(image));
        }
      }
    }
  }
  return {
    prompt: texts
      .filter(function (text) {
        return trimmed(text);
      })
      .join("\n"),
    images: images,
  };
}

function responsesVideoText(task) {
  const url = trimmed(task && isObject(task.data) && task.data.url);
  if (!url) throw new Error("video url is unavailable");
  const escaped = url.replace(/&/g, "&amp;").replace(/"/g, "&quot;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
  return '<video controls src="' + escaped + '"></video>';
}

export function buildSubmitRequest(ctx) {
  const req = isObject(ctx.requestBody) ? ctx.requestBody : {};
  if (!trimmed(req.prompt)) throw new Error("field prompt is required");
  const body = Object.assign({}, req, { model: ctx.upstreamModel || ctx.model });
  const headers = authHeaders(ctx);
  if ((ctx.files || []).length) {
    const parts = [];
    for (const key of Object.keys(body)) {
      if (body[key] !== undefined && body[key] !== null && typeof body[key] !== "object") parts.push({ name: key, value: body[key] });
    }
    for (const file of ctx.files) parts.push({ name: file.field, fileRef: file.ref, filename: file.filename });
    return { url: ctx.baseUrl + "/v1/videos", method: "POST", headers: headers, bodyType: "multipart", parts: parts };
  }
  headers["Content-Type"] = "application/json";
  return { url: ctx.baseUrl + "/v1/videos", method: "POST", headers: headers, body: body };
}

export function parseSubmitResponse(ctx, resp) {
  const body = isObject(resp.body) ? resp.body : {};
  const taskId = trimmed(body.id);
  if (!taskId) throw new Error("flow2api did not return a task id");
  return { taskId: taskId, taskData: body };
}

export function extractUsage(ctx) {
  const req = isObject(ctx.requestBody) ? ctx.requestBody : {};
  return { seconds: requestedSeconds(req), resolution: requestedResolution(req) };
}

// flow2api's status body does not echo the request parameters; the submission facts stand.
export function extractUsageOnComplete() {
  return {};
}

export function buildQueryRequest(ctx) {
  return { url: ctx.baseUrl + "/v1/videos/" + encodeURIComponent(ctx.taskId), method: "GET", headers: authHeaders(ctx) };
}

export function parseTaskResult(ctx, body) {
  const statuses = { queued: "QUEUED", in_progress: "IN_PROGRESS", completed: "SUCCESS", failed: "FAILURE" };
  const data = isObject(body) ? body : {};
  const mapped = statuses[data.status];
  const result = { status: mapped || "UNKNOWN" };
  if (!mapped) {
    result.reason = "unrecognized status: " + String(data.status || "");
    return result;
  }
  const progress = Number(data.progress);
  if (Number.isFinite(progress) && progress > 0 && progress < 100) result.progress = progress + "%";
  // The clip URL stays only in task.data; setting result.url would let the
  // gateway proxy it through the id-based content route.
  if (mapped === "FAILURE") result.reason = (isObject(data.error) && trimmed(data.error.message)) || "video generation failed";
  return result;
}

// openai_video requires both hooks. No artifacts are projected, so the host
// never asks for content; buildContentRequest refuses if it ever does.
export function listArtifacts() {
  return [];
}

export function buildContentRequest() {
  throw new Error("id-based download is disabled; download the video from the url returned by GET /v1/videos/{id}");
}

export const protocols = {
  openai_responses: {
    decodeRequest: function (ctx) {
      if (!ctx.body || ctx.body.kind !== "json") throw new Error("JSON body required");
      const req = ctx.body.value;
      if (!isObject(req)) throw new Error("request body must be an object");
      const model = trimmed(req.model);
      if (!model) throw new Error("model is required");
      if (req.input !== undefined && typeof req.input !== "string" && !Array.isArray(req.input)) throw new Error("input must be a string or array");
      if (req.images !== undefined && !Array.isArray(req.images)) throw new Error("images must be an array");
      const input = responsesInput(req);
      const prompt = input.prompt || trimmed(req.prompt);
      if (!prompt) throw new Error("input is required");
      const images = [];
      for (const image of [req.image, req.input_reference].concat(req.images || [], input.images)) {
        if (trimmed(image) && !images.includes(trimmed(image))) images.push(trimmed(image));
      }
      validateSeconds(req);
      const requestBody = { model: model, prompt: prompt };
      if (images.length) requestBody.reference_images = images;
      for (const key of ["seconds", "duration", "aspect_ratio", "resolution", "size", "reference_videos"]) {
        if (Object.prototype.hasOwnProperty.call(req, key)) requestBody[key] = req[key];
      }
      return { kind: "submit", model: model, action: images.length ? "image_to_video" : "text_to_video", requestBody: requestBody };
    },
    renderEvents: function (ctx, task, previousState) {
      const status = String(task.status || "UNKNOWN").toUpperCase();
      const value = Number(String(task.progress || "").replace("%", ""));
      const progress = Number.isFinite(value) && value >= 0 && value <= 100 ? value : null;
      const state = { status: status, progress: progress };
      if (status === "SUCCESS") {
        const text = responsesVideoText(task);
        const events = previousState && previousState.status === status ? [] : [{ type: "output", data: text }];
        return { events: events, state: state, done: true };
      }
      if (status === "FAILURE")
        return { events: [{ type: "error", code: "task_failed", message: task.fail_reason || "task failed" }], state: state, done: true };
      if (previousState && previousState.status === status && previousState.progress === progress) return { events: [], state: state, done: false };
      const event = { type: "progress", message: status.toLowerCase() };
      if (progress !== null) event.progress = progress;
      return { events: [event], state: state, done: false };
    },
    renderFinal: function (_ctx, task) {
      return {
        output: [
          {
            type: "message",
            status: "completed",
            role: "assistant",
            content: [{ type: "output_text", text: responsesVideoText(task), annotations: [], logprobs: [] }],
          },
        ],
        metadata: { vendor: "flow2api" },
      };
    },
  },
  openai_video: {
    decodeRequest: function (ctx) {
      if (!ctx.body || (ctx.body.kind !== "json" && ctx.body.kind !== "multipart")) throw new Error("JSON or multipart body required");
      let req;
      if (ctx.body.kind === "json") {
        if (!isObject(ctx.body.value)) throw new Error("JSON object required");
        req = Object.assign({}, ctx.body.value);
      } else {
        req = {};
        const fields = ctx.body.fields || {};
        for (const name of Object.keys(fields)) {
          const values = fields[name] || [];
          if (values.length > 1) throw new Error(name + " must be provided once");
          req[name] = values[0];
        }
        for (const file of ctx.body.files || []) {
          if (file.field !== "input_reference") throw new Error("unexpected file field: " + file.field);
        }
      }
      validateSeconds(req);
      const hasFile = ctx.body.kind === "multipart" && (ctx.body.files || []).length > 0;
      return {
        kind: "submit",
        model: ctx.model,
        action: hasFile || hasReferenceMedia(req) ? "image_to_video" : "text_to_video",
        requestBody: Object.assign(req, { model: ctx.model }),
      };
    },
    render: function (ctx, task) {
      if (isObject(task.data)) return task.data;
      const statuses = { NOT_START: "queued", SUBMITTED: "queued", QUEUED: "queued", IN_PROGRESS: "in_progress", SUCCESS: "completed", FAILURE: "failed" };
      const output = {
        id: task.task_id,
        object: "video",
        status: statuses[task.status] || "unknown",
        progress: Number(String(task.progress || "0").replace("%", "")),
      };
      if (task.status === "FAILURE") output.error = { code: "video_generation_failed", message: task.fail_reason || "The video generation task failed." };
      return output;
    },
  },
};
