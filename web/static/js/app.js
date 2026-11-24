import {
  api,
  escapeHtml,
  formatTimestamp,
  formatUptime,
  severityMeta,
  statusMeta,
  deliveryStateMeta,
  sourceMeta,
  clientMeta,
  copyToClipboard,
  getStoredToken,
  setStoredToken,
  clearStoredToken,
  onTokenChange,
  setAuthPromptHandler,
} from './api.js';
import { mountDashboard } from './views/dashboard.js';
import { mountAlerts } from './views/alerts.js';
import { mountConfig } from './views/config.js';
import { mountSimulator } from './views/simulator.js';
import { mountLogs } from './views/logs.js';

const THEME_KEY = 'aw_theme';

const ROUTES = {
  dashboard: {
    title: '监控总览',
    subtitle: '实时查看告警流量、渠道健康与后台监控服务状态',
    mount: mountDashboard,
  },
  alerts: {
    title: '告警记录',
    subtitle: '审计全部接收、转发与拦截的告警明细及渠道投递回执',
    mount: mountAlerts,
  },
  config: {
    title: '配置中心',
    subtitle: '可视化编辑通知渠道、过滤规则、监控参数与 YAML 热加载',
    mount: mountConfig,
  },
  simulator: {
    title: '调试沙箱',
    subtitle: '模拟 Alertmanager 告警负载，在线预览消息排版与过滤决策',
    mount: mountSimulator,
  },
  logs: {
    title: '运行日志',
    subtitle: '实时查看服务端结构化运行日志与故障排查信息',
    mount: mountLogs,
  },
};

const state = {
  route: 'dashboard',
  currentCleanup: null,
  currentRefresh: null,
  shouldPauseRefresh: null,
  autoRefresh: true,
  refreshTimer: null,
  authRequired: false,
  confirmResolve: null,
};

/* ---------- Theme ---------- */

function applyTheme(theme) {
  const next = theme === 'dark' ? 'dark' : 'light';
  document.documentElement.setAttribute('data-theme', next);
  try {
    localStorage.setItem(THEME_KEY, next);
  } catch {
    // ignore
  }
  const sun = document.getElementById('theme-icon-sun');
  const moon = document.getElementById('theme-icon-moon');
  if (sun && moon) {
    sun.classList.toggle('hidden', next !== 'dark');
    moon.classList.toggle('hidden', next === 'dark');
  }
}

function toggleTheme() {
  const curr = document.documentElement.getAttribute('data-theme') === 'dark' ? 'dark' : 'light';
  applyTheme(curr === 'dark' ? 'light' : 'dark');
}

/* ---------- Toast ---------- */

export function showToast(message, type = 'info', duration = 3400) {
  const container = document.getElementById('toast-container');
  if (!container) return;

  const toast = document.createElement('div');
  toast.className = `toast is-${type}`;
  toast.innerHTML = `
    <div style="flex:1;min-width:0;word-break:break-word;">${escapeHtml(message)}</div>
    <button type="button" class="icon-btn sm" style="border:none;background:transparent;" aria-label="关闭提示">×</button>
  `;
  const remove = () => {
    if (toast.parentNode) toast.parentNode.removeChild(toast);
  };
  toast.querySelector('button')?.addEventListener('click', remove);
  container.appendChild(toast);

  if (duration > 0) {
    setTimeout(remove, duration);
  }
}

/* ---------- Slide-Over Drawer ---------- */

export function openDrawer({ title, subtitle = '', bodyHtml = '', footerHtml = '', onMounted = null }) {
  const drawer = document.getElementById('detail-drawer');
  const titleEl = document.getElementById('drawer-title');
  const subEl = document.getElementById('drawer-subtitle');
  const bodyEl = document.getElementById('drawer-body');
  const footerEl = document.getElementById('drawer-footer');
  if (!drawer || !titleEl || !bodyEl) return;

  titleEl.textContent = title || '详情';
  if (subEl) subEl.textContent = subtitle;
  bodyEl.innerHTML = bodyHtml;

  if (footerEl) {
    if (footerHtml) {
      footerEl.innerHTML = footerHtml;
      footerEl.classList.remove('hidden');
    } else {
      footerEl.innerHTML = '';
      footerEl.classList.add('hidden');
    }
  }

  drawer.classList.remove('hidden');
  drawer.setAttribute('aria-hidden', 'false');

  if (typeof onMounted === 'function') {
    onMounted(bodyEl, footerEl);
  }
}

export function closeDrawer() {
  const drawer = document.getElementById('detail-drawer');
  if (!drawer) return;
  drawer.classList.add('hidden');
  drawer.setAttribute('aria-hidden', 'true');
}

export function openAlertDetail(record) {
  if (!record) return;
  const sev = severityMeta(record.severity, record.severity_code);
  const st = statusMeta(record.status);
  const delState = deliveryStateMeta(record.delivery_state);
  const src = sourceMeta(record.source);

  const labels = record.labels && typeof record.labels === 'object' ? Object.entries(record.labels) : [];
  const annotations = record.annotations && typeof record.annotations === 'object' ? Object.entries(record.annotations) : [];
  const deliveries = Array.isArray(record.deliveries) ? record.deliveries : [];

  const filterHtml = record.filter_reason
    ? `
      <div class="banner ${record.delivery_state === 'filtered' ? 'banner-warning' : 'banner-info'}">
        <div class="banner-content">
          <div>
            <div class="banner-title">过滤规则判定</div>
            <div class="banner-desc">${escapeHtml(record.filter_reason)}</div>
          </div>
        </div>
      </div>
    `
    : '';

  const labelsHtml = labels.length
    ? `<div class="card"><table class="kv-table">${labels
        .map(([k, v]) => `<tr><td class="kv-key">${escapeHtml(k)}</td><td class="kv-val">${escapeHtml(v)}</td></tr>`)
        .join('')}</table></div>`
    : '<div class="text-muted text-sm">无标签信息</div>';

  const annotationsHtml = annotations.length
    ? `<div class="card"><table class="kv-table">${annotations
        .map(([k, v]) => `<tr><td class="kv-key">${escapeHtml(k)}</td><td class="kv-val">${escapeHtml(v)}</td></tr>`)
        .join('')}</table></div>`
    : '<div class="text-muted text-sm">无注解信息</div>';

  const deliveriesHtml = deliveries.length
    ? deliveries
        .map((d) => {
          const cm = clientMeta(d.client);
          const okBadge = d.success
            ? '<span class="pill pill-success">推送成功</span>'
            : '<span class="pill pill-emergency">推送失败</span>';
          return `
            <div class="delivery-card">
              <div class="delivery-card-top">
                <div style="display:flex;align-items:center;gap:8px;">
                  <strong>${escapeHtml(cm.name)}</strong>
                  <span class="pill pill-neutral mono">${escapeHtml(d.client)}</span>
                </div>
                <div style="display:flex;align-items:center;gap:6px;">
                  ${d.status_code ? `<span class="pill pill-neutral mono">HTTP ${escapeHtml(d.status_code)}</span>` : ''}
                  ${d.batches ? `<span class="pill pill-info mono">${escapeHtml(d.batches)} 批</span>` : ''}
                  <span class="pill pill-neutral mono">${escapeHtml(d.duration_ms ?? 0)} ms</span>
                  ${okBadge}
                </div>
              </div>
              ${d.error ? `<div class="text-danger text-sm mt-2 mono">${escapeHtml(d.error)}</div>` : ''}
              ${d.response ? `<pre class="code-block mt-2" style="max-height:120px;">${escapeHtml(d.response)}</pre>` : ''}
            </div>
          `;
        })
        .join('')
    : '<div class="text-muted text-sm">无外发投递记录</div>';

  const bodyHtml = `
    <div style="display:flex;gap:8px;flex-wrap:wrap;align-items:center;">
      <span class="pill ${st.pillClass}">${escapeHtml(st.label)}</span>
      <span class="pill ${sev.pillClass}">${escapeHtml(sev.label)}</span>
      <span class="pill ${delState.pillClass}">${escapeHtml(delState.label)}</span>
      <span class="pill ${src.pillClass}">${escapeHtml(src.label)}</span>
    </div>

    ${filterHtml}

    <div>
      <div class="drawer-section-title">基础元数据</div>
      <div class="card">
        <table class="kv-table">
          <tr><td class="kv-key">记录 ID</td><td class="kv-val">${escapeHtml(record.id)}</td></tr>
          <tr><td class="kv-key">记录时间</td><td class="kv-val">${escapeHtml(formatTimestamp(record.timestamp))}</td></tr>
          <tr><td class="kv-key">实例</td><td class="kv-val">${escapeHtml(record.instance || '--')}</td></tr>
          <tr><td class="kv-key">触发时间 (StartsAt)</td><td class="kv-val">${escapeHtml(formatTimestamp(record.starts_at))}</td></tr>
          ${record.ends_at ? `<tr><td class="kv-key">恢复时间 (EndsAt)</td><td class="kv-val">${escapeHtml(formatTimestamp(record.ends_at))}</td></tr>` : ''}
        </table>
      </div>
    </div>

    <div>
      <div class="drawer-section-title">摘要与描述</div>
      <div class="card card-body">
        <div style="font-weight:600;margin-bottom:6px;">${escapeHtml(record.summary || '（无摘要）')}</div>
        <div class="text-secondary text-sm" style="white-space:pre-wrap;word-break:break-word;">${escapeHtml(record.description || '（无详细描述）')}</div>
      </div>
    </div>

    <div>
      <div class="drawer-section-title">投递回执 (${deliveries.length})</div>
      ${deliveriesHtml}
    </div>

    <div>
      <div class="drawer-section-title">Labels (${labels.length})</div>
      ${labelsHtml}
    </div>

    <div>
      <div class="drawer-section-title">Annotations (${annotations.length})</div>
      ${annotationsHtml}
    </div>
  `;

  openDrawer({
    title: record.alert_name || '告警详情',
    subtitle: `${formatTimestamp(record.timestamp)} · ${record.id || ''}`,
    bodyHtml,
    footerHtml: '<button id="drawer-copy-json" class="btn btn-secondary btn-sm" type="button">复制原始 JSON</button>',
    onMounted: (_body, footer) => {
      footer?.querySelector('#drawer-copy-json')?.addEventListener('click', async () => {
        const ok = await copyToClipboard(JSON.stringify(record, null, 2));
        showToast(ok ? '已复制告警 JSON 到剪贴板' : '复制失败', ok ? 'success' : 'error');
      });
    },
  });
}

/* ---------- Confirm Modal ---------- */

export function confirmDialog({ title = '确认操作', message = '', confirmText = '确定', danger = false } = {}) {
  const modal = document.getElementById('confirm-modal');
  const titleEl = document.getElementById('confirm-title');
  const bodyEl = document.getElementById('confirm-body');
  const okBtn = document.getElementById('confirm-ok');
  if (!modal || !titleEl || !bodyEl || !okBtn) {
    return Promise.resolve(false);
  }

  titleEl.textContent = title;
  bodyEl.innerHTML = `<p style="margin:0;line-height:1.6;">${escapeHtml(message)}</p>`;
  okBtn.textContent = confirmText;
  okBtn.className = `btn ${danger ? 'btn-danger' : 'btn-primary'}`;

  modal.classList.remove('hidden');
  modal.setAttribute('aria-hidden', 'false');

  return new Promise((resolve) => {
    state.confirmResolve = resolve;
  });
}

function finishConfirm(result) {
  const modal = document.getElementById('confirm-modal');
  if (modal) {
    modal.classList.add('hidden');
    modal.setAttribute('aria-hidden', 'true');
  }
  if (state.confirmResolve) {
    const res = state.confirmResolve;
    state.confirmResolve = null;
    res(result);
  }
}

/* ---------- Token Unlock Modal ---------- */

let tokenModalResolve = null;

function updateTokenIndicator() {
  const btn = document.getElementById('btn-token-lock');
  const label = document.getElementById('token-status-label');
  const lockedIcon = document.getElementById('token-icon-locked');
  const unlockedIcon = document.getElementById('token-icon-unlocked');
  if (!btn || !label) return;

  const hasToken = Boolean(getStoredToken());
  btn.classList.remove('is-locked', 'is-required');
  if (hasToken) {
    btn.classList.add('is-locked');
    label.textContent = '已设令牌';
    lockedIcon?.classList.remove('hidden');
    unlockedIcon?.classList.add('hidden');
  } else if (state.authRequired) {
    btn.classList.add('is-required');
    label.textContent = '待解锁';
    lockedIcon?.classList.add('hidden');
    unlockedIcon?.classList.remove('hidden');
  } else {
    label.textContent = '未设令牌';
    lockedIcon?.classList.add('hidden');
    unlockedIcon?.classList.remove('hidden');
  }
}

export function openTokenModal(reason = '') {
  const modal = document.getElementById('token-modal');
  const desc = document.getElementById('token-modal-desc');
  const input = document.getElementById('token-input');
  const errEl = document.getElementById('token-error');
  if (!modal || !input) return Promise.resolve(false);

  if (desc) {
    desc.textContent = reason || '如果服务端配置了 server.auth_token 或 ALERT_WEBHOOK_AUTH_TOKEN，请在此填写 API 访问令牌（将安全保存于当前浏览器 localStorage）。';
  }
  input.value = getStoredToken();
  input.type = 'password';
  if (errEl) {
    errEl.textContent = '';
    errEl.classList.add('hidden');
  }

  modal.classList.remove('hidden');
  modal.setAttribute('aria-hidden', 'false');
  setTimeout(() => input.focus(), 50);

  return new Promise((resolve) => {
    tokenModalResolve = resolve;
  });
}

function closeTokenModal(unlocked) {
  const modal = document.getElementById('token-modal');
  if (modal) {
    modal.classList.add('hidden');
    modal.setAttribute('aria-hidden', 'true');
  }
  if (tokenModalResolve) {
    const res = tokenModalResolve;
    tokenModalResolve = null;
    res(unlocked);
  }
}

/* ---------- Quick Test Push Modal ---------- */

function openQuickTestModal() {
  const modal = document.getElementById('quick-test-modal');
  const resultsEl = document.getElementById('quick-test-results');
  if (!modal) return;
  if (resultsEl) resultsEl.innerHTML = '';
  modal.classList.remove('hidden');
  modal.setAttribute('aria-hidden', 'false');
}

function closeQuickTestModal() {
  const modal = document.getElementById('quick-test-modal');
  if (!modal) return;
  modal.classList.add('hidden');
  modal.setAttribute('aria-hidden', 'true');
}

async function runQuickTestPush() {
  const select = document.getElementById('quick-test-client');
  const sendBtn = document.getElementById('quick-test-send');
  const resultsEl = document.getElementById('quick-test-results');
  if (!select || !sendBtn || !resultsEl) return;

  const client = select.value || 'all';
  sendBtn.disabled = true;
  sendBtn.innerHTML = '<span class="spinner"></span><span>发送中...</span>';
  resultsEl.innerHTML = '<div class="text-muted text-sm">正在发送测试消息，请稍候...</div>';

  try {
    const res = await api.testNotifier({ client });
    const list = Array.isArray(res.results) ? res.results : [];
    if (list.length === 0) {
      resultsEl.innerHTML = '<div class="banner banner-warning"><div class="banner-desc">未找到可测试的渠道，请先在配置中心启用并填写 Webhook URL。</div></div>';
    } else {
      resultsEl.innerHTML = list
        .map((r) => {
          const cm = clientMeta(r.client);
          return `
            <div class="delivery-card">
              <div class="delivery-card-top">
                <strong>${escapeHtml(cm.name)}</strong>
                <div style="display:flex;align-items:center;gap:6px;">
                  ${r.status_code ? `<span class="pill pill-neutral mono">HTTP ${escapeHtml(r.status_code)}</span>` : ''}
                  <span class="pill pill-neutral mono">${escapeHtml(r.duration_ms ?? 0)} ms</span>
                  ${r.success ? '<span class="pill pill-success">成功</span>' : '<span class="pill pill-emergency">失败</span>'}
                </div>
              </div>
              ${r.error ? `<div class="text-danger text-sm mt-2 mono">${escapeHtml(r.error)}</div>` : ''}
              ${r.response ? `<pre class="code-block mt-2" style="max-height:96px;">${escapeHtml(r.response)}</pre>` : ''}
            </div>
          `;
        })
        .join('');
    }
    showToast(res.ok ? '测试推送完成' : '部分或全部渠道测试失败', res.ok ? 'success' : 'warning');
  } catch (err) {
    resultsEl.innerHTML = `<div class="banner banner-danger"><div class="banner-desc">${escapeHtml(err.message)}</div></div>`;
  } finally {
    sendBtn.disabled = false;
    sendBtn.textContent = '发送测试';
  }
}

/* ---------- Header / Status Sync ---------- */

export function updateChromeFromOverview(data) {
  if (!data) return;

  const verEl = document.getElementById('nav-version');
  if (verEl && data.version) {
    verEl.textContent = data.version.startsWith('v') ? data.version : `v${data.version}`;
  }

  const pill = document.getElementById('topbar-status-pill');
  const statusText = document.getElementById('topbar-status-text');
  const uptimeEl = document.getElementById('topbar-uptime');
  if (pill && statusText && uptimeEl) {
    pill.classList.remove('is-loading', 'is-error');
    pill.classList.add('is-healthy');
    statusText.textContent = '运行正常';
    uptimeEl.textContent = formatUptime(data.uptime_seconds);
  }

  const firingBadge = document.getElementById('nav-firing-badge');
  const firingCount = Number(data.stats?.firing) || 0;
  if (firingBadge) {
    firingBadge.textContent = String(firingCount);
    firingBadge.classList.toggle('hidden', firingCount <= 0);
  }

  const setupDot = document.getElementById('nav-setup-dot');
  if (setupDot) {
    setupDot.classList.toggle('hidden', Boolean(data.setup_complete));
  }

  state.authRequired = Boolean(data.auth_required);
  updateTokenIndicator();
}

async function pollTopbarHealth() {
  try {
    const h = await api.healthz();
    const pill = document.getElementById('topbar-status-pill');
    const statusText = document.getElementById('topbar-status-text');
    const uptimeEl = document.getElementById('topbar-uptime');
    const verEl = document.getElementById('nav-version');
    if (pill && statusText && uptimeEl && h) {
      pill.classList.remove('is-loading', 'is-error');
      pill.classList.add('is-healthy');
      statusText.textContent = '运行正常';
      uptimeEl.textContent = formatUptime(h.uptime_seconds);
    }
    if (verEl && h?.version) {
      verEl.textContent = h.version.startsWith('v') ? h.version : `v${h.version}`;
    }
  } catch {
    const pill = document.getElementById('topbar-status-pill');
    const statusText = document.getElementById('topbar-status-text');
    if (pill && statusText) {
      pill.classList.remove('is-loading', 'is-healthy');
      pill.classList.add('is-error');
      statusText.textContent = '连接异常';
    }
  }
}

/* ---------- Routing ---------- */

function parseRouteFromHash() {
  const raw = window.location.hash.replace(/^#\/?/, '').trim();
  const routeKey = raw.split('?')[0];
  return ROUTES[routeKey] ? routeKey : 'dashboard';
}

function setMobileSidebarOpen(open) {
  const sidebar = document.getElementById('sidebar');
  const backdrop = document.getElementById('mobile-backdrop');
  sidebar?.classList.toggle('is-open', open);
  backdrop?.classList.toggle('is-open', open);
}

export function navigate(routeKey, options = {}) {
  const target = ROUTES[routeKey] ? routeKey : 'dashboard';
  if (window.location.hash !== `#/${target}`) {
    window.location.hash = `#/${target}`;
    if (!options.force) return;
  }
  mountRoute(target);
}

function createViewContext() {
  return {
    navigate,
    toast: showToast,
    openDrawer,
    closeDrawer,
    openAlertDetail,
    confirm: confirmDialog,
    onOverviewUpdated: updateChromeFromOverview,
    openTokenModal,
  };
}

function mountRoute(routeKey) {
  const def = ROUTES[routeKey] || ROUTES.dashboard;
  state.route = routeKey;

  if (typeof state.currentCleanup === 'function') {
    try {
      state.currentCleanup();
    } catch {
      // ignore
    }
  }
  state.currentCleanup = null;
  state.currentRefresh = null;
  state.shouldPauseRefresh = null;

  document.querySelectorAll('.nav-item').forEach((btn) => {
    btn.classList.toggle('active', btn.dataset.route === routeKey);
  });

  const titleEl = document.getElementById('page-title');
  const subEl = document.getElementById('page-subtitle');
  if (titleEl) titleEl.textContent = def.title;
  if (subEl) subEl.textContent = def.subtitle;

  setMobileSidebarOpen(false);

  const root = document.getElementById('view-root');
  if (!root) return;
  root.innerHTML = '<div class="empty-state"><span class="spinner"></span> 正在加载...</div>';

  const mounted = def.mount(root, createViewContext());
  if (mounted && typeof mounted === 'object') {
    state.currentCleanup = mounted.destroy || null;
    state.currentRefresh = mounted.refresh || null;
    state.shouldPauseRefresh = mounted.isPaused || null;
  }
}

function startAutoRefresh() {
  if (state.refreshTimer) clearInterval(state.refreshTimer);
  state.refreshTimer = setInterval(async () => {
    if (!state.autoRefresh || document.hidden) return;
    await pollTopbarHealth();
    if (typeof state.shouldPauseRefresh === 'function' && state.shouldPauseRefresh()) {
      return;
    }
    if (typeof state.currentRefresh === 'function') {
      try {
        await state.currentRefresh({ silent: true });
      } catch {
        // silent background refresh
      }
    }
  }, 10000);
}

/* ---------- Bootstrap ---------- */

function init() {
  applyTheme(document.documentElement.getAttribute('data-theme') || 'light');

  setAuthPromptHandler((reason) => openTokenModal(reason));
  onTokenChange(() => updateTokenIndicator());
  updateTokenIndicator();

  // Sidebar webhook copy
  const webhookUrlEl = document.getElementById('sidebar-webhook-url');
  const fullWebhookUrl = `${window.location.origin}/webhook-alert`;
  if (webhookUrlEl) webhookUrlEl.textContent = fullWebhookUrl;
  document.getElementById('sidebar-copy-webhook')?.addEventListener('click', async () => {
    const ok = await copyToClipboard(fullWebhookUrl);
    showToast(ok ? '已复制 Webhook 接收地址' : '复制失败', ok ? 'success' : 'error');
  });

  // Nav buttons
  document.querySelectorAll('.nav-item').forEach((btn) => {
    btn.addEventListener('click', () => {
      navigate(btn.dataset.route, { force: true });
    });
  });

  // Mobile sidebar
  document.getElementById('menu-toggle')?.addEventListener('click', () => setMobileSidebarOpen(true));
  document.getElementById('sidebar-close')?.addEventListener('click', () => setMobileSidebarOpen(false));
  document.getElementById('mobile-backdrop')?.addEventListener('click', () => setMobileSidebarOpen(false));

  // Theme toggle
  document.getElementById('btn-theme-toggle')?.addEventListener('click', toggleTheme);

  // Auto refresh toggle
  const refreshToggle = document.getElementById('auto-refresh-toggle');
  if (refreshToggle) {
    state.autoRefresh = refreshToggle.checked;
    refreshToggle.addEventListener('change', () => {
      state.autoRefresh = refreshToggle.checked;
      showToast(state.autoRefresh ? '已开启 10s 自动刷新' : '已暂停自动刷新', 'info', 2000);
    });
  }

  // Drawer close bindings
  document.getElementById('drawer-close')?.addEventListener('click', closeDrawer);
  document.getElementById('drawer-backdrop')?.addEventListener('click', closeDrawer);

  // Confirm modal bindings
  document.getElementById('confirm-close')?.addEventListener('click', () => finishConfirm(false));
  document.getElementById('confirm-cancel')?.addEventListener('click', () => finishConfirm(false));
  document.getElementById('confirm-backdrop')?.addEventListener('click', () => finishConfirm(false));
  document.getElementById('confirm-ok')?.addEventListener('click', () => finishConfirm(true));

  // Token modal bindings
  document.getElementById('btn-token-lock')?.addEventListener('click', () => openTokenModal());
  document.getElementById('token-modal-close')?.addEventListener('click', () => closeTokenModal(false));
  document.getElementById('token-cancel-btn')?.addEventListener('click', () => closeTokenModal(false));
  document.getElementById('token-backdrop')?.addEventListener('click', () => closeTokenModal(false));

  document.getElementById('token-toggle-vis')?.addEventListener('click', () => {
    const input = document.getElementById('token-input');
    if (input) input.type = input.type === 'password' ? 'text' : 'password';
  });

  document.getElementById('token-clear-btn')?.addEventListener('click', () => {
    clearStoredToken();
    const input = document.getElementById('token-input');
    if (input) input.value = '';
    showToast('已清除本地保存的 API Token', 'info');
    closeTokenModal(false);
  });

  document.getElementById('token-form')?.addEventListener('submit', (e) => {
    e.preventDefault();
    const input = document.getElementById('token-input');
    const errEl = document.getElementById('token-error');
    const val = (input?.value || '').trim();
    if (!val) {
      if (errEl) {
        errEl.textContent = '请输入有效的 API 令牌，或点击“清除本地令牌”。';
        errEl.classList.remove('hidden');
      }
      return;
    }
    setStoredToken(val);
    showToast('API 令牌已保存', 'success', 2000);
    closeTokenModal(true);
  });

  // Quick test modal bindings
  document.getElementById('btn-quick-test')?.addEventListener('click', openQuickTestModal);
  document.getElementById('quick-test-close')?.addEventListener('click', closeQuickTestModal);
  document.getElementById('quick-test-cancel')?.addEventListener('click', closeQuickTestModal);
  document.getElementById('quick-test-backdrop')?.addEventListener('click', closeQuickTestModal);
  document.getElementById('quick-test-send')?.addEventListener('click', runQuickTestPush);

  // Escape key closes open overlays
  document.addEventListener('keydown', (e) => {
    if (e.key !== 'Escape') return;
    if (!document.getElementById('token-modal')?.classList.contains('hidden')) {
      closeTokenModal(false);
    } else if (!document.getElementById('confirm-modal')?.classList.contains('hidden')) {
      finishConfirm(false);
    } else if (!document.getElementById('quick-test-modal')?.classList.contains('hidden')) {
      closeQuickTestModal();
    } else if (!document.getElementById('detail-drawer')?.classList.contains('hidden')) {
      closeDrawer();
    }
  });

  window.addEventListener('hashchange', () => {
    const next = parseRouteFromHash();
    if (next !== state.route) {
      mountRoute(next);
    }
  });

  pollTopbarHealth();
  mountRoute(parseRouteFromHash());
  startAutoRefresh();
}

document.addEventListener('DOMContentLoaded', init);
