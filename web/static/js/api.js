const TOKEN_KEY = 'aw_api_token';

let unauthorizedHandler = null;
const authListeners = new Set();

export function getStoredToken() {
  try {
    return localStorage.getItem(TOKEN_KEY) || '';
  } catch {
    return '';
  }
}

export function setStoredToken(token) {
  try {
    const trimmed = String(token || '').trim();
    if (trimmed) {
      localStorage.setItem(TOKEN_KEY, trimmed);
    } else {
      localStorage.removeItem(TOKEN_KEY);
    }
  } catch {
    // ignore storage quota/privacy errors
  }
  notifyTokenChange();
}

export function clearStoredToken() {
  try {
    localStorage.removeItem(TOKEN_KEY);
  } catch {
    // ignore
  }
  notifyTokenChange();
}

export function onTokenChange(fn) {
  authListeners.add(fn);
  return () => authListeners.delete(fn);
}

function notifyTokenChange() {
  const tok = getStoredToken();
  authListeners.forEach((fn) => {
    try {
      fn(tok);
    } catch {
      // ignore listener errors
    }
  });
}

export function setUnauthorizedHandler(fn) {
  unauthorizedHandler = fn;
}

export async function request(url, options = {}) {
  const {
    method = 'GET',
    body,
    headers = {},
    skipAuthRetry = false,
    timeoutMs = 15000,
    signal: externalSignal,
  } = options;
  const reqHeaders = { ...headers };
  const token = getStoredToken();

  if (token) {
    reqHeaders['X-API-Key'] = token;
    reqHeaders['X-API-Token'] = token;
    reqHeaders['Authorization'] = `Bearer ${token}`;
  }

  let payloadBody = body;
  if (body !== undefined && body !== null && typeof body !== 'string') {
    reqHeaders['Content-Type'] = 'application/json';
    payloadBody = JSON.stringify(body);
  }

  const controller = new AbortController();
  let timedOut = false;
  let timeoutId = null;
  const onExternalAbort = () => controller.abort();

  if (externalSignal) {
    if (externalSignal.aborted) {
      const abortErr = new Error('请求已取消');
      abortErr.aborted = true;
      throw abortErr;
    }
    externalSignal.addEventListener('abort', onExternalAbort, { once: true });
  }

  if (Number.isFinite(timeoutMs) && timeoutMs > 0) {
    timeoutId = setTimeout(() => {
      timedOut = true;
      controller.abort();
    }, timeoutMs);
  }

  let res;
  let text;
  try {
    res = await fetch(url, {
      method,
      headers: reqHeaders,
      body: payloadBody,
      credentials: 'same-origin',
      signal: controller.signal,
    });

    if (res.status === 401 && url.startsWith('/api/') && !url.startsWith('/api/auth/') && !skipAuthRetry) {
      const hadToken = Boolean(token);
      clearStoredToken();
      if (unauthorizedHandler) {
        unauthorizedHandler(hadToken ? 'API Key 已失效或已变更，请重新登录。' : '当前控制台受 API Key 保护，请登录后继续。');
      }
      const authErr = new Error('需要有效 API Key 才能访问控制台');
      authErr.status = 401;
      authErr.authRequired = true;
      throw authErr;
    }

    text = await res.text();
  } catch (err) {
    if (err?.name === 'AbortError') {
      if (timedOut) {
        const timeoutErr = new Error('请求超时，服务端响应缓慢或连接已断开');
        timeoutErr.timeout = true;
        throw timeoutErr;
      }
      const abortErr = new Error('请求已取消');
      abortErr.aborted = true;
      throw abortErr;
    }
    if (err instanceof TypeError) {
      const netErr = new Error('无法连接服务端，请检查服务进程与网络状态');
      netErr.network = true;
      throw netErr;
    }
    throw err;
  } finally {
    if (timeoutId !== null) clearTimeout(timeoutId);
    if (externalSignal) externalSignal.removeEventListener('abort', onExternalAbort);
  }

  let data = null;
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = { raw: text };
    }
  }

  if (!res.ok) {
    const errMsg = (data && (data.error || data.message)) || `请求失败 (HTTP ${res.status})`;
    const err = new Error(errMsg);
    err.status = res.status;
    err.data = data;
    err.issues = data && Array.isArray(data.issues) ? data.issues : [];
    throw err;
  }

  return data;
}

function buildQuery(params = {}) {
  const qs = new URLSearchParams();
  Object.entries(params).forEach(([k, v]) => {
    if (v !== undefined && v !== null && v !== '') {
      qs.set(k, String(v));
    }
  });
  const str = qs.toString();
  return str ? `?${str}` : '';
}

export const api = {
  healthz: () => request('/healthz', { timeoutMs: 5000 }),
  readyz: () => request('/readyz', { timeoutMs: 5000 }),
  authStatus: () => request('/api/auth/status', { skipAuthRetry: true }),
  login: (apiKey) => request('/api/auth/login', { method: 'POST', body: { api_key: apiKey }, skipAuthRetry: true }),
  getOverview: () => request('/api/overview'),
  getAlerts: (params = {}, options = {}) => request(`/api/alerts${buildQuery(params)}`, options),
  clearAlerts: () => request('/api/alerts/clear', { method: 'POST' }),
  getConfig: () => request('/api/config'),
  saveConfig: (config) => request('/api/config', { method: 'POST', body: config }),
  saveConfigYaml: (yaml) => request('/api/config/yaml', { method: 'POST', body: { yaml } }),
  reloadConfig: () => request('/api/config/reload', { method: 'POST' }),
  testNotifier: (payload) => request('/api/test/notifier', { method: 'POST', body: payload, timeoutMs: 30000 }),
  testClickHouse: (cfg) => request('/api/test/clickhouse', { method: 'POST', body: cfg || {}, timeoutMs: 30000 }),
  testKafka: (cfg) => request('/api/test/kafka', { method: 'POST', body: cfg || {}, timeoutMs: 30000 }),
  testFilter: (payload) => request('/api/filter/test', { method: 'POST', body: payload }),
  simulate: (payload) => request('/api/simulate', { method: 'POST', body: payload, timeoutMs: 30000 }),
  triggerTrafficCheck: () => request('/api/services/traffic/check', { method: 'POST' }),
  triggerKafkaCheck: () => request('/api/services/kafka/check', { method: 'POST' }),
  getLogs: (params = {}) => request(`/api/logs${buildQuery(params)}`),
};

/* Shared Safe Formatting & Metadata Helpers */

export function escapeHtml(value) {
  if (value === null || value === undefined) return '';
  return String(value)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}

export function formatTimestamp(value) {
  if (!value) return '--';
  const str = String(value);
  if (str.startsWith('0001-')) return '--';
  const date = new Date(str);
  if (Number.isNaN(date.getTime()) || date.getFullYear() < 1970) {
    return '--';
  }
  const pad = (n) => String(n).padStart(2, '0');
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`;
}

export function formatUptime(seconds) {
  const s = Number(seconds);
  if (!Number.isFinite(s) || s < 0) return '--';
  const d = Math.floor(s / 86400);
  const h = Math.floor((s % 86400) / 3600);
  const m = Math.floor((s % 3600) / 60);
  const sec = Math.floor(s % 60);
  if (d > 0) return `${d}天${h}时`;
  if (h > 0) return `${h}时${m}分`;
  if (m > 0) return `${m}分${sec}秒`;
  return `${sec}秒`;
}

export function formatBytes(bytes) {
  const n = Number(bytes);
  if (!Number.isFinite(n) || n < 0) return '0 B';
  if (n < 1024) return `${Math.round(n)} B`;
  const units = ['KB', 'MB', 'GB', 'TB'];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i += 1;
  }
  return `${Number(v.toFixed(v >= 100 ? 0 : 2))} ${units[i]}`;
}

export function severityMeta(severity, severityCode = '') {
  const s = String(severity || '').toLowerCase();
  const code = severityCode ? ` (${severityCode})` : '';
  switch (s) {
    case 'emergency':
      return { label: `紧急${code || ' (P0)'}`, short: '紧急', pillClass: 'pill-emergency', color: '#ef4444' };
    case 'critical':
      return { label: `严重${code || ' (P1)'}`, short: '严重', pillClass: 'pill-critical', color: '#f97316' };
    case 'warning':
      return { label: `警告${code || ' (P2)'}`, short: '警告', pillClass: 'pill-warning', color: '#f59e0b' };
    case 'info':
      return { label: `信息${code || ' (P3)'}`, short: '信息', pillClass: 'pill-info', color: '#0ea5e9' };
    default:
      return { label: severity ? `${severity}${code}` : '其他', short: severity || '其他', pillClass: 'pill-neutral', color: '#64748b' };
  }
}

export function statusMeta(status) {
  const s = String(status || '').toLowerCase();
  if (s === 'firing') {
    return { label: '告警中', pillClass: 'pill-emergency' };
  }
  if (s === 'resolved') {
    return { label: '已恢复', pillClass: 'pill-success' };
  }
  return { label: status || '未知', pillClass: 'pill-neutral' };
}

export function deliveryStateMeta(state) {
  switch (String(state || '').toLowerCase()) {
    case 'sent':
      return { label: '已发送', pillClass: 'pill-success' };
    case 'partial':
      return { label: '部分成功', pillClass: 'pill-warning' };
    case 'failed':
      return { label: '发送失败', pillClass: 'pill-emergency' };
    case 'filtered':
      return { label: '规则拦截', pillClass: 'pill-neutral' };
    case 'dry_run':
      return { label: '沙箱演练', pillClass: 'pill-info' };
    default:
      return { label: state || '--', pillClass: 'pill-neutral' };
  }
}

export function sourceMeta(source) {
  switch (String(source || '').toLowerCase()) {
    case 'alertmanager':
      return { label: 'Alertmanager', pillClass: 'pill-accent' };
    case 'traffic':
      return { label: '流量监控', pillClass: 'pill-warning' };
    case 'kafka':
      return { label: 'Kafka 积压', pillClass: 'pill-info' };
    case 'test':
      return { label: '沙箱测试', pillClass: 'pill-neutral' };
    default:
      return { label: source || '未知', pillClass: 'pill-neutral' };
  }
}

export function clientMeta(client) {
  switch (String(client || '').toLowerCase()) {
    case 'wechat':
      return {
        key: 'wechat',
        name: '企业微信',
        short: '企微',
        placeholder: 'https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=...',
        guide: '在企业微信群右上角「群机器人」→「添加机器人」获取 Webhook 地址（包含 key 参数）。超长告警将自动分批推送。',
      };
    case 'dingtalk':
      return {
        key: 'dingtalk',
        name: '钉钉机器人',
        short: '钉钉',
        placeholder: 'https://oapi.dingtalk.com/robot/send?access_token=...',
        guide: '在钉钉群「群设置」→「智能群助手」→「自定义机器人」获取 Webhook 地址；若配置自定义关键词安全校验，请包含「告警」或「Prometheus」。',
      };
    case 'feishu':
      return {
        key: 'feishu',
        name: '飞书机器人',
        short: '飞书',
        placeholder: 'https://open.feishu.cn/open-apis/bot/v2/hook/...',
        guide: '在飞书群「设置」→「群机器人」→「自定义机器人」获取 v2 Hook 地址，支持富文本告警卡片推送。',
      };
    default:
      return {
        key: String(client || ''),
        name: String(client || '未知渠道'),
        short: String(client || '?'),
        placeholder: 'https://...',
        guide: '',
      };
  }
}

export async function copyToClipboard(text) {
  const val = String(text ?? '');
  if (navigator.clipboard && window.isSecureContext) {
    try {
      await navigator.clipboard.writeText(val);
      return true;
    } catch {
      // fallback below
    }
  }
  try {
    const ta = document.createElement('textarea');
    ta.value = val;
    ta.setAttribute('readonly', '');
    ta.style.position = 'fixed';
    ta.style.top = '-9999px';
    ta.style.left = '-9999px';
    document.body.appendChild(ta);
    ta.select();
    const ok = document.execCommand('copy');
    document.body.removeChild(ta);
    return ok;
  } catch {
    return false;
  }
}

export function debounce(fn, delay = 250) {
  let timer = null;
  return (...args) => {
    clearTimeout(timer);
    timer = setTimeout(() => fn(...args), delay);
  };
}
