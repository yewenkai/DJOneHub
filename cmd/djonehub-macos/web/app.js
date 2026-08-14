const $ = (selector) => document.querySelector(selector);
let lastSMSCount = null;
let networkTrafficTimer = null;
let networkTrafficPrevious = null;
let networkTrafficInFlight = false;
let trafficQuotaInFlight = false;
let cellularLabTimer = null;
let cellularLabSamples = [];
let cellularLabInFlight = false;
let bandPreference = null;
let bandPreferenceInFlight = false;
let bandPreferenceTimer = null;
let currentOperator = "";
let voiceOverview = null;
let voiceStatusInFlight = false;
let voiceCallsInFlight = false;
let actionToken = "";
let actionTokenPromise = null;
let lastIncomingCallKey = "";
let egressPolicyDraft = null;
let egressInstalledApps = [];
let egressLoading = false;

function setThemePreference(theme) {
  if (theme === "light" || theme === "dark") {
    document.documentElement.dataset.theme = theme;
    localStorage.setItem("djonehub-theme", theme);
    localStorage.removeItem("vohive-theme");
  } else {
    delete document.documentElement.dataset.theme;
    localStorage.removeItem("djonehub-theme");
    localStorage.removeItem("vohive-theme");
  }
  document.querySelectorAll("[data-theme-option]").forEach((button) => {
    button.setAttribute("aria-pressed", String(button.dataset.themeOption === theme));
  });
}

const savedTheme = localStorage.getItem("djonehub-theme") || localStorage.getItem("vohive-theme");
setThemePreference(savedTheme === "light" || savedTheme === "dark" ? savedTheme : "auto");
document.querySelectorAll("[data-theme-option]").forEach((button) => {
  button.addEventListener("click", () => setThemePreference(button.dataset.themeOption));
});

const operatorNames = new Map([
  ["CHN-UNICOM", "中国联通"],
  ["CHINA UNICOM", "中国联通"],
  ["UNICOM", "中国联通"],
  ["46001", "中国联通"],
  ["46006", "中国联通"],
  ["46009", "中国联通"],
  ["CHINA MOBILE", "中国移动"],
  ["CMCC", "中国移动"],
  ["CHN-CMCC", "中国移动"],
  ["46000", "中国移动"],
  ["46002", "中国移动"],
  ["46004", "中国移动"],
  ["46007", "中国移动"],
  ["46008", "中国移动"],
  ["CHINA TELECOM", "中国电信"],
  ["CHN-CT", "中国电信"],
  ["CTCC", "中国电信"],
  ["46003", "中国电信"],
  ["46005", "中国电信"],
  ["46011", "中国电信"],
  ["CBN", "中国广电"],
  ["CHN-CBN", "中国广电"],
  ["CHINA BROADNET", "中国广电"],
  ["46015", "中国广电"],
]);

const mainlandLTEBandInfo = new Map([
  [1, { frequency: "2100 MHz", duplex: "FDD", carriers: ["telecom", "unicom"] }],
  [3, { frequency: "1800 MHz", duplex: "FDD", carriers: ["mobile", "unicom", "telecom"] }],
  [5, { frequency: "850 MHz", duplex: "FDD", carriers: ["telecom"] }],
  [8, { frequency: "900 MHz", duplex: "FDD", carriers: ["mobile", "unicom"] }],
  [34, { frequency: "2000 MHz", duplex: "TDD", carriers: ["mobile"] }],
  [38, { frequency: "2600 MHz", duplex: "TDD", carriers: ["mobile"] }],
  [39, { frequency: "1900 MHz", duplex: "TDD", carriers: ["mobile"] }],
  [40, { frequency: "2300 MHz", duplex: "TDD", carriers: ["mobile", "unicom"] }],
  [41, { frequency: "2600 MHz", duplex: "TDD", carriers: ["mobile"] }],
]);

const otherLTEBandInfo = new Map([
  [2, { frequency: "1900 MHz", duplex: "FDD" }],
  [4, { frequency: "AWS", duplex: "FDD" }],
  [7, { frequency: "2600 MHz", duplex: "FDD" }],
  [12, { frequency: "700 MHz", duplex: "FDD" }],
  [13, { frequency: "700 MHz", duplex: "FDD" }],
  [18, { frequency: "850 MHz", duplex: "FDD" }],
  [19, { frequency: "850 MHz", duplex: "FDD" }],
  [20, { frequency: "800 MHz", duplex: "FDD" }],
  [25, { frequency: "1900 MHz", duplex: "FDD" }],
  [26, { frequency: "850 MHz", duplex: "FDD", note: "与B5频率有重叠，国内电信LTE仍按B5使用" }],
  [28, { frequency: "700 MHz", duplex: "FDD", note: "广电国内700MHz主要用于5G" }],
  [66, { frequency: "AWS", duplex: "FDD" }],
  [71, { frequency: "600 MHz", duplex: "FDD" }],
]);

const carrierLabels = {
  mobile: "中国移动",
  unicom: "中国联通",
  telecom: "中国电信",
};

async function loadActionToken(force = false) {
  if (force) {
    actionToken = "";
    actionTokenPromise = null;
  }
  if (actionToken) return actionToken;
  if (!actionTokenPromise) {
    actionTokenPromise = fetch("/api/session", {
      cache: "no-store",
      headers: { Accept: "application/json" },
    })
      .then(async (response) => {
        const data = await response.json().catch(() => ({}));
        if (!response.ok || !data.action_token) {
          throw new Error(data.error || ("HTTP " + response.status));
        }
        actionToken = data.action_token;
        return actionToken;
      })
      .finally(() => {
        actionTokenPromise = null;
      });
  }
  return actionTokenPromise;
}

async function api(path, options = {}, allowTokenRefresh = true) {
  const method = String(options.method || "GET").toUpperCase();
  const stateChanging = ["POST", "PUT", "PATCH", "DELETE"].includes(method);
  const headers = { Accept: "application/json", ...(options.headers || {}) };
  if (stateChanging) {
    headers["Content-Type"] = "application/json";
    headers["X-DJOneHub-Action-Token"] = await loadActionToken();
  }
  const response = await fetch(path, {
    ...options,
    headers,
  });
  const data = await response.json().catch(() => ({}));
  if (
    response.status === 403 &&
    data.code === "invalid_action_token" &&
    stateChanging &&
    allowTokenRefresh
  ) {
    await loadActionToken(true);
    return api(path, options, false);
  }
  if (!response.ok) throw new Error(data.error || `HTTP ${response.status}`);
  return data;
}

function notice(message) {
  const el = $("#notice");
  el.textContent = message;
  el.classList.add("show");
  clearTimeout(notice.timer);
  notice.timer = setTimeout(() => el.classList.remove("show"), 2600);
}

let modalResolve = null;

function closeModal(result = null) {
  const modal = $("#app-modal");
  modal.hidden = true;
  document.body.classList.remove("modal-open");
  if (modalResolve) {
    const resolve = modalResolve;
    modalResolve = null;
    resolve(result);
  }
}

function showModal({ title, message = "", fields = [], confirmLabel = "确定", danger = false }) {
  if (modalResolve) closeModal(null);
  const modal = $("#app-modal");
  const messageElement = $("#modal-message");
  const fieldsElement = $("#modal-fields");
  const confirmButton = $("#modal-confirm");
  $("#modal-title").textContent = title;
  messageElement.textContent = message;
  messageElement.hidden = !message;
  fieldsElement.replaceChildren(...fields.map((field) => {
    const label = document.createElement("label");
    label.className = "modal-field";
    const caption = document.createElement("span");
    caption.textContent = field.label;
    const input = document.createElement("input");
    input.name = field.name;
    input.value = field.value || "";
    input.placeholder = field.placeholder || "";
    input.autocomplete = "off";
    if (field.required) input.required = true;
    label.append(caption, input);
    return label;
  }));
  confirmButton.textContent = confirmLabel;
  confirmButton.className = danger ? "danger modal-danger" : "";
  modal.hidden = false;
  document.body.classList.add("modal-open");
  const firstInput = fieldsElement.querySelector("input");
  setTimeout(() => (firstInput || confirmButton).focus(), 0);
  return new Promise((resolve) => { modalResolve = resolve; });
}

$("#modal-form").addEventListener("submit", (event) => {
  event.preventDefault();
  const values = {};
  event.currentTarget.querySelectorAll(".modal-fields input").forEach((input) => {
    values[input.name] = input.value.trim();
  });
  closeModal(values);
});
$("#modal-cancel").addEventListener("click", () => closeModal(null));
$("#modal-close").addEventListener("click", () => closeModal(null));
$("#app-modal").addEventListener("click", (event) => {
  if (event.target === event.currentTarget) closeModal(null);
});
document.addEventListener("keydown", (event) => {
  if (event.key === "Escape" && !$("#app-modal").hidden) closeModal(null);
});

async function copySMSCode(code) {
  try {
    await navigator.clipboard.writeText(code);
    notice(`验证码 ${code} 已复制`);
  } catch (error) {
    notice("复制失败，请手动复制验证码");
  }
}

function renderHardwareDetails(status) {
  const panel = $("#hardware-details");
  const device = status.usb_device;
  if (!device && !status.discovery_error) {
    panel.hidden = true;
    panel.replaceChildren();
    return;
  }

  const title = document.createElement("strong");
  title.textContent = device ? "已检测到大疆 USB 设备" : "未检测到可用硬件";

  const detail = document.createElement("p");
  if (device) {
    const interfaceText = Array.isArray(device.interfaces)
      ? `${device.interfaces.length} 个 USB interface`
      : "interface 未知";
    detail.textContent = [
      `${device.vendor || "DJI"} ${device.product || ""}`.trim(),
      `${device.vendor_id}:${device.product_id}`,
      device.mode,
      interfaceText,
    ].filter(Boolean).join(" · ");
  } else {
    detail.textContent = status.discovery_error || "设备未枚举";
  }

  const hint = document.createElement("small");
  hint.textContent = status.discovery_error
    ? `当前限制：${status.discovery_error}`
    : "AT 通道可用后，短信、网络诊断和通话控制会自动启用。";

  panel.hidden = false;
  panel.replaceChildren(title, detail, hint);
}

function setValue(id, text, tone = "") {
  const el = $(id);
  el.textContent = text || "--";
  el.className = tone;
}

function displayOperatorName(value) {
  const raw = String(value || "").trim();
  if (!raw) return "--";
  return operatorNames.get(raw.toUpperCase()) || raw;
}

function displayWorkMode(value) {
	if (value === null || value === undefined || value === "") {
	  return { label: "待读取", tone: "muted" };
	}
  switch (Number(value)) {
    case 0: return { label: "管理兼容模式", tone: "info" };
    case 1: return { label: "日常模式 · 上网+短信", tone: "good" };
    case 2: return { label: "实验模式 2", tone: "warn" };
    case 3: return { label: "实验模式 3", tone: "warn" };
    default: return { label: "待读取", tone: "muted" };
  }
}

function signalTone(dbm) {
  const value = Number(dbm);
  if (!Number.isFinite(value) || value === 0) return "muted";
  if (value >= -65) return "good";
  if (value >= -75) return "signal-fair";
  if (value >= -85) return "warn";
  if (value >= -95) return "orange";
  return "bad";
}

async function loadStatus() {
  try {
    const status = await api("/api/status");
    currentOperator = status.operator || "";
    setValue("#operator", displayOperatorName(status.operator), status.operator ? "info" : "muted");
    setValue("#signal", status.signal_dbm ? `${status.signal_dbm} dBm` : "--", signalTone(status.signal_dbm));
    setValue("#network-mode", status.network_mode || status.reg_status_text || "--", status.network_mode ? "info" : "muted");
    setValue(
      "#sim",
      status.sim_inserted ? "已插入" : (status.usb_device ? "待读取" : "未检测到"),
      status.sim_inserted ? "good" : (status.usb_device ? "warn" : "bad"),
    );
    const workMode = Object.prototype.hasOwnProperty.call(status, "usbnet_mode")
      ? displayWorkMode(status.usbnet_mode)
      : displayWorkMode(null);
    setValue("#work-mode", workMode.label, workMode.tone);
    const workModeStatus = $("#workmode-status");
    if (Number(status.usbnet_mode) === 1) {
      workModeStatus.hidden = false;
      workModeStatus.textContent = "日常模式已开启：4G USB 网卡与短信后台轮询可同时工作，无需为收短信切换模式。";
    } else if (!workModeStatus.textContent.includes("正在")) {
      workModeStatus.hidden = false;
      workModeStatus.textContent = "当前为管理兼容模式；需要日常上网时可切换到模式 1，短信与 AT 管理仍会保留。";
    }
    $("#device-summary").textContent =
      status.hardware_status || [status.imei, status.firmware].filter(Boolean).join(" · ") || "模块初始化中";
    renderHardwareDetails(status);
  } catch (error) {
    $("#device-summary").textContent = error.message;
  }
}

async function loadSMS() {
  const list = $("#sms-list");
  try {
    const [messages, status] = await Promise.all([
      api("/api/sms"),
      api("/api/sms/status"),
    ]);
    const pollText = status.polling
      ? `自动轮询 ${status.poll_interval_s || 8}s`
      : "自动轮询未启用";
    const cleanupText = status.auto_cleanup_me ? "自动清理 ME 已开启" : "自动清理 ME 未开启";
    const errorText = status.last_poll_error ? ` · 最近错误：${status.last_poll_error}` : "";
    $("#sms-status").textContent = `当前缓存 ${messages.length} 条短信 · ${pollText} · ${cleanupText}${errorText}`;
    if (lastSMSCount !== null && messages.length > lastSMSCount) {
      notice(`收到 ${messages.length - lastSMSCount} 条新短信`);
    }
    lastSMSCount = messages.length;
    if (!messages.length) {
      list.className = "list empty";
      list.textContent = "暂无短信";
      return;
    }
    list.className = "list";
    list.replaceChildren(...messages.map((message) => {
      const row = document.createElement("article");
      row.className = "item";
      const sender = document.createElement("strong");
      sender.textContent = message.sender || "未知号码";
      const content = document.createElement("p");
      content.textContent = message.content;
      const time = document.createElement("time");
      time.textContent = new Date(message.timestamp).toLocaleString();
      if (message.code) {
        const actions = document.createElement("div");
        actions.className = "sms-actions";
        const badge = document.createElement("span");
        badge.className = "code-badge";
        badge.textContent = `验证码 ${message.code}`;
        const copy = document.createElement("button");
        copy.className = "secondary compact";
        copy.type = "button";
        copy.textContent = "复制";
        copy.addEventListener("click", () => copySMSCode(message.code));
        actions.append(badge, copy, time);
        row.append(sender, content, actions);
      } else {
        row.append(sender, content, time);
      }
      return row;
    }));
  } catch (error) {
    $("#sms-status").textContent = `读取列表失败：${error.message}`;
    notice(error.message);
  }
}

function maskPhoneNumber(value) {
  const text = String(value || "").trim();
  const digitCount = [...text].filter((char) => /\d/.test(char)).length;
  if (digitCount <= 8) return text;
  let digitIndex = 0;
  return [...text].map((char) => {
    if (!/\d/.test(char)) return char;
    digitIndex += 1;
    return digitIndex > 4 && digitIndex <= digitCount - 4 ? "*" : char;
  }).join("");
}

function diagnosticCard(label, value, detail = "") {
  const card = document.createElement("div");
  card.className = "diagnostic-card";
  const span = document.createElement("span");
  span.textContent = label;
  const strong = document.createElement("strong");
  strong.textContent = value || "--";
  card.append(span, strong);
  if (detail) {
    const small = document.createElement("small");
    small.textContent = detail;
    card.append(small);
  }
  return card;
}

const voiceCallStateNames = {
  active: "通话中",
  held: "保持",
  dialing: "正在拨号",
  alerting: "等待接听",
  incoming: "来电",
  waiting: "呼叫等待",
  disconnected: "已结束",
  unknown: "未知状态",
};

function renderVoiceCalls(calls, audio, recording) {
  const list = $("#voice-calls");
  const rows = Array.isArray(calls) ? calls : [];
  const incoming = rows.find((call) => call.state === "incoming" || call.state === "waiting");
  $("#call-answer").disabled = !incoming;
  $("#call-hangup").disabled = rows.length === 0;
  $("#voice-audio-start").disabled = Boolean(audio?.running || audio?.stopping);
  $("#voice-audio-stop").disabled = !audio?.running || Boolean(audio?.stopping);
  $("#voice-recording-start").disabled = !audio?.running || Boolean(recording?.running);
  $("#voice-recording-stop").disabled = !recording?.running;

  if (incoming) {
    const key = `${incoming.id}:${incoming.number || "unknown"}`;
    if (lastIncomingCallKey !== key) {
      notice(`收到来电：${maskPhoneNumber(incoming.number || "未知号码")}`);
      lastIncomingCallKey = key;
    }
  } else {
    lastIncomingCallKey = "";
  }

  if (!rows.length) {
    list.className = "list empty";
    list.textContent = "当前没有语音通话";
    return;
  }
  list.className = "list";
  list.replaceChildren(...rows.map((call) => {
    const row = document.createElement("article");
    row.className = "item";
    const title = document.createElement("strong");
    title.textContent = maskPhoneNumber(call.number || "未知号码");
    const detail = document.createElement("p");
    detail.textContent = call.direction === "incoming" ? "呼入" : "呼出";
    const state = document.createElement("small");
    state.textContent = voiceCallStateNames[call.state] || call.state;
    row.append(title, detail, state);
    return row;
  }));
}

function renderVoiceHistory(history) {
  const list = $("#voice-history");
  const rows = Array.isArray(history) ? history : [];
  if (!rows.length) {
    list.className = "list empty";
    list.textContent = "暂无通话记录";
    return;
  }
  list.className = "list";
  list.replaceChildren(...rows.map((call) => {
    const row = document.createElement("article");
    row.className = "item";
    const title = document.createElement("strong");
    title.textContent = maskPhoneNumber(call.number || "未知号码");
    const detail = document.createElement("p");
    if (call.missed) detail.textContent = "未接来电";
    else detail.textContent = call.direction === "incoming" ? "已接来电" : "呼出电话";
    const ended = document.createElement("time");
    ended.dateTime = call.ended_at || call.updated_at || "";
    ended.textContent = new Date(call.ended_at || call.updated_at || call.started_at).toLocaleString("zh-CN");
    row.append(title, detail, ended);
    return row;
  }));
}

function renderVoiceOverview(overview) {
  voiceOverview = overview;
  const inventory = overview.inventory || {};
  const audio = overview.audio || {};
  const recording = overview.recording || {};
  $("#voice-audio-grid").replaceChildren(
    diagnosticCard("USB Audio", overview.uac_enabled ? "已识别" : "未识别", inventory.error || "AC / AS Interface"),
    diagnosticCard("模块下行", inventory.module_capture || audio.module_capture || "--", "模块通话音频 → Mac"),
    diagnosticCard("模块上行", inventory.module_playback || audio.module_playback || "--", "Mac 麦克风 → 模块"),
    diagnosticCard("Mac 麦克风", inventory.mac_capture || audio.mac_capture || "--"),
    diagnosticCard("Mac 播放", inventory.mac_playback || audio.mac_playback || "--"),
    diagnosticCard("模块音频路由", overview.audio_route ? "已接通" : "未接通", overview.audio_route ? "QPCMV 已在通话中启用" : "接通电话后自动启用"),
    diagnosticCard("模块下行电平", `${audio.module_level || 0}%`, "对方声音进入模块 USB Audio"),
    diagnosticCard("Mac 麦克风电平", `${audio.mac_level || 0}%`, "本机声音送往模块 USB Audio"),
    diagnosticCard("音频桥", audio.running ? "运行中" : (audio.stopping ? "正在停止" : "已停止"), audio.running ? `${audio.sample_rate || 8000} Hz · 单声道` : (audio.last_error || "需要时手动或随拨号启动")),
    diagnosticCard(
      "通话录音",
      recording.running ? "录音中" : (recording.file_name ? "已保存" : "未开始"),
      recording.running
        ? `${recording.file_name || "WAV"} · ${Math.floor(Number(recording.duration_ms || 0) / 1000)} 秒`
        : (recording.path || recording.last_error || "左声道对方 · 右声道本机"),
    ),
  );
  const messages = [
    overview.ims ? `IMS ${overview.ims}` : "",
    overview.warning || "",
    overview.last_error ? `最近提示：${overview.last_error}` : "",
  ].filter(Boolean);
  $("#voice-status").textContent = messages.join(" · ") || "通话控制已就绪";
  renderVoiceCalls(overview.calls, audio, recording);
}

async function loadVoiceOverview() {
  if (voiceStatusInFlight) return;
  voiceStatusInFlight = true;
  try {
    renderVoiceOverview(await api("/api/voice"));
  } catch (error) {
    $("#voice-status").textContent = `通话状态读取失败：${error.message}`;
  } finally {
    voiceStatusInFlight = false;
  }
}

async function loadVoiceCalls() {
  if (voiceCallsInFlight) return;
  voiceCallsInFlight = true;
  try {
    const status = await api("/api/voice/calls");
    renderVoiceCalls(status.calls, status.audio, status.recording);
    renderVoiceHistory(status.history);
    if (voiceOverview) {
      voiceOverview.calls = status.calls;
      voiceOverview.audio = status.audio;
      voiceOverview.audio_route = status.audio_route;
      voiceOverview.recording = status.recording;
      renderVoiceOverview(voiceOverview);
    }
  } catch (_) {
    // Other status and SMS polling can temporarily own the single AT channel.
  } finally {
    voiceCallsInFlight = false;
  }
}

async function runVoiceAction(button, path, body, successMessage) {
  button.disabled = true;
  try {
    await api(path, { method: "POST", body: JSON.stringify(body || {}) });
    await Promise.all([loadVoiceCalls(), loadVoiceOverview()]);
    notice(successMessage);
  } catch (error) {
    notice(error.message);
    await loadVoiceOverview();
  } finally {
    button.disabled = false;
  }
}

function renderNetworkCheck(label, result) {
  const list = $("#network-checks");
  list.className = "list";
  const row = document.createElement("article");
  row.className = `item check-item ${result.ok ? "ok" : "bad"}`;
  const name = document.createElement("strong");
  name.textContent = label;
  const detail = document.createElement("p");
  detail.textContent = result.detail || result.summary || "";
  const status = document.createElement("small");
  status.textContent = result.ok ? "通过" : "未通过";
  row.append(name, detail, status);
  const existing = [...list.querySelectorAll(".item")].filter((item) => item.dataset.label !== label);
  row.dataset.label = label;
  list.replaceChildren(row, ...existing);
}

function labMetric(value, suffix, digits = 0) {
  return Number.isFinite(value) ? `${Number(value).toFixed(digits)}${suffix}` : "--";
}

function latestLabMetric(samples, key) {
  for (let index = samples.length - 1; index >= 0; index -= 1) {
    if (Number.isFinite(samples[index]?.[key])) return samples[index][key];
  }
  return null;
}

function latestLabSampleWith(samples, key) {
  for (let index = samples.length - 1; index >= 0; index -= 1) {
    if (samples[index]?.[key] != null) return samples[index];
  }
  return null;
}

function cellularLabDiagnosis(latest, speed, webSample) {
  const rsrp = Number(latest.rsrp);
  const rsrq = Number(latest.rsrq);
  const sinr = Number(latest.sinr);
  let radio = "无线待测";
  if (Number.isFinite(rsrp) && Number.isFinite(rsrq) && Number.isFinite(sinr)) {
    if (rsrp >= -80 && rsrq >= -10 && sinr >= 20) radio = "无线优秀";
    else if (rsrp >= -95 && rsrq >= -15 && sinr >= 10) radio = "无线良好";
    else radio = "无线偏弱";
  }
  const web = webSample?.web_probe;
  const webUsable = web && Number(web.success_percent) >= 67;
  const slowThroughput = Number.isFinite(speed) && speed < 2;
  if (webUsable && slowThroughput) {
    return { value: `${radio} · 网页可用`, detail: "吞吐偏低，符合数据面或核心网拥塞特征" };
  }
  if (webUsable) {
    return { value: `${radio} · 网页可用`, detail: `国内站点成功 ${web.success_count}/${web.target_count}` };
  }
  if (web) {
    return { value: `${radio} · 网页不稳定`, detail: `国内站点成功 ${web.success_count}/${web.target_count}` };
  }
  if (slowThroughput) {
    return { value: `${radio} · 吞吐偏低`, detail: "建议执行国内网页体验测试区分可用性" };
  }
  return { value: radio, detail: "等待网页体验或下载测速结果" };
}

function renderWebProbeResults(sample) {
  const panel = $("#lab-web-results");
  const probe = sample?.web_probe;
  if (!probe || !Array.isArray(probe.results)) {
    panel.hidden = true;
    panel.replaceChildren();
    return;
  }
  panel.hidden = false;
  const heading = document.createElement("div");
  heading.className = "lab-web-heading";
  const title = document.createElement("strong");
  title.textContent = `国内网页体验 · 成功 ${probe.success_count}/${probe.target_count}`;
  const route = document.createElement("small");
  route.textContent = `${probe.interface || "--"} · 源地址 ${probe.source_ip || "--"} · 读取 ${formatTrafficBytes(probe.bytes_read)}`;
  heading.append(title, route);
  const list = document.createElement("div");
  list.className = "lab-web-targets";
  probe.results.forEach((result) => {
    const row = document.createElement("article");
    row.className = `lab-web-target ${result.ok ? "ok" : "bad"}`;
    const name = document.createElement("strong");
    name.textContent = result.name;
    const metrics = document.createElement("p");
    metrics.textContent = result.ok
      ? `DNS ${labMetric(result.dns_ms, " ms", 1)} · TCP ${labMetric(result.connect_ms, " ms", 1)} · TLS ${labMetric(result.tls_ms, " ms", 1)} · TTFB ${labMetric(result.ttfb_ms, " ms", 1)} · 总计 ${labMetric(result.total_ms, " ms", 1)}`
      : result.error || "测试失败";
    const status = document.createElement("small");
    status.textContent = result.ok ? `HTTP ${result.status_code} · ${result.resolved_ip}` : "未通过";
    row.append(name, metrics, status);
    list.append(row);
  });
  panel.replaceChildren(heading, list);
}

function labWindowSamples() {
  const hours = Number($("#lab-window")?.value || 6);
  const cutoff = Date.now() - hours * 60 * 60 * 1000;
  return cellularLabSamples.filter((sample) => Number(sample.sampled_at_ms) >= cutoff);
}

function renderLabChart(selector, samples, series) {
  const host = $(selector);
  host.replaceChildren();
  const pointsBySeries = series.map((item) => ({
    ...item,
    points: samples
      .filter((sample) => Number.isFinite(sample[item.key]))
      .map((sample) => ({ time: Number(sample.sampled_at_ms), value: Number(sample[item.key]) })),
  }));
  const allPoints = pointsBySeries.flatMap((item) => item.points);
  if (!allPoints.length) {
    const empty = document.createElement("div");
    empty.className = "chart-empty";
    empty.textContent = "暂无数据";
    host.append(empty);
    return;
  }
  const width = 640;
  const height = 190;
  const left = 48;
  const right = 14;
  const top = 15;
  const bottom = 31;
  const times = allPoints.map((point) => point.time);
  const values = allPoints.map((point) => point.value);
  let minTime = Math.min(...times);
  let maxTime = Math.max(...times);
  let minValue = Math.min(...values);
  let maxValue = Math.max(...values);
  if (minTime === maxTime) { minTime -= 30000; maxTime += 30000; }
  if (minValue === maxValue) { minValue -= 1; maxValue += 1; }
  const padding = Math.max((maxValue - minValue) * 0.12, 0.5);
  minValue -= padding;
  maxValue += padding;
  const x = (time) => left + ((time - minTime) / (maxTime - minTime)) * (width - left - right);
  const y = (value) => top + ((maxValue - value) / (maxValue - minValue)) * (height - top - bottom);
  const svgNS = "http://www.w3.org/2000/svg";
  const svg = document.createElementNS(svgNS, "svg");
  svg.setAttribute("viewBox", `0 0 ${width} ${height}`);
  svg.setAttribute("role", "img");
  svg.setAttribute("aria-label", series.map((item) => item.label).join("、") + "历史趋势");
  for (let line = 0; line <= 3; line += 1) {
    const gridY = top + (line / 3) * (height - top - bottom);
    const grid = document.createElementNS(svgNS, "line");
    grid.setAttribute("x1", left); grid.setAttribute("x2", width - right);
    grid.setAttribute("y1", gridY); grid.setAttribute("y2", gridY);
    grid.setAttribute("class", "chart-grid-line");
    svg.append(grid);
    const label = document.createElementNS(svgNS, "text");
    label.setAttribute("x", left - 7); label.setAttribute("y", gridY + 4);
    label.setAttribute("class", "chart-axis-label"); label.setAttribute("text-anchor", "end");
    label.textContent = (maxValue - (line / 3) * (maxValue - minValue)).toFixed(maxValue - minValue < 10 ? 1 : 0);
    svg.append(label);
  }
  const timeFormat = new Intl.DateTimeFormat("zh-CN", { hour: "2-digit", minute: "2-digit" });
  [[minTime, "start"], [maxTime, "end"]].forEach(([time, anchor]) => {
    const label = document.createElementNS(svgNS, "text");
    label.setAttribute("x", x(time)); label.setAttribute("y", height - 8);
    label.setAttribute("class", "chart-axis-label"); label.setAttribute("text-anchor", anchor);
    label.textContent = timeFormat.format(new Date(time));
    svg.append(label);
  });
  pointsBySeries.forEach((item) => {
    if (!item.points.length) return;
    const polyline = document.createElementNS(svgNS, "polyline");
    polyline.setAttribute("points", item.points.map((point) => `${x(point.time).toFixed(1)},${y(point.value).toFixed(1)}`).join(" "));
    polyline.setAttribute("fill", "none");
    polyline.setAttribute("stroke", item.color);
    polyline.setAttribute("stroke-width", "2.4");
    polyline.setAttribute("vector-effect", "non-scaling-stroke");
    svg.append(polyline);
  });
  host.append(svg);
  if (series.length > 1) {
    const legend = document.createElement("div");
    legend.className = "chart-legend";
    series.forEach((item) => {
      const label = document.createElement("span");
      label.style.setProperty("--legend-color", item.color);
      label.textContent = item.label;
      legend.append(label);
    });
    host.append(legend);
  }
}

function renderCellularLab() {
  const samples = labWindowSamples();
  const latest = cellularLabSamples.at(-1) || {};
  const latency = latestLabMetric(cellularLabSamples, "latency_ms");
  const loss = latestLabMetric(cellularLabSamples, "packet_loss_percent");
  const speed = latestLabMetric(cellularLabSamples, "download_mbps");
  const webSample = latestLabSampleWith(cellularLabSamples, "web_probe");
  const web = webSample?.web_probe;
  const diagnosis = cellularLabDiagnosis(latest, speed, webSample);
  $("#lab-current").replaceChildren(
    diagnosticCard("RSRP", labMetric(latest.rsrp, " dBm"), "参考信号功率"),
    diagnosticCard("RSRQ", labMetric(latest.rsrq, " dB"), "参考信号质量"),
    diagnosticCard("SINR", labMetric(latest.sinr, " dB"), "信号与干扰噪声比"),
    diagnosticCard("频段 / 信道", [latest.band, latest.channel ? `EARFCN ${latest.channel}` : ""].filter(Boolean).join(" · ") || "--", `${latest.network_mode || ""} ${latest.duplex || ""}`.trim()),
    diagnosticCard("服务小区", latest.cell_id || "--", [`PCI ${latest.pci ?? "--"}`, `TAC ${latest.tac || "--"}`, `${latest.mcc || ""}${latest.mnc || ""}`].join(" · ")),
    diagnosticCard("延迟 / 丢包", latency === null ? "--" : `${labMetric(latency, " ms", 1)} · ${labMetric(loss, "%", 1)}`, "每分钟探测阿里公共 DNS 10 次"),
    diagnosticCard("Cloudflare 国际路径", speed === null ? "尚未测速" : labMetric(speed, " Mbps", 2), "不代表国内综合网速"),
    diagnosticCard("国内网页", web ? `成功 ${web.success_count}/${web.target_count}` : "尚未测试", web ? `中位 TTFB ${labMetric(web.median_ttfb_ms, " ms", 1)} · 总计 ${labMetric(web.median_total_ms, " ms", 1)}` : "百度、腾讯云、京东低流量测试"),
    diagnosticCard("综合判断", diagnosis.value, diagnosis.detail),
  );
  renderWebProbeResults(webSample);
  const routeText = latest.route_interface || "未知";
  const sampled = latest.sampled_at_ms ? new Date(latest.sampled_at_ms).toLocaleTimeString("zh-CN", { hour12: false }) : "--";
  $("#lab-status").textContent = latest.cellular_route
    ? `4G USB 网卡是默认出口 · 最近采样 ${sampled} · ${cellularLabSamples.length} 个历史点`
    : `无线指标可用；默认出口为 ${routeText}，延迟、丢包和测速暂不计入，避免混入 Wi-Fi/VPN 数据 · 最近采样 ${sampled}`;
  renderLabChart("#chart-rsrp", samples, [{ key: "rsrp", label: "RSRP", color: "#2563eb" }]);
  renderLabChart("#chart-quality", samples, [
    { key: "rsrq", label: "RSRQ", color: "#8b5cf6" },
    { key: "sinr", label: "SINR", color: "#16a34a" },
  ]);
  renderLabChart("#chart-latency", samples, [{ key: "latency_ms", label: "延迟", color: "#ea580c" }]);
  renderLabChart("#chart-loss", samples, [{ key: "packet_loss_percent", label: "丢包", color: "#dc2626" }]);
  renderLabChart("#chart-speed", samples, [{ key: "download_mbps", label: "下载", color: "#0891b2" }]);
  renderLabChart("#chart-web", samples, [
    { key: "web_ttfb_ms", label: "TTFB", color: "#7c3aed" },
    { key: "web_total_ms", label: "32 KB 总耗时", color: "#0f766e" },
  ]);
}

function observedLTEBands() {
  const bands = new Set();
  cellularLabSamples.forEach((sample) => {
    const match = String(sample.band || "").match(/(\d+)/);
    if (match) bands.add(Number(match[1]));
  });
  return bands;
}

function isBroadnetOperator(value) {
  const normalized = String(value || "").trim().toUpperCase();
  return ["中国广电", "CBN", "CHN-CBN", "CHINA BROADNET", "46015"].includes(normalized);
}

function createCarrierBadge(carrier, compact = false) {
  const badge = document.createElement("span");
  badge.className = `carrier-badge ${carrier}${compact ? " compact-badge" : ""}`;
  badge.textContent = carrier === "broadnet" ? "广电共享" : carrierLabels[carrier];
  return badge;
}

function createBandOption(band, info, enabled, observed, servingBand, broadnet) {
  const label = document.createElement("label");
  label.className = "band-option";
  const isServing = Number(servingBand) === Number(band);
  if (isServing) label.classList.add("serving");
  if (observed.has(Number(band))) label.classList.add("observed");
  if (broadnet && info.carriers?.includes("mobile")) label.classList.add("broadnet-candidate");

  const input = document.createElement("input");
  input.type = "checkbox";
  input.name = "lte-band";
  input.value = String(band);
  input.checked = enabled.has(Number(band));

  const heading = document.createElement("div");
  heading.className = "band-option-heading";
  const title = document.createElement("strong");
  title.textContent = `B${band}`;
  const frequency = document.createElement("small");
  frequency.textContent = `${info.frequency} · ${info.duplex}`;
  heading.append(title, frequency);

  const carriers = document.createElement("div");
  carriers.className = "carrier-badges";
  (info.carriers || []).forEach((carrier) => carriers.append(createCarrierBadge(carrier)));
  if (broadnet && info.carriers?.includes("mobile")) carriers.append(createCarrierBadge("broadnet"));
  if (!info.carriers?.length) {
    const roaming = document.createElement("span");
    roaming.className = "carrier-badge roaming";
    roaming.textContent = info.note || "境外 / 漫游";
    carriers.append(roaming);
  }
  label.append(input, heading, carriers);
  return label;
}

function createBandGroup(title, detail, bands, infoMap, enabled, observed, servingBand, broadnet) {
  const section = document.createElement("section");
  section.className = "band-group";
  const heading = document.createElement("div");
  heading.className = "band-group-heading";
  const text = document.createElement("div");
  const name = document.createElement("h3");
  name.textContent = title;
  const description = document.createElement("p");
  description.textContent = detail;
  text.append(name, description);
  heading.append(text);
  if (title.includes("中国大陆")) {
    const legend = document.createElement("div");
    legend.className = "carrier-legend";
    ["mobile", "unicom", "telecom"].forEach((carrier) => legend.append(createCarrierBadge(carrier, true)));
    if (broadnet) legend.append(createCarrierBadge("broadnet", true));
    heading.append(legend);
  }
  const grid = document.createElement("div");
  grid.className = "band-grid";
  bands.forEach((band) => grid.append(createBandOption(band, infoMap.get(band) || { frequency: "--", duplex: "LTE" }, enabled, observed, servingBand, broadnet)));
  section.append(heading, grid);
  return section;
}

function renderBandPreference(status) {
  bandPreference = status;
  const supported = Array.isArray(status.supported_bands) ? status.supported_bands : [];
  const enabled = new Set(Array.isArray(status.enabled_bands) ? status.enabled_bands : []);
  const observed = observedLTEBands();
  const operation = status.operation || {};
  const broadnet = isBroadnetOperator(currentOperator);
  const modeLabels = {
    auto: "自动选频",
    preferred: enabled.size === 1 ? "单频锁定" : "优选频段",
    custom: "外部自定义",
  };
  $("#band-summary").replaceChildren(
    diagnosticCard("当前驻留", status.serving_band ? `LTE B${status.serving_band}` : "待读取", "网络仍会在允许频段内自主选择小区"),
    diagnosticCard("选择策略", modeLabels[status.mode] || status.mode, status.mode === "auto" ? `模块可在 ${supported.length} 个频段中选择` : `当前允许 ${enabled.size} 个频段`),
    diagnosticCard("安全恢复", status.can_restore ? "原始配置已保存" : "首次修改时保存", operation.rolled_back ? "最近一次失败已自动回退" : `失联 ${status.registration_timeout_seconds || 75} 秒自动回退`),
  );

  const domesticBands = supported.filter((band) => mainlandLTEBandInfo.has(Number(band)));
  const otherBands = supported.filter((band) => !mainlandLTEBandInfo.has(Number(band)));
  $("#band-options").replaceChildren(
    createBandGroup(
      "中国大陆常用 4G",
      broadnet ? "广电4G可使用移动共享网络；当前卡和当地网络最终决定可接入频段。" : "按工信部频率许可与常见 LTE Band 映射整理。",
      domesticBands,
      mainlandLTEBandInfo,
      enabled,
      observed,
      status.serving_band,
      broadnet,
    ),
    createBandGroup(
      "其他地区 / 漫游频段",
      "模块硬件允许，但不属于中国大陆常规 LTE 主力频段。",
      otherBands,
      otherLTEBandInfo,
      enabled,
      observed,
      status.serving_band,
      broadnet,
    ),
  );

  const operatorNote = $("#band-operator-note");
  if (broadnet) {
    operatorNote.className = "band-operator-note broadnet-note";
    operatorNote.textContent = `当前 SIM：中国广电。当前真机驻留 LTE B${status.serving_band || "--"}；带“广电共享”的频段来自中国移动4G网络候选。广电700MHz在国内主要用于5G，本工具仅处理4G，因此不把B28列为广电4G主力频段。`;
  } else {
    operatorNote.className = "band-operator-note";
    operatorNote.textContent = `当前运营商：${displayOperatorName(currentOperator) || "待读取"}。运营商标签表示中国大陆常见网络归属，不保证当地一定部署。`;
  }
  $("#band-selector").disabled = Boolean(operation.in_progress);
  $("#band-apply").disabled = Boolean(operation.in_progress) || supported.length === 0;
  $("#band-auto").disabled = Boolean(operation.in_progress) || (!status.can_restore && status.mode === "auto");

  if (operation.in_progress) {
    $("#band-status").textContent = `${operation.message || "正在切换频段"}，请保持模块连接；失败会自动恢复。`;
  } else if (operation.phase === "failed") {
    $("#band-status").textContent = `${operation.message || "频段切换失败"}：${operation.last_error || "请重新读取状态"}`;
  } else if (operation.message) {
    $("#band-status").textContent = operation.message;
  } else if (status.mode === "auto") {
    $("#band-status").textContent = `当前为自动选频，模块正在 LTE B${status.serving_band || "--"}；取消不需要的频段后可应用白名单。`;
  } else {
    $("#band-status").textContent = `当前允许：${[...enabled].map((band) => `B${band}`).join("、") || "--"}。`;
  }
}

function scheduleBandPreferencePoll(inProgress) {
  clearTimeout(bandPreferenceTimer);
  bandPreferenceTimer = null;
  if (inProgress) {
    bandPreferenceTimer = setTimeout(() => loadBandPreference(true), 2000);
  }
}

async function loadBandPreference(silent = false) {
  if (bandPreferenceInFlight) return;
  bandPreferenceInFlight = true;
  if (!silent) $("#band-status").textContent = "正在读取模块允许的 LTE 频段...";
  try {
    const status = await api("/api/network/bands");
    renderBandPreference(status);
    scheduleBandPreferencePoll(Boolean(status.operation?.in_progress));
  } catch (error) {
    $("#band-status").textContent = `读取频段配置失败：${error.message}`;
    $("#band-apply").disabled = true;
    $("#band-auto").disabled = true;
    scheduleBandPreferencePoll(Boolean(bandPreference?.operation?.in_progress));
  } finally {
    bandPreferenceInFlight = false;
  }
}

async function applyBandPreference(mode) {
  const selected = [...document.querySelectorAll('input[name="lte-band"]:checked')].map((input) => Number(input.value));
  if (mode === "preferred" && selected.length === 0) {
    notice("至少选择一个 LTE 频段");
    return;
  }
  const singleBand = mode === "preferred" && selected.length === 1;
  const currentBand = bandPreference?.serving_band ? `当前驻留 B${bandPreference.serving_band}。` : "";
  const confirmed = await showModal({
    title: mode === "auto" ? "恢复自动选频" : (singleBand ? `锁定 LTE B${selected[0]}` : "应用频段白名单"),
    message: mode === "auto"
      ? "将恢复此设备首次修改前保存的原始频段掩码，蜂窝网络可能短暂重选。"
      : `${currentBand} 将只允许搜索 ${selected.map((band) => `B${band}`).join("、")}。切换期间网络可能短暂中断，75 秒内未恢复会自动回退。`,
    confirmLabel: mode === "auto" ? "确认恢复" : "确认应用",
    danger: singleBand,
  });
  if (!confirmed) return;
  $("#band-apply").disabled = true;
  $("#band-auto").disabled = true;
  $("#band-selector").disabled = true;
  try {
    await api("/api/network/bands", {
      method: "POST",
      body: JSON.stringify({ mode, bands: mode === "preferred" ? selected : [] }),
    });
    notice(mode === "auto" ? "正在恢复自动选频" : "正在应用频段偏好");
    await loadBandPreference(true);
  } catch (error) {
    notice(error.message);
    await loadBandPreference(true);
  }
}

async function loadCellularLab() {
  if (cellularLabInFlight) return;
  cellularLabInFlight = true;
  try {
    const result = await api("/api/network/lab");
    cellularLabSamples = Array.isArray(result.samples) ? result.samples : [];
    renderCellularLab();
  } catch (error) {
    $("#lab-status").textContent = `读取蜂窝实验数据失败：${error.message}`;
  } finally {
    cellularLabInFlight = false;
  }
}

function setCellularLabPolling(enabled) {
  clearInterval(cellularLabTimer);
  cellularLabTimer = null;
  if (!enabled) return;
  void loadCellularLab();
  cellularLabTimer = setInterval(loadCellularLab, 30000);
}

async function runNetworkCheck(label, path, button) {
  button.disabled = true;
  try {
    const result = await api(path, { method: "POST" });
    renderNetworkCheck(label, result);
    notice(result.summary || "检测完成");
  } catch (error) {
    renderNetworkCheck(label, { ok: false, summary: "检测失败", detail: error.message });
    notice(error.message);
  } finally {
    button.disabled = false;
  }
}

async function loadNetwork() {
  const grid = $("#network-grid");
  const ifaceList = $("#network-interfaces");
  $("#network-status").textContent = "正在读取网络诊断...";
  try {
    const diag = await api("/api/network");
    const active = Array.isArray(diag.active_contexts) ? diag.active_contexts.join(", ") : "";
    const apns = Array.isArray(diag.pdp_contexts)
      ? diag.pdp_contexts.map((ctx) => `${ctx.id}:${ctx.apn}`).join(" · ")
      : "";
    const addresses = Array.isArray(diag.pdp_addresses) ? diag.pdp_addresses.join(" · ") : "";
    const usb = diag.usb_device
      ? `${diag.usb_device.vendor || ""} ${diag.usb_device.product || ""} (${diag.usb_device.vendor_id}:${diag.usb_device.product_id})`
      : "未检测到";
    const route = diag.default_route || {};
    const routeText = route.interface
      ? `${route.interface}${route.gateway ? ` -> ${route.gateway}` : ""}`
      : "未知";
    grid.replaceChildren(
      diagnosticCard("USB 网卡", diag.usb_network_present ? "已识别" : "未识别", "macOS 是否出现可用 USB 网络接口"),
      diagnosticCard("默认出口", routeText, "当前 macOS 实际优先使用的网卡和网关"),
      diagnosticCard("usbnet", diag.usbnet_mode || "未知", "模块当前 USB 网络模式"),
      diagnosticCard("蜂窝数据", active ? `已激活 ${active}` : "未激活", "PDP context 激活状态"),
      diagnosticCard("蜂窝 IP", addresses || "无", "模块侧拿到的数据网络地址"),
      diagnosticCard("APN", apns || "无", "当前可见 PDP 配置"),
      diagnosticCard("USB 枚举", usb, diag.usb_device?.mode || ""),
      diagnosticCard(
        "DHCP 自愈",
        diag.dhcp_repair?.running ? "检测中" : (diag.dhcp_repair?.last_error ? "最近失败" : (diag.dhcp_repair?.last_success ? "正常" : "待检测")),
        diag.dhcp_repair?.last_error || (diag.dhcp_repair?.address ? `${diag.dhcp_repair.service} · ${diag.dhcp_repair.address}` : "USB 重连后自动检查 IPv4"),
      ),
    );

    const errorText = diag.errors ? ` · 错误：${Object.values(diag.errors).join("；")}` : "";
    $("#network-status").textContent = diag.usb_network_present
      ? `macOS 已识别 USB 网络接口${errorText}`
      : `蜂窝侧可能已通，但 macOS 尚未识别 USB 网卡${errorText}`;

    const interfaces = Array.isArray(diag.mac_interfaces) ? diag.mac_interfaces : [];
    if (!interfaces.length) {
      ifaceList.className = "list empty";
      ifaceList.textContent = "未读取到网络接口";
      return;
    }
    ifaceList.className = "list";
    ifaceList.replaceChildren(...interfaces.map((item) => {
      const row = document.createElement("article");
      row.className = "item";
      const name = document.createElement("strong");
      name.textContent = item.name;
      const detail = document.createElement("p");
      detail.textContent = [item.kind, item.status, item.ipv4].filter(Boolean).join(" · ");
      const status = document.createElement("small");
      status.textContent = item.status === "active" ? "active" : "inactive";
      row.append(name, detail, status);
      return row;
    }));
  } catch (error) {
    $("#network-status").textContent = `读取网络诊断失败：${error.message}`;
    grid.replaceChildren();
    ifaceList.className = "list empty";
    ifaceList.textContent = "读取失败";
    notice(error.message);
  }
}

function formatTrafficBytes(value) {
  const bytes = Math.max(0, Number(value || 0));
  const units = ["B", "KB", "MB", "GB", "TB"];
  let amount = bytes;
  let unit = 0;
  while (amount >= 1024 && unit < units.length - 1) {
    amount /= 1024;
    unit += 1;
  }
  const digits = unit === 0 ? 0 : (unit >= 3 ? 2 : amount >= 100 ? 0 : amount >= 10 ? 1 : 2);
  const display = Number(amount.toFixed(digits)).toString();
  return `${display} ${units[unit]}`;
}

function trafficGB(value) {
  return (Number(value || 0) / (1024 ** 3)).toFixed(2).replace(/\.00$/, "");
}

async function loadTrafficQuota(updateForm = false) {
  if (trafficQuotaInFlight) return;
  trafficQuotaInFlight = true;
  try {
    const quota = await api("/api/network/quota");
    const source = quota.source === "operator_sms"
      ? "10099 运营商短信"
      : quota.source === "manual" ? "手动校准" : "本机网卡估算";
    const detail = quota.partial_estimate
      ? `从 ${new Date(quota.tracking_started_at).toLocaleString()} 开始统计，无法补回此前用量`
      : `最近校准 ${new Date(quota.last_calibration_at).toLocaleString()}`;
    $("#quota-grid").replaceChildren(
      diagnosticCard("本月剩余", formatTrafficBytes(quota.remaining_bytes), source),
      diagnosticCard("本月已用", formatTrafficBytes(quota.used_bytes), quota.used_known ? detail : `本机累计口径 · ${detail}`),
      diagnosticCard("套餐总量", formatTrafficBytes(quota.plan_total_bytes), `基础 ${formatTrafficBytes(quota.base_quota_bytes)} · 结转 ${formatTrafficBytes(quota.rollover_bytes)}`),
    );
    const queryState = quota.query_configured
      ? (quota.auto_query ? "每日自动校准已开启" : "已配置查询指令，仅手动查询")
      : "尚无可用的 10099 查询指令，自动发送保持关闭";
    $("#quota-status").textContent = `${source} · 本机累计 ${formatTrafficBytes(quota.local_tracked_bytes)} · ${queryState}`;
    if (updateForm || !$("#quota-config-form").contains(document.activeElement)) {
      $("#quota-base").value = trafficGB(quota.base_quota_bytes);
      $("#quota-rollover").value = trafficGB(quota.rollover_bytes);
      $("#quota-command").value = quota.query_command || "";
      $("#quota-auto").checked = Boolean(quota.auto_query);
    }
    $("#quota-query").disabled = !quota.query_configured;
  } catch (error) {
    $("#quota-status").textContent = `读取月度流量失败：${error.message}`;
  } finally {
    trafficQuotaInFlight = false;
  }
}

async function loadNetworkTraffic() {
  if (networkTrafficInFlight) return;
  networkTrafficInFlight = true;
  try {
    const sample = await api("/api/network/traffic");
    if (!sample.available) {
      networkTrafficPrevious = null;
      setValue("#traffic-rx-rate", "--", "muted");
      setValue("#traffic-tx-rate", "--", "muted");
      setValue("#traffic-session-rx", "--", "muted");
      setValue("#traffic-session-tx", "--", "muted");
      setValue("#traffic-session-total", "--", "muted");
      return;
    }

    let rxRate = 0;
    let txRate = 0;
    const previous = networkTrafficPrevious;
    if (previous && previous.interface === sample.interface) {
      const elapsed = (Number(sample.sampled_at_ms) - Number(previous.sampled_at_ms)) / 1000;
      if (elapsed > 0) {
        rxRate = Math.max(0, Number(sample.rx_bytes) - Number(previous.rx_bytes)) / elapsed;
        txRate = Math.max(0, Number(sample.tx_bytes) - Number(previous.tx_bytes)) / elapsed;
      }
    }
    networkTrafficPrevious = sample;
    setValue("#traffic-rx-rate", `${formatTrafficBytes(rxRate)}/s`, "neutral");
    setValue("#traffic-tx-rate", `${formatTrafficBytes(txRate)}/s`, "neutral");
    setValue("#traffic-session-rx", formatTrafficBytes(sample.session_rx_bytes), "neutral");
    setValue("#traffic-session-tx", formatTrafficBytes(sample.session_tx_bytes), "neutral");
    setValue("#traffic-session-total", formatTrafficBytes(sample.session_total_bytes), "emphasis");
    $("#traffic-session-total").title = "本次启动期间的下载与上传流量之和；关闭 DJOneHub 后清零";
  } catch (error) {
    setValue("#traffic-rx-rate", "--", "muted");
    setValue("#traffic-tx-rate", "--", "muted");
    setValue("#traffic-session-total", "--", "muted");
  } finally {
    networkTrafficInFlight = false;
  }
}

function setNetworkTrafficPolling(enabled) {
  clearInterval(networkTrafficTimer);
  networkTrafficTimer = null;
  if (!enabled) {
    networkTrafficPrevious = null;
    return;
  }
  void loadNetworkTraffic();
  networkTrafficTimer = setInterval(loadNetworkTraffic, 1000);
}

async function setUSBNetMode(mode) {
  const label = `模式 ${mode}`;
  const confirmed = await showModal({
    title: `切换到${label}`,
    message: `将写入 usbnet=${mode}，重启模块后生效。`,
    confirmLabel: "继续切换",
  });
  if (!confirmed) return;
  try {
    const result = await api("/api/network/usbnet", {
      method: "POST",
      body: JSON.stringify({ mode }),
    });
    notice(`usbnet 已写入 ${result.mode}，请重启模块`);
    await loadNetwork();
  } catch (error) {
    notice(error.message);
  }
}

async function switchWorkMode(mode, label, button) {
  const confirmed = await showModal({
    title: `切换到${label}`,
    message: `将写入 usbnet=${mode} 并重启模块，USB 会短暂断开。`,
    confirmLabel: "确认切换",
  });
  if (!confirmed) return;
  const status = $("#workmode-status");
  const buttons = [$("#workmode-sms"), $("#workmode-network")];
  buttons.forEach((item) => { item.disabled = true; });
  status.hidden = false;
  status.textContent = `正在切到${label}...`;
  try {
    const result = await api("/api/network/usbnet", {
      method: "POST",
      body: JSON.stringify({ mode }),
    });
    status.textContent = `usbnet 已写入 ${result.mode}，正在重启模块...`;
    await api("/api/network/reboot-module", { method: "POST" });
    status.textContent = `${label}已写入，等待模块重新枚举后自动刷新。`;
    notice(`${label}切换中`);
    setTimeout(loadStatus, 8000);
    setTimeout(loadNetwork, 12000);
    setTimeout(() => {
      status.textContent = `${label}切换完成后，请确认状态卡和网络诊断。`;
      buttons.forEach((item) => { item.disabled = false; });
    }, 13000);
  } catch (error) {
    status.hidden = false;
    status.textContent = `${label}切换失败：${error.message}`;
    notice(error.message);
    buttons.forEach((item) => { item.disabled = false; });
  }
}

async function rebootModule() {
  const confirmed = await showModal({
    title: "重启模块",
    message: "模块会重新枚举 USB，网页可能短暂断开。",
    confirmLabel: "确认重启",
  });
  if (!confirmed) return;
  try {
    await api("/api/network/reboot-module", { method: "POST" });
    notice("模块正在重启，稍后刷新状态");
    setTimeout(loadStatus, 8000);
    setTimeout(loadNetwork, 12000);
  } catch (error) {
    notice(error.message);
  }
}

function egressOption(value, label, selected) {
  const option = document.createElement("option");
  option.value = value;
  option.textContent = label;
  option.selected = value === selected;
  return option;
}

function renderEgressDestinations(rules = []) {
  const list = $("#egress-destinations");
  list.replaceChildren(...rules.map((rule) => {
    const row = document.createElement("div");
    row.className = "egress-rule-row destination";
    const kind = document.createElement("select");
    kind.className = "egress-destination-kind";
    kind.append(
      egressOption("domain", "网站域名", rule.kind),
      egressOption("ip", "IPv4 地址", rule.kind),
      egressOption("cidr", "IPv4 CIDR", rule.kind),
    );
    const value = document.createElement("input");
    value.className = "egress-destination-value";
    value.value = rule.value || "";
    value.placeholder = rule.kind === "domain" ? "例如 example.com" : "例如 203.0.113.10";
    value.spellcheck = false;
    const policy = document.createElement("select");
    policy.className = "egress-destination-policy";
    policy.append(
      egressOption("corporate_direct", "公司网络直连", rule.policy),
      egressOption("cellular_direct", "4G 模块直连", rule.policy),
    );
    const remove = document.createElement("button");
    remove.type = "button";
    remove.className = "secondary danger compact";
    remove.textContent = "移除";
    remove.addEventListener("click", () => row.remove());
    row.append(kind, value, policy, remove);
    return row;
  }));
}

function renderEgressApplications(rules = []) {
  const list = $("#egress-applications");
  list.replaceChildren(...rules.map((rule) => {
    const row = document.createElement("div");
    row.className = "egress-rule-row application";
    row.dataset.app = JSON.stringify(rule);
    const identity = document.createElement("div");
    identity.className = "egress-app-identity";
    const name = document.createElement("strong");
    name.textContent = rule.name || rule.path;
    const path = document.createElement("small");
    path.textContent = rule.bundle_id ? `${rule.bundle_id} · ${rule.path}` : rule.path;
    identity.append(name, path);
    const policy = document.createElement("select");
    policy.className = "egress-application-policy";
    policy.append(
      egressOption("follow_global", "跟随全局", rule.policy),
      egressOption("corporate_direct", "公司网络直连", rule.policy),
      egressOption("cellular_direct", "4G 模块直连", rule.policy),
      egressOption("vpn", "走 VPN（随全局底层）", rule.policy),
    );
    const remove = document.createElement("button");
    remove.type = "button";
    remove.className = "secondary danger compact";
    remove.textContent = "移除";
    remove.addEventListener("click", () => row.remove());
    row.append(identity, policy, remove);
    return row;
  }));
}

function collectEgressPolicy() {
  const destinations = [...document.querySelectorAll("#egress-destinations .destination")]
    .map((row) => ({
      kind: row.querySelector(".egress-destination-kind").value,
      value: row.querySelector(".egress-destination-value").value.trim(),
      policy: row.querySelector(".egress-destination-policy").value,
    }))
    .filter((rule) => rule.value);
  const applications = [...document.querySelectorAll("#egress-applications .application")].map((row) => {
    const rule = JSON.parse(row.dataset.app);
    rule.policy = row.querySelector(".egress-application-policy").value;
    return rule;
  });
  return {
    enabled: $("#egress-enabled").checked,
    global_underlay: $("#egress-underlay").value,
    vpn_policy: $("#egress-vpn-policy").value.trim(),
    corporate_cidrs: $("#egress-cidrs").value.split(/\r?\n/).map((value) => value.trim()).filter(Boolean),
    destinations,
    applications,
  };
}

function populateEgressForm(policy) {
  egressPolicyDraft = policy;
  $("#egress-enabled").checked = policy.enabled !== false;
  $("#egress-underlay").value = policy.global_underlay || "corporate";
  $("#egress-vpn-policy").value = policy.vpn_policy || "Proxy";
  $("#egress-cidrs").value = (policy.corporate_cidrs || []).join("\n");
  renderEgressDestinations(policy.destinations || []);
  renderEgressApplications(policy.applications || []);
}

function renderEgressPreview(preview) {
  const runtime = preview.runtime || {};
  const corporate = runtime.corporate || {};
  const cellular = runtime.cellular || {};
  const client = runtime.client || {};
  $("#egress-runtime").replaceChildren(
    diagnosticCard("当前逻辑出口", runtime.logical_interface || "未知", runtime.tunnel_active ? "VPN 隧道已启用" : "当前未走 VPN 隧道"),
    diagnosticCard("当前实体出口", runtime.physical_name || runtime.physical_interface || "未知", runtime.physical_interface || ""),
    diagnosticCard("公司网络", corporate.ready ? `${corporate.interface} → ${corporate.gateway}` : "未就绪", corporate.service || "未发现服务"),
    diagnosticCard("4G 模块", cellular.ready ? `${cellular.interface} → ${cellular.gateway}` : "未就绪", cellular.service || "未发现服务"),
    diagnosticCard("Stash 客户端", client.platform || "未确认", client.detail || ""),
    diagnosticCard("应用规则", client.process_rules_supported ? "可生效" : "当前不生效", `${preview.application_count || 0} 个应用已配置`),
  );
  $("#egress-status").textContent = runtime.tunnel_active
    ? `VPN 当前由 ${runtime.physical_name || runtime.physical_interface || "未知实体网络"} 承载`
    : `当前默认出口是 ${runtime.physical_name || runtime.physical_interface || "未知实体网络"}`;

  const warning = $("#egress-warning");
  const warnings = preview.warnings || [];
  warning.hidden = warnings.length === 0;
  warning.replaceChildren();
  if (warnings.length) {
    const list = document.createElement("ul");
    warnings.forEach((message) => {
      const item = document.createElement("li");
      item.textContent = message;
      list.append(item);
    });
    warning.append(list);
  }

  const serviceList = document.createElement("ul");
  (preview.service_order || []).forEach((service, index) => {
    const item = document.createElement("li");
    item.textContent = `${index + 1}. ${service}`;
    serviceList.append(item);
  });
  const routeList = document.createElement("ul");
  (preview.routes || []).forEach((route) => {
    const item = document.createElement("li");
    if (route.kind === "domain" && !route.resolved?.length) {
      item.textContent = `${route.target} → 仅生成 Stash 域名规则（未写系统路由）`;
    } else {
      const resolution = route.resolved?.length ? `（${route.resolved.join(", ")}）` : "";
      item.textContent = `${route.target}${resolution} → ${route.interface || "?"} / ${route.gateway || "?"}`;
    }
    routeList.append(item);
  });
  if (!(preview.routes || []).length) {
    const item = document.createElement("li");
    item.textContent = "没有目标路由";
    routeList.append(item);
  }
  const serviceBlock = document.createElement("div");
  serviceBlock.className = "egress-preview-block";
  const serviceTitle = document.createElement("h3");
  serviceTitle.textContent = "macOS 网络服务顺序";
  serviceBlock.append(serviceTitle, serviceList);
  const routeBlock = document.createElement("div");
  routeBlock.className = "egress-preview-block";
  const routeTitle = document.createElement("h3");
  routeTitle.textContent = "目标路由";
  routeBlock.append(routeTitle, routeList);
  const grid = document.createElement("div");
  grid.className = "egress-preview-grid";
  grid.append(serviceBlock, routeBlock);
  const code = document.createElement("pre");
  code.textContent = preview.stash_override || "# 尚未生成 Override";
  $("#egress-preview-panel").replaceChildren(grid, code);
  $("#egress-apply").disabled = !preview.can_apply;
  $("#egress-restore").disabled = !preview.can_restore;
}

async function loadEgressPolicy() {
  if (egressLoading) return;
  egressLoading = true;
  try {
    const [result, appsResult] = await Promise.all([
      api("/api/egress"),
      egressInstalledApps.length ? Promise.resolve({ applications: egressInstalledApps }) : api("/api/egress/apps"),
    ]);
    egressInstalledApps = appsResult.applications || [];
    const picker = $("#egress-app-picker");
    picker.replaceChildren(egressOption("", "选择已安装应用...", ""), ...egressInstalledApps.map((app, index) => egressOption(String(index), app.name, "")));
    populateEgressForm(result.policy || {});
    renderEgressPreview(result.preview || {});
  } catch (error) {
    $("#egress-status").textContent = `读取出口策略失败：${error.message}`;
    notice(error.message);
  } finally {
    egressLoading = false;
  }
}

async function persistEgressPolicyFromForm(showNotice = true) {
  const result = await api("/api/egress", { method: "PUT", body: JSON.stringify(collectEgressPolicy()) });
  populateEgressForm(result.policy || {});
  renderEgressPreview(result.preview || {});
  if (showNotice) notice("出口策略已保存，尚未改动当前网络");
  return result;
}

async function previewEgressPolicyFromForm() {
  const preview = await api("/api/egress/preview", { method: "POST", body: JSON.stringify(collectEgressPolicy()) });
  renderEgressPreview(preview);
  notice("预览已更新，当前网络没有变化");
}

document.querySelectorAll(".tab").forEach((tab) => {
  tab.addEventListener("click", () => {
    document.querySelectorAll(".tab, .view").forEach((el) => el.classList.remove("active"));
    tab.classList.add("active");
    $(`#${tab.dataset.view}`).classList.add("active");
    if (tab.dataset.view === "network") {
      loadNetwork();
      loadBandPreference();
      setCellularLabPolling(true);
    } else {
      setCellularLabPolling(false);
    }
    if (tab.dataset.view === "voice") loadVoiceOverview();
    if (tab.dataset.view === "egress") loadEgressPolicy();
  });
});

$("#send-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const button = event.submitter;
  const originalLabel = button.textContent;
  button.disabled = true;
  button.textContent = "发送中";
  try {
    const result = await api("/api/sms/send", {
      method: "POST",
      body: JSON.stringify({ phone: $("#phone").value, message: $("#message").value }),
    });
    $("#message").value = "";
    const segments = Number(result.segments || 1);
    notice(segments > 1 ? `短信已发送（${segments} 个分片）` : "短信已发送");
  } catch (error) {
    notice(error.message);
  } finally {
    button.disabled = false;
    button.textContent = originalLabel;
  }
});

$("#at-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const output = $("#at-output");
  output.textContent = "执行中";
  try {
    const result = await api("/api/at", {
      method: "POST",
      body: JSON.stringify({ command: $("#at-command").value }),
    });
    output.textContent = result.response || "OK";
  } catch (error) {
    output.textContent = error.message;
  }
});

$("#call-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const number = $("#call-number").value.trim();
  const confirmed = await showModal({
    title: `拨打 ${maskPhoneNumber(number)}`,
    message: "将使用当前 SIM 的 VoLTE/语音套餐发起真实电话，可能产生运营商费用。",
    confirmLabel: "拨打",
  });
  if (!confirmed) return;
  await runVoiceAction(
    event.submitter,
    "/api/voice/dial",
    { number, with_audio: $("#call-with-audio").checked },
    "拨号指令已发出",
  );
});

$("#call-answer").addEventListener("click", () => runVoiceAction(
  $("#call-answer"), "/api/voice/answer", { with_audio: $("#call-with-audio").checked }, "已接听",
));
$("#call-hangup").addEventListener("click", () => runVoiceAction(
  $("#call-hangup"), "/api/voice/hangup", {}, "通话已挂断",
));
$("#voice-audio-start").addEventListener("click", () => runVoiceAction(
  $("#voice-audio-start"), "/api/voice/audio/start", {}, "Mac 双向音频桥已启动",
));
$("#voice-audio-stop").addEventListener("click", () => runVoiceAction(
  $("#voice-audio-stop"), "/api/voice/audio/stop", {}, "Mac 双向音频桥已停止",
));
$("#voice-recording-start").addEventListener("click", async () => {
  const confirmed = await showModal({
    title: "开始通话录音",
    message: "将把对方声音和本机麦克风分别写入 WAV 左右声道。开始前请确认已告知通话对方并取得必要同意。",
    confirmLabel: "开始录音",
  });
  if (!confirmed) return;
  await runVoiceAction(
    $("#voice-recording-start"), "/api/voice/recording/start", {}, "通话录音已开始",
  );
});
$("#voice-recording-stop").addEventListener("click", () => runVoiceAction(
  $("#voice-recording-stop"), "/api/voice/recording/stop", {}, "通话录音已保存到本机",
));

$("#refresh").addEventListener("click", async () => {
  await Promise.all([loadStatus(), loadSMS()]);
  notice("状态已刷新");
});
$("#refresh-sms").addEventListener("click", async () => {
  const button = $("#refresh-sms");
  button.disabled = true;
  $("#sms-status").textContent = "正在读取短信...";
  try {
    const result = await api("/api/sms/refresh", { method: "POST" });
    await loadSMS();
    $("#sms-status").textContent = `短信读取完成：${result.count ?? "未知"} 条`;
    notice("短信读取完成");
  } catch (error) {
    $("#sms-status").textContent = `读取短信失败：${error.message}`;
    notice(error.message);
  } finally {
    button.disabled = false;
  }
});
$("#clear-module-sms").addEventListener("click", async () => {
  const confirmed = await showModal({
    title: "清空模块旧短信",
    message: "只会清空模块内部 ME 存储里的旧短信，不会删除 SIM 卡短信。",
    confirmLabel: "确认清空",
    danger: true,
  });
  if (!confirmed) return;
  const button = $("#clear-module-sms");
  button.disabled = true;
  $("#sms-status").textContent = "正在清空模块内部旧短信...";
  try {
    const result = await api("/api/sms/clear-module", { method: "POST" });
    $("#sms-status").textContent = `模块旧短信已清理：${result.before ?? 0} -> ${result.after ?? 0} 条`;
    await loadSMS();
    notice("模块旧短信已清理");
  } catch (error) {
    $("#sms-status").textContent = `清理模块旧短信失败：${error.message}`;
    notice(error.message);
  } finally {
    button.disabled = false;
  }
});
$("#refresh-network").addEventListener("click", loadNetwork);
$("#band-refresh").addEventListener("click", () => loadBandPreference());
$("#band-apply").addEventListener("click", () => applyBandPreference("preferred"));
$("#band-auto").addEventListener("click", () => applyBandPreference("auto"));
$("#lab-window").addEventListener("change", renderCellularLab);
$("#lab-sample").addEventListener("click", async () => {
  const button = $("#lab-sample");
  button.disabled = true;
  try {
    await api("/api/network/lab/sample", { method: "POST", body: "{}" });
    await loadCellularLab();
    notice("蜂窝指标已采样");
  } catch (error) {
    notice(error.message);
  } finally {
    button.disabled = false;
  }
});
$("#lab-web-test").addEventListener("click", async () => {
  const confirmed = await showModal({
    title: "测试国内网页体验",
    message: "将通过 4G USB 默认出口依次访问百度、腾讯云和京东，每站最多读取 32 KB，总流量约 100 KB。检测到 VPN、代理 Fake-IP 或非公网地址时会自动取消。",
    confirmLabel: "开始测试",
  });
  if (!confirmed) return;
  const button = $("#lab-web-test");
  button.disabled = true;
  button.textContent = "测试中...";
  try {
    const result = await api("/api/network/lab/web-test", { method: "POST", body: "{}" });
    await loadCellularLab();
    const probe = result.web_probe || {};
    notice(`国内网页测试完成：成功 ${probe.success_count || 0}/${probe.target_count || 3}`);
  } catch (error) {
    notice(error.message);
  } finally {
    button.disabled = false;
    button.textContent = "测试国内网页";
  }
});
$("#lab-speed-test").addEventListener("click", async () => {
  const confirmed = await showModal({
    title: "执行 Cloudflare 国际路径测速",
    message: "将通过当前默认出口从 Cloudflare 下载约 5 MB 测试数据。该结果只代表到 Cloudflare 的路径，不代表国内综合网速。",
    confirmLabel: "开始测速",
  });
  if (!confirmed) return;
  const button = $("#lab-speed-test");
  button.disabled = true;
  button.textContent = "测速中...";
  try {
    const result = await api("/api/network/lab/speed-test", { method: "POST", body: JSON.stringify({ bytes: 5000000 }) });
    await loadCellularLab();
    notice(`下载测速完成：${Number(result.download_mbps || 0).toFixed(2)} Mbps`);
  } catch (error) {
    notice(error.message);
  } finally {
    button.disabled = false;
    button.textContent = "5 MB 国际测速";
  }
});
$("#workmode-sms").addEventListener("click", () =>
  switchWorkMode(0, "管理兼容模式", $("#workmode-sms")));
$("#workmode-network").addEventListener("click", () =>
  switchWorkMode(1, "日常模式（上网 + 短信）", $("#workmode-network")));
$("#check-4g-route").addEventListener("click", () =>
  runNetworkCheck("4G 出口", "/api/network/check-4g", $("#check-4g-route")));
$("#check-proxy-route").addEventListener("click", () =>
  runNetworkCheck("代理", "/api/network/check-proxy", $("#check-proxy-route")));
$("#usbnet-mode-0").addEventListener("click", () => setUSBNetMode(0));
$("#usbnet-mode-1").addEventListener("click", () => setUSBNetMode(1));
$("#usbnet-mode-2").addEventListener("click", () => setUSBNetMode(2));
$("#usbnet-mode-3").addEventListener("click", () => setUSBNetMode(3));
$("#reboot-module").addEventListener("click", rebootModule);
$("#egress-add-destination").addEventListener("click", () => {
  const policy = collectEgressPolicy();
  policy.destinations.push({ kind: "domain", value: "", policy: "cellular_direct" });
  renderEgressDestinations(policy.destinations);
  $("#egress-destinations .destination:last-child input")?.focus();
});
$("#egress-add-app").addEventListener("click", () => {
  const selected = $("#egress-app-picker").value;
  const index = selected === "" ? -1 : Number(selected);
  const app = Number.isInteger(index) && index >= 0 ? egressInstalledApps[index] : null;
  if (!app) {
    notice("请先选择一个已安装应用");
    return;
  }
  const policy = collectEgressPolicy();
  if (policy.applications.some((rule) => rule.path === app.path)) {
    notice("这个应用已经在策略中");
    return;
  }
  policy.applications.push({ ...app, policy: "follow_global" });
  renderEgressApplications(policy.applications);
  $("#egress-app-picker").value = "";
});
$("#egress-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const button = event.submitter;
  button.disabled = true;
  try {
    await persistEgressPolicyFromForm();
  } catch (error) {
    notice(error.message);
  } finally {
    button.disabled = false;
  }
});
$("#egress-preview").addEventListener("click", async () => {
  const button = $("#egress-preview");
  button.disabled = true;
  try {
    await previewEgressPolicyFromForm();
  } catch (error) {
    notice(error.message);
  } finally {
    button.disabled = false;
  }
});
$("#egress-apply").addEventListener("click", async () => {
  try {
    await persistEgressPolicyFromForm(false);
    const confirmed = await showModal({
      title: "应用出口策略",
      message: "将调整 macOS 网络服务顺序并写入所列目标路由，现有 VPN 或网络连接可能短暂重连。随后系统会要求管理员授权，并保存一次可恢复快照。",
      confirmLabel: "授权并应用",
    });
    if (!confirmed) return;
    const button = $("#egress-apply");
    button.disabled = true;
    await api("/api/egress/apply", { method: "POST", body: "{}" });
    await loadEgressPolicy();
    notice("系统出口策略已应用");
  } catch (error) {
    notice(error.message);
  } finally {
    $("#egress-apply").disabled = false;
  }
});
$("#egress-restore").addEventListener("click", async () => {
  const confirmed = await showModal({
    title: "恢复应用前网络",
    message: "将删除 DJOneHub 本次新增的目标路由，并恢复应用前的 macOS 网络服务顺序。系统会要求管理员授权。",
    confirmLabel: "授权并恢复",
    danger: true,
  });
  if (!confirmed) return;
  const button = $("#egress-restore");
  button.disabled = true;
  try {
    await api("/api/egress/restore", { method: "POST", body: "{}" });
    await loadEgressPolicy();
    notice("已恢复应用前的网络设置");
  } catch (error) {
    notice(error.message);
  } finally {
    button.disabled = false;
  }
});
$("#quota-config-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const button = event.currentTarget.querySelector("button[type=submit]");
  button.disabled = true;
  try {
    await api("/api/network/quota/config", {
      method: "POST",
      body: JSON.stringify({
        base_quota_gb: Number($("#quota-base").value),
        rollover_gb: Number($("#quota-rollover").value),
        query_command: $("#quota-command").value.trim(),
        auto_query: $("#quota-auto").checked,
        manual_used_gb: $("#quota-manual-used").value === "" ? null : Number($("#quota-manual-used").value),
        manual_remaining_gb: $("#quota-manual-remaining").value === "" ? null : Number($("#quota-manual-remaining").value),
      }),
    });
    await loadTrafficQuota(true);
    $("#quota-manual-used").value = "";
    $("#quota-manual-remaining").value = "";
    notice("月度流量设置已保存");
  } catch (error) {
    notice(error.message);
  } finally {
    button.disabled = false;
  }
});
$("#quota-query").addEventListener("click", async () => {
  const button = $("#quota-query");
  button.disabled = true;
  try {
    await api("/api/network/quota/query", { method: "POST" });
    await loadTrafficQuota(true);
    notice("已向 10099 发送一次查询，24 小时内不会重复发送");
  } catch (error) {
    notice(error.message);
  } finally {
    await loadTrafficQuota();
  }
});

loadStatus();
loadSMS();
loadTrafficQuota(true);
loadVoiceOverview();
loadVoiceCalls();
setNetworkTrafficPolling(true);
setInterval(loadStatus, 10000);
setInterval(loadSMS, 5000);
setInterval(loadTrafficQuota, 30000);
setInterval(loadVoiceCalls, 1500);
