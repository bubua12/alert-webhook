import {
  api,
  escapeHtml,
  clientMeta,
  copyToClipboard,
  formatBytes,
} from '../api.js';

const BYTE_UNITS = [
  { label: 'B', mult: 1 },
  { label: 'KB', mult: 1024 },
  { label: 'MB', mult: 1024 * 1024 },
  { label: 'GB', mult: 1024 * 1024 * 1024 },
];

const SEVERITY_PRESETS = ['emergency', 'critical', 'warning', 'info', 'none'];

function detectBestUnit(bytes) {
  const n = Number(bytes) || 0;
  if (n <= 0) return 1024 * 1024;
  if (n % (1024 * 1024 * 1024) === 0) return 1024 * 1024 * 1024;
  if (n % (1024 * 1024) === 0) return 1024 * 1024;
  if (n % 1024 === 0) return 1024;
  return 1;
}

function deepClone(obj) {
  return JSON.parse(JSON.stringify(obj));
}

function parseRowKey(raw) {
  const str = String(raw || '');
  const sep = str.lastIndexOf(':');
  if (sep <= 0) return null;
  const key = str.slice(0, sep);
  const idx = Number(str.slice(sep + 1));
  if (!Number.isInteger(idx) || idx < 0) return null;
  return { key, idx };
}

function shiftIndexedState(stateMap, key, removeIdx) {
  const prefix = `${key}:`;
  const entries = [];
  Object.keys(stateMap).forEach((k) => {
    if (!k.startsWith(prefix)) return;
    const idx = Number(k.slice(prefix.length));
    const val = stateMap[k];
    delete stateMap[k];
    if (Number.isInteger(idx) && idx >= 0 && idx !== removeIdx) {
      entries.push([idx > removeIdx ? idx - 1 : idx, val]);
    }
  });
  entries.forEach(([newIdx, val]) => {
    stateMap[`${prefix}${newIdx}`] = val;
  });
}

function normalizeWebhookEntry(w) {
  if (typeof w === 'string') {
    return {
      name: '',
      url: w,
      enabled: true,
    };
  }
  return {
    name: String(w?.name || ''),
    url: String(w?.url || ''),
    enabled: w?.enabled !== false,
  };
}

function normalizeNotifier(rawNotifier) {
  if (Array.isArray(rawNotifier?.webhooks) && rawNotifier.webhooks.length > 0) {
    return {
      webhooks: rawNotifier.webhooks.map(normalizeWebhookEntry),
    };
  }
  const legacyUrl = String(rawNotifier?.webhook_url || '').trim();
  if (legacyUrl) {
    return {
      webhooks: [{ name: '', url: legacyUrl, enabled: true }],
    };
  }
  return { webhooks: [] };
}

function normalizeConfig(raw = {}) {
  const c = raw || {};
  const notifiers = c.notifiers && typeof c.notifiers === 'object' ? c.notifiers : {};
  const normNotifiers = {};
  ['wechat', 'dingtalk', 'feishu'].forEach((k) => {
    normNotifiers[k] = normalizeNotifier(notifiers[k]);
  });
  Object.keys(notifiers).forEach((k) => {
    if (!normNotifiers[k]) {
      normNotifiers[k] = normalizeNotifier(notifiers[k]);
    }
  });

  const server = c.server || {};
  const filter = c.filter || {};
  const ch = c.clickhouse || {};
  const ta = c.traffic_alert || {};
  const km = c.kafka_monitor || {};

  return {
    client: Array.isArray(c.client) ? c.client.slice() : [],
    notifiers: normNotifiers,
    server: {
      port: String(server.port || '0.0.0.0:18082'),
      ui_enabled: server.ui_enabled !== false,
      auth_token: String(server.auth_token || ''),
      data_dir: String(server.data_dir || './data'),
      log_dir: String(server.log_dir || './logs'),
    },
    filter: {
      alert_name: {
        include: Array.isArray(filter.alert_name?.include) ? filter.alert_name.include.slice() : [],
        exclude: Array.isArray(filter.alert_name?.exclude) ? filter.alert_name.exclude.slice() : [],
      },
      severity: {
        include: Array.isArray(filter.severity?.include) ? filter.severity.include.slice() : [],
        exclude: Array.isArray(filter.severity?.exclude) ? filter.severity.exclude.slice() : [],
      },
    },
    clickhouse: {
      host: String(ch.host || 'localhost'),
      port: Number.isFinite(Number(ch.port)) && Number(ch.port) > 0 ? Number(ch.port) : 9000,
      database: String(ch.database || 'nginxlogs'),
      username: String(ch.username || 'default'),
      password: String(ch.password || ''),
    },
    traffic_alert: {
      enabled: Boolean(ta.enabled),
      check_interval: Number.isFinite(Number(ta.check_interval)) ? Number(ta.check_interval) : 300,
      request_size_threshold: Number.isFinite(Number(ta.request_size_threshold)) ? Number(ta.request_size_threshold) : 1048576,
      response_size_threshold: Number.isFinite(Number(ta.response_size_threshold)) ? Number(ta.response_size_threshold) : 5242880,
      time_window: Number.isFinite(Number(ta.time_window)) ? Number(ta.time_window) : 10,
      count_threshold: Number.isFinite(Number(ta.count_threshold)) ? Number(ta.count_threshold) : 5,
    },
    kafka_monitor: {
      enabled: Boolean(km.enabled),
      kafka_ui_url: String(km.kafka_ui_url || 'http://localhost:8090'),
      cluster_name: String(km.cluster_name || 'local-kafka'),
      check_interval: Number.isFinite(Number(km.check_interval)) ? Number(km.check_interval) : 60,
      consumer_lag_threshold: Number.isFinite(Number(km.consumer_lag_threshold)) ? Number(km.consumer_lag_threshold) : 100000,
      monitored_groups: Array.isArray(km.monitored_groups) ? km.monitored_groups.slice() : [],
      ignored_groups: Array.isArray(km.ignored_groups) ? km.ignored_groups.slice() : [],
    },
  };
}

export function mountConfig(root, ctx) {
  let destroyed = false;
  let activeTab = 'channels';
  let savedConfig = null;
  let draftConfig = null;
  let configPath = '';
  let rawYaml = '';
  let draftYaml = '';
  let warnings = [];
  let envOverrides = [];
  let saveError = '';
  let saveIssues = [];
  let restartRequired = false;

  const revealSecrets = {
    clickhouse_password: false,
    auth_token: false,
  };

  const notifierTestState = {};
  let chTestState = null;
  let kafkaTestState = null;
  let filterTestState = null;
  let filterTestInput = { alert_name: 'HighCPUUsage', severity: 'critical' };

  const byteUnits = {
    request_size_threshold: 1024 * 1024,
    response_size_threshold: 1024 * 1024,
  };

  function isDirty() {
    if (!savedConfig || !draftConfig) return false;
    return JSON.stringify(savedConfig) !== JSON.stringify(draftConfig);
  }

  function renderChipEditor({ id, items, placeholder, isExclude = false, presets = [] }) {
    const chipsHtml = items
      .map(
        (val, idx) => `
        <span class="tag-chip ${isExclude ? 'is-exclude' : ''}">
          <span>${escapeHtml(val)}</span>
          <button type="button" class="tag-chip-remove" data-chip-remove="${escapeHtml(id)}" data-idx="${idx}" aria-label="移除 ${escapeHtml(val)}">×</button>
        </span>
      `
      )
      .join('');

    const presetsHtml = presets.length
      ? `
        <div class="preset-chips">
          <span class="text-xs text-muted">快捷选择:</span>
          ${presets
            .map((p) => {
              const selected = items.includes(p);
              return `<button type="button" class="preset-chip-btn ${selected ? 'is-selected' : ''}" data-chip-preset="${escapeHtml(id)}" data-val="${escapeHtml(p)}">${selected ? '✓ ' : '+ '}${escapeHtml(p)}</button>`;
            })
            .join('')}
        </div>
      `
      : '';

    return `
      <div>
        <div class="chip-editor" data-chip-box="${escapeHtml(id)}">
          ${chipsHtml}
          <input class="chip-input" data-chip-input="${escapeHtml(id)}" type="text" placeholder="${escapeHtml(placeholder)}">
        </div>
        <div class="form-hint mt-2">输入内容后按 Enter 或英文逗号添加</div>
        ${presetsHtml}
      </div>
    `;
  }

  function renderBanners() {
    const blocks = [];

    if (restartRequired) {
      blocks.push(`
        <div class="banner banner-warning">
          <div class="banner-content">
            <div>
              <div class="banner-title">服务端口配置已修改，需重启进程后生效</div>
              <div class="banner-desc">其余通知渠道、过滤规则及后台监控参数已通过热加载即时生效；监听端口变更 (${escapeHtml(draftConfig?.server?.port || '')}) 将在下次重启服务时绑定。</div>
            </div>
          </div>
          <button id="cfg-dismiss-restart" class="btn btn-ghost btn-xs" type="button">知道了</button>
        </div>
      `);
    }

    if (envOverrides.length > 0) {
      blocks.push(`
        <div class="banner banner-info">
          <div class="banner-content">
            <div>
              <div class="banner-title">检测到环境变量覆盖 (${envOverrides.length})</div>
              <div class="banner-desc">以下环境变量正在覆盖配置文件对应字段：<span class="mono">${envOverrides.map((e) => escapeHtml(e)).join(', ')}</span></div>
            </div>
          </div>
        </div>
      `);
    }

    if (saveError || saveIssues.length > 0) {
      const issuesHtml = saveIssues.length
        ? `<ul style="margin:6px 0 0 18px;padding:0;">${saveIssues.map((it) => `<li><code class="mono">${escapeHtml(it.field)}</code>: ${escapeHtml(it.message)}</li>`).join('')}</ul>`
        : '';
      blocks.push(`
        <div class="banner banner-danger">
          <div class="banner-content">
            <div>
              <div class="banner-title">配置校验未通过：${escapeHtml(saveError || '请检查输入字段')}</div>
              <div class="banner-desc">${issuesHtml}</div>
            </div>
          </div>
        </div>
      `);
    }

    if (warnings.length > 0) {
      const warnList = warnings
        .map((w) => {
          const badge = w.level === 'error' ? '<span class="pill pill-emergency">ERROR</span>' : '<span class="pill pill-warning">WARN</span>';
          return `<div style="display:flex;align-items:center;gap:6px;margin-top:4px;">${badge} <code class="mono">${escapeHtml(w.field)}</code> <span>${escapeHtml(w.message)}</span></div>`;
        })
        .join('');
      blocks.push(`
        <div class="banner banner-warning">
          <div class="banner-content">
            <div>
              <div class="banner-title">配置校验提示 (${warnings.length})</div>
              <div class="banner-desc">${warnList}</div>
            </div>
          </div>
        </div>
      `);
    }

    return blocks.join('');
  }

  function renderWebhookTestBox(testRes) {
    if (!testRes) return '';
    if (testRes.loading) {
      return '<div class="webhook-test-result text-muted text-xs mt-2"><span class="spinner"></span> 正在发送连通性测试...</div>';
    }
    if (testRes.result) {
      const r = testRes.result;
      const target = String(r.target || '').trim();
      return `
        <div class="delivery-card webhook-test-result mt-2">
          <div class="delivery-card-top">
            <span class="${r.success ? 'text-success' : 'text-danger'}" style="font-weight:600;font-size:12px;">
              ${r.success ? '连通性测试成功' : '连通性测试失败'}
            </span>
            <div style="display:flex;align-items:center;gap:6px;flex-wrap:wrap;">
              ${target ? `<span class="pill pill-info">${escapeHtml(target)}</span>` : ''}
              <span class="pill pill-neutral mono">${escapeHtml(r.duration_ms ?? 0)} ms${r.status_code ? ` · HTTP ${escapeHtml(r.status_code)}` : ''}</span>
            </div>
          </div>
          ${r.error ? `<div class="text-danger text-xs mt-2 mono" style="word-break:break-word;">${escapeHtml(r.error)}</div>` : ''}
          ${r.response ? `<pre class="code-block mt-2" style="max-height:84px;">${escapeHtml(r.response)}</pre>` : ''}
        </div>
      `;
    }
    if (testRes.error) {
      return `<div class="webhook-test-result text-danger text-xs mt-2">${escapeHtml(testRes.error)}</div>`;
    }
    return '';
  }

  function renderChannelsTab() {
    const channels = ['wechat', 'dingtalk', 'feishu'];
    const cardsHtml = channels
      .map((key) => {
        const meta = clientMeta(key);
        const enabled = draftConfig.client.includes(key);
        const webhooks = Array.isArray(draftConfig.notifiers[key]?.webhooks)
          ? draftConfig.notifiers[key].webhooks
          : [];
        const totalCount = webhooks.length;
        const enabledCount = webhooks.filter((w) => w.enabled !== false).length;
        const channelTest = notifierTestState[key];
        const isBatchTesting = Boolean(channelTest?.loading);

        let countPill = '<span class="pill pill-neutral">0 个目标</span>';
        if (totalCount > 0) {
          if (enabledCount === 0) {
            countPill = `<span class="pill pill-warning">已启用 0/${totalCount}</span>`;
          } else if (enabledCount < totalCount) {
            countPill = `<span class="pill pill-info">已启用 ${enabledCount}/${totalCount}</span>`;
          } else {
            countPill = `<span class="pill pill-info">${totalCount} 个目标</span>`;
          }
        }

        const listBodyHtml =
          totalCount === 0
            ? `
              <div class="webhook-empty">
                <div class="text-sm text-muted">尚未配置 ${escapeHtml(meta.name)} Webhook 地址，点击右侧按钮添加目标群机器人。</div>
                <button type="button" class="btn btn-secondary btn-sm" data-add-webhook="${escapeHtml(key)}">+ 添加第一个 Webhook</button>
              </div>
            `
            : `
              <div class="webhook-list">
                ${webhooks
                  .map((item, idx) => {
                    const rowKey = `${key}:${idx}`;
                    const itemEnabled = item.enabled !== false;
                    const revealed = Boolean(revealSecrets[rowKey]);
                    const rowTest = notifierTestState[rowKey];
                    const rowBusy = Boolean(rowTest?.loading) || isBatchTesting;
                    return `
                      <div class="webhook-item${itemEnabled ? '' : ' is-disabled'}">
                        <div class="webhook-item-row">
                          <span class="webhook-idx mono">#${idx + 1}</span>
                          <input
                            class="form-control webhook-name-input"
                            type="text"
                            value="${escapeHtml(item.name || '')}"
                            data-webhook-name="${escapeHtml(rowKey)}"
                            placeholder="备注名（可选，如运维主群）"
                            autocomplete="off"
                            aria-label="${escapeHtml(meta.name)} 备注名 #${idx + 1}"
                          >
                          <div class="input-with-action webhook-url-wrap">
                            <input
                              id="notifier-url-${escapeHtml(key)}-${idx}"
                              class="form-control mono"
                              type="${revealed ? 'text' : 'password'}"
                              value="${escapeHtml(item.url || '')}"
                              data-webhook-url="${escapeHtml(rowKey)}"
                              placeholder="${escapeHtml(meta.placeholder)}"
                              autocomplete="off"
                              aria-label="${escapeHtml(meta.name)} Webhook URL #${idx + 1}"
                            >
                            <button
                              type="button"
                              class="input-action-btn"
                              data-toggle-reveal="${escapeHtml(rowKey)}"
                              title="${revealed ? '隐藏地址' : '显示地址'}"
                              aria-label="显示或隐藏 Webhook URL"
                            >
                              <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"/><circle cx="12" cy="12" r="3"/></svg>
                            </button>
                          </div>
                          <div class="webhook-item-actions">
                            <label class="toggle-switch" title="${itemEnabled ? '停用此目标' : '启用此目标'}">
                              <input type="checkbox" data-webhook-toggle="${escapeHtml(rowKey)}" ${itemEnabled ? 'checked' : ''} ${rowBusy ? 'disabled' : ''} aria-label="启用或停用目标 #${idx + 1}">
                              <span class="toggle-track"></span>
                            </label>
                            <button type="button" class="btn btn-secondary btn-xs" data-test-webhook="${escapeHtml(rowKey)}" ${rowBusy ? 'disabled' : ''}>
                              ${rowTest?.loading ? '测试中...' : '测试'}
                            </button>
                            <button type="button" class="btn btn-ghost btn-xs text-danger" data-remove-webhook="${escapeHtml(rowKey)}" ${rowBusy ? 'disabled' : ''} title="移除此 Webhook">
                              删除
                            </button>
                          </div>
                        </div>
                        ${renderWebhookTestBox(rowTest)}
                      </div>
                    `;
                  })
                  .join('')}
              </div>
            `;

        const channelErrorHtml = channelTest?.error
          ? `<div class="banner banner-danger mt-3"><div class="banner-desc">${escapeHtml(channelTest.error)}</div></div>`
          : '';

        return `
          <div class="card channel-card">
            <div class="card-header">
              <div class="card-title">
                <span>${escapeHtml(meta.name)}</span>
                <span class="pill pill-neutral mono">${escapeHtml(key)}</span>
                ${countPill}
              </div>
              <div class="channel-header-actions">
                <button type="button" class="btn btn-secondary btn-sm" data-add-webhook="${escapeHtml(key)}">+ 添加 Webhook</button>
                <button type="button" class="btn btn-secondary btn-sm" data-test-notifier="${escapeHtml(key)}" ${enabledCount === 0 || isBatchTesting ? 'disabled' : ''}>
                  ${isBatchTesting ? '<span class="spinner"></span> 测试中...' : '测试全部'}
                </button>
                <label class="toggle-switch" title="启用或停用该通知渠道">
                  <input type="checkbox" data-client-toggle="${escapeHtml(key)}" ${enabled ? 'checked' : ''}>
                  <span class="toggle-track"></span>
                  <span class="text-xs text-muted">${enabled ? '已启用' : '未启用'}</span>
                </label>
              </div>
            </div>
            <div class="card-body channel-card-body">
              ${listBodyHtml}
              ${channelErrorHtml}
              <div class="text-xs text-muted mt-3" style="line-height:1.55;">
                <strong>接入指引：</strong>${escapeHtml(meta.guide)}
              </div>
            </div>
          </div>
        `;
      })
      .join('');

    return `
      <div class="channel-stack">${cardsHtml}</div>
    `;
  }

  function renderFilterTab() {
    const f = draftConfig.filter;
    let testResultHtml = '';
    if (filterTestState?.loading) {
      testResultHtml = '<span class="text-muted text-sm"><span class="spinner"></span> 检测中...</span>';
    } else if (filterTestState?.decision) {
      const d = filterTestState.decision;
      testResultHtml = `
        <div class="banner ${d.allowed ? 'banner-success' : 'banner-warning'} mt-3">
          <div class="banner-content">
            <div>
              <div class="banner-title">${d.allowed ? '允许转发 (Allowed)' : '被过滤规则拦截 (Blocked)'}</div>
              <div class="banner-desc">
                原因：${escapeHtml(d.reason || '--')}
                ${d.rule ? ` · 命中规则：<code class="mono">${escapeHtml(d.rule)}</code>` : ''}
              </div>
            </div>
          </div>
        </div>
      `;
    } else if (filterTestState?.error) {
      testResultHtml = `<div class="text-danger text-sm mt-2">${escapeHtml(filterTestState.error)}</div>`;
    }

    return `
      <div style="display:flex;flex-direction:column;gap:16px;">
        <div class="banner banner-info">
          <div class="banner-content">
            <div>
              <div class="banner-title">过滤规则匹配优先级说明</div>
              <div class="banner-desc">支持通配符 <code class="mono">*</code>（如 <code class="mono">HighCPU*</code>）；<strong>exclude 优先级高于 include</strong>；告警名称和告警级别规则需同时通过才会发送。</div>
            </div>
          </div>
        </div>

        <div class="grid-2">
          <div class="card">
            <div class="card-header">
              <h3 class="card-title">按告警名称过滤 (alert_name)</h3>
            </div>
            <div class="card-body" style="display:flex;flex-direction:column;gap:16px;">
              <div class="form-group">
                <label class="form-label">包含规则 (include) — 留空表示允许全部</label>
                ${renderChipEditor({
                  id: 'filter.alert_name.include',
                  items: f.alert_name.include,
                  placeholder: '例如 HighCPU*, *Memory*',
                })}
              </div>
              <div class="form-group">
                <label class="form-label">排除规则 (exclude) — 优先拦截</label>
                ${renderChipEditor({
                  id: 'filter.alert_name.exclude',
                  items: f.alert_name.exclude,
                  placeholder: '例如 *Test*, DebugAlert',
                  isExclude: true,
                })}
              </div>
            </div>
          </div>

          <div class="card">
            <div class="card-header">
              <h3 class="card-title">按告警级别过滤 (severity)</h3>
            </div>
            <div class="card-body" style="display:flex;flex-direction:column;gap:16px;">
              <div class="form-group">
                <label class="form-label">包含级别 (include) — 留空表示不限制</label>
                ${renderChipEditor({
                  id: 'filter.severity.include',
                  items: f.severity.include,
                  placeholder: '输入级别如 critical',
                  presets: SEVERITY_PRESETS,
                })}
              </div>
              <div class="form-group">
                <label class="form-label">排除级别 (exclude) — 优先拦截</label>
                ${renderChipEditor({
                  id: 'filter.severity.exclude',
                  items: f.severity.exclude,
                  placeholder: '输入级别如 info, none',
                  isExclude: true,
                  presets: SEVERITY_PRESETS,
                })}
              </div>
            </div>
          </div>
        </div>

        <div class="card">
          <div class="card-header">
            <h3 class="card-title">实时规则匹配测试器 (Live Filter Tester)</h3>
            <span class="text-xs text-muted">直接基于当前未保存的编辑草稿进行实时判定</span>
          </div>
          <div class="card-body">
            <div class="grid-3" style="align-items:end;">
              <div class="form-group">
                <label class="form-label" for="filter-test-name">测试告警名称 (alertname)</label>
                <input id="filter-test-name" class="form-control mono" type="text" value="${escapeHtml(filterTestInput.alert_name)}" placeholder="HighCPUUsage">
              </div>
              <div class="form-group">
                <label class="form-label" for="filter-test-sev">测试告警级别 (severity)</label>
                <input id="filter-test-sev" class="form-control mono" list="filter-sev-datalist" type="text" value="${escapeHtml(filterTestInput.severity)}" placeholder="critical">
                <datalist id="filter-sev-datalist">
                  <option value="emergency"></option>
                  <option value="critical"></option>
                  <option value="warning"></option>
                  <option value="info"></option>
                  <option value="none"></option>
                </datalist>
              </div>
              <div>
                <button id="btn-test-filter" class="btn btn-primary" type="button">测试匹配结果</button>
              </div>
            </div>
            ${testResultHtml}
          </div>
        </div>
      </div>
    `;
  }

  function renderByteThresholdField(field, label) {
    const bytes = Number(draftConfig.traffic_alert[field]) || 0;
    const mult = byteUnits[field] || 1048576;
    const displayVal = Number((bytes / mult).toFixed(4));
    const unitOptions = BYTE_UNITS.map(
      (u) => `<option value="${u.mult}" ${mult === u.mult ? 'selected' : ''}>${u.label}</option>`
    ).join('');

    return `
      <div class="form-group">
        <label class="form-label" for="ta-${escapeHtml(field)}">${escapeHtml(label)}</label>
        <div class="input-group">
          <input id="ta-${escapeHtml(field)}" class="form-control mono" type="number" min="0" step="any" value="${displayVal}" data-byte-val="${escapeHtml(field)}">
          <select class="form-control unit-select mono" data-byte-unit="${escapeHtml(field)}">${unitOptions}</select>
        </div>
        <div class="form-hint mono">精确值: ${bytes.toLocaleString()} B (${escapeHtml(formatBytes(bytes))})</div>
      </div>
    `;
  }

  function renderTrafficTab() {
    const ch = draftConfig.clickhouse;
    const ta = draftConfig.traffic_alert;
    const revealed = Boolean(revealSecrets.clickhouse_password);

    let chTestHtml = '';
    if (chTestState?.loading) {
      chTestHtml = '<div class="text-muted text-sm mt-3"><span class="spinner"></span> 正在连接 ClickHouse 执行检测...</div>';
    } else if (chTestState?.result) {
      const r = chTestState.result;
      chTestHtml = `
        <div class="banner ${r.ok ? 'banner-success' : 'banner-danger'} mt-3">
          <div class="banner-content">
            <div>
              <div class="banner-title">${r.ok ? 'ClickHouse 连接与查询正常' : 'ClickHouse 连接失败'}</div>
              <div class="banner-desc">
                延迟: <code class="mono">${escapeHtml(r.latency_ms ?? 0)} ms</code>
                ${r.ok ? ` · 近 1 分钟访问日志行数: <code class="mono">${escapeHtml(r.recent_rows ?? 0)}</code>` : ''}
                ${r.message ? ` · ${escapeHtml(r.message)}` : ''}
                ${r.error ? `<div class="text-danger mono mt-2">${escapeHtml(r.error)}</div>` : ''}
              </div>
            </div>
          </div>
        </div>
      `;
    } else if (chTestState?.error) {
      chTestHtml = `<div class="banner banner-danger mt-3"><div class="banner-desc">${escapeHtml(chTestState.error)}</div></div>`;
    }

    return `
      <div class="grid-2">
        <div class="card">
          <div class="card-header">
            <h3 class="card-title">ClickHouse 数据源连接</h3>
            <button id="btn-test-clickhouse" class="btn btn-secondary btn-sm" type="button" ${chTestState?.loading ? 'disabled' : ''}>测试 ClickHouse 连接</button>
          </div>
          <div class="card-body" style="display:flex;flex-direction:column;gap:14px;">
            <div class="grid-2">
              <div class="form-group">
                <label class="form-label" for="ch-host">Host</label>
                <input id="ch-host" class="form-control mono" type="text" value="${escapeHtml(ch.host)}" data-path="clickhouse.host">
              </div>
              <div class="form-group">
                <label class="form-label" for="ch-port">Port (Native TCP)</label>
                <input id="ch-port" class="form-control mono" type="number" min="1" max="65535" value="${escapeHtml(ch.port)}" data-path="clickhouse.port" data-type="int">
              </div>
            </div>
            <div class="grid-2">
              <div class="form-group">
                <label class="form-label" for="ch-db">Database</label>
                <input id="ch-db" class="form-control mono" type="text" value="${escapeHtml(ch.database)}" data-path="clickhouse.database">
              </div>
              <div class="form-group">
                <label class="form-label" for="ch-user">Username</label>
                <input id="ch-user" class="form-control mono" type="text" value="${escapeHtml(ch.username)}" data-path="clickhouse.username">
              </div>
            </div>
            <div class="form-group">
              <label class="form-label" for="ch-pass">Password</label>
              <div class="input-with-action">
                <input id="ch-pass" class="form-control mono" type="${revealed ? 'text' : 'password'}" value="${escapeHtml(ch.password)}" data-path="clickhouse.password" autocomplete="off">
                <button type="button" class="input-action-btn" data-toggle-reveal="clickhouse_password" aria-label="显示或隐藏密码">
                  <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"/><circle cx="12" cy="12" r="3"/></svg>
                </button>
              </div>
            </div>
            ${chTestHtml}
          </div>
        </div>

        <div class="card">
          <div class="card-header">
            <h3 class="card-title">Nginx 大流量告警策略</h3>
            <label class="toggle-switch">
              <input type="checkbox" data-path="traffic_alert.enabled" data-type="bool" ${ta.enabled ? 'checked' : ''}>
              <span class="toggle-track"></span>
              <span class="text-xs text-muted">${ta.enabled ? '已启用' : '未启用'}</span>
            </label>
          </div>
          <div class="card-body" style="display:flex;flex-direction:column;gap:14px;">
            ${renderByteThresholdField('request_size_threshold', '单次请求大小阈值 (request_size_threshold)')}
            ${renderByteThresholdField('response_size_threshold', '单次响应大小阈值 (response_size_threshold)')}
            <div class="grid-3">
              <div class="form-group">
                <label class="form-label" for="ta-interval">检查间隔 (秒)</label>
                <input id="ta-interval" class="form-control mono" type="number" min="5" value="${escapeHtml(ta.check_interval)}" data-path="traffic_alert.check_interval" data-type="int">
              </div>
              <div class="form-group">
                <label class="form-label" for="ta-window">时间窗口 (分钟)</label>
                <input id="ta-window" class="form-control mono" type="number" min="1" value="${escapeHtml(ta.time_window)}" data-path="traffic_alert.time_window" data-type="int">
              </div>
              <div class="form-group">
                <label class="form-label" for="ta-count">触发次数阈值</label>
                <input id="ta-count" class="form-control mono" type="number" min="1" value="${escapeHtml(ta.count_threshold)}" data-path="traffic_alert.count_threshold" data-type="int">
              </div>
            </div>
          </div>
        </div>
      </div>
    `;
  }

  function renderKafkaTab() {
    const km = draftConfig.kafka_monitor;
    let kafkaTestHtml = '';
    if (kafkaTestState?.loading) {
      kafkaTestHtml = '<div class="text-muted text-sm mt-3"><span class="spinner"></span> 正在查询 Kafka UI 消费组状态...</div>';
    } else if (kafkaTestState?.result) {
      const r = kafkaTestState.result;
      const groups = Array.isArray(r.groups) ? r.groups : [];
      const rowsHtml = groups.length
        ? groups
            .map((g) => {
              const isMonitored = km.monitored_groups.includes(g.groupId);
              const isIgnored = km.ignored_groups.includes(g.groupId);
              const lagNum = Number(g.consumerLag) || 0;
              const overThreshold = lagNum >= (Number(km.consumer_lag_threshold) || 0);
              return `
                <tr>
                  <td class="mono">${escapeHtml(g.groupId)}</td>
                  <td class="mono">${escapeHtml(g.members ?? 0)}</td>
                  <td class="mono">${escapeHtml(g.topics ?? 0)}</td>
                  <td><span class="pill pill-neutral mono">${escapeHtml(g.state || '--')}</span></td>
                  <td class="mono ${overThreshold ? 'text-danger' : ''}" style="font-weight:600;">${lagNum.toLocaleString()}</td>
                  <td style="text-align:right;white-space:nowrap;">
                    <button type="button" class="btn btn-xs ${isMonitored ? 'btn-primary' : 'btn-secondary'}" data-kafka-add="monitored" data-group="${escapeHtml(g.groupId)}">
                      ${isMonitored ? '✓ 已监控' : '+ 监控'}
                    </button>
                    <button type="button" class="btn btn-xs ${isIgnored ? 'btn-danger' : 'btn-ghost'}" data-kafka-add="ignored" data-group="${escapeHtml(g.groupId)}">
                      ${isIgnored ? '✓ 已忽略' : '+ 忽略'}
                    </button>
                  </td>
                </tr>
              `;
            })
            .join('')
        : '<tr><td colspan="6"><div class="empty-state">未返回消费组列表</div></td></tr>';

      kafkaTestHtml = `
        <div class="mt-4">
          <div class="banner ${r.ok ? 'banner-success' : 'banner-danger'} mb-3">
            <div class="banner-content">
              <div>
                <div class="banner-title">${r.ok ? `Kafka UI 连接成功 · 发现 ${r.group_count ?? groups.length} 个消费组` : 'Kafka UI 连接失败'}</div>
                <div class="banner-desc">
                  延迟: <code class="mono">${escapeHtml(r.latency_ms ?? 0)} ms</code>
                  ${r.error ? `<div class="text-danger mono mt-2">${escapeHtml(r.error)}</div>` : ''}
                </div>
              </div>
            </div>
          </div>
          ${r.ok ? `
            <div class="card">
              <div class="card-body no-pad">
                <div class="table-wrap" style="max-height:280px;">
                  <table class="data-table">
                    <thead>
                      <tr>
                        <th>Consumer Group</th>
                        <th>Members</th>
                        <th>Topics</th>
                        <th>State</th>
                        <th>Lag</th>
                        <th style="text-align:right;">快捷加入规则</th>
                      </tr>
                    </thead>
                    <tbody>${rowsHtml}</tbody>
                  </table>
                </div>
              </div>
            </div>
          ` : ''}
        </div>
      `;
    } else if (kafkaTestState?.error) {
      kafkaTestHtml = `<div class="banner banner-danger mt-3"><div class="banner-desc">${escapeHtml(kafkaTestState.error)}</div></div>`;
    }

    return `
      <div style="display:flex;flex-direction:column;gap:16px;">
        <div class="card">
          <div class="card-header">
            <h3 class="card-title">Kafka UI 消费积压监控设置</h3>
            <div style="display:flex;align-items:center;gap:12px;">
              <button id="btn-test-kafka" class="btn btn-secondary btn-sm" type="button" ${kafkaTestState?.loading ? 'disabled' : ''}>测试 Kafka UI 连接</button>
              <label class="toggle-switch">
                <input type="checkbox" data-path="kafka_monitor.enabled" data-type="bool" ${km.enabled ? 'checked' : ''}>
                <span class="toggle-track"></span>
                <span class="text-xs text-muted">${km.enabled ? '已启用' : '未启用'}</span>
              </label>
            </div>
          </div>
          <div class="card-body">
            <div class="grid-4">
              <div class="form-group">
                <label class="form-label" for="km-url">Kafka UI URL</label>
                <input id="km-url" class="form-control mono" type="text" value="${escapeHtml(km.kafka_ui_url)}" data-path="kafka_monitor.kafka_ui_url" placeholder="http://localhost:8090">
              </div>
              <div class="form-group">
                <label class="form-label" for="km-cluster">集群名称 (cluster_name)</label>
                <input id="km-cluster" class="form-control mono" type="text" value="${escapeHtml(km.cluster_name)}" data-path="kafka_monitor.cluster_name" placeholder="local-kafka">
              </div>
              <div class="form-group">
                <label class="form-label" for="km-lag">积压阈值 (consumer_lag_threshold)</label>
                <input id="km-lag" class="form-control mono" type="number" min="1" value="${escapeHtml(km.consumer_lag_threshold)}" data-path="kafka_monitor.consumer_lag_threshold" data-type="int">
              </div>
              <div class="form-group">
                <label class="form-label" for="km-interval">检查间隔 (秒)</label>
                <input id="km-interval" class="form-control mono" type="number" min="5" value="${escapeHtml(km.check_interval)}" data-path="kafka_monitor.check_interval" data-type="int">
              </div>
            </div>
            ${kafkaTestHtml}
          </div>
        </div>

        <div class="grid-2">
          <div class="card">
            <div class="card-header">
              <h3 class="card-title">指定监控消费组 (monitored_groups)</h3>
            </div>
            <div class="card-body">
              <div class="form-hint mb-3" style="margin-bottom:8px;">留空表示监控集群内所有未忽略的消费组。</div>
              ${renderChipEditor({
                id: 'kafka_monitor.monitored_groups',
                items: km.monitored_groups,
                placeholder: '输入消费组 ID',
              })}
            </div>
          </div>

          <div class="card">
            <div class="card-header">
              <h3 class="card-title">忽略消费组 (ignored_groups)</h3>
            </div>
            <div class="card-body">
              <div class="form-hint mb-3" style="margin-bottom:8px;">匹配以下消费组 ID 的积压将不产生告警。</div>
              ${renderChipEditor({
                id: 'kafka_monitor.ignored_groups',
                items: km.ignored_groups,
                placeholder: '输入需忽略的消费组 ID',
                isExclude: true,
              })}
            </div>
          </div>
        </div>
      </div>
    `;
  }

  function renderServerYamlTab() {
    const sv = draftConfig.server;
    const revealed = Boolean(revealSecrets.auth_token);
    const webhookTarget = `${window.location.origin}/webhook-alert`;
    const amSnippet = [
      'route:',
      '  group_by: ["alertname", "cluster"]',
      '  group_wait: 15s',
      '  group_interval: 2m',
      '  repeat_interval: 1h',
      '  receiver: "alert-webhook"',
      '',
      'receivers:',
      '  - name: "alert-webhook"',
      '    webhook_configs:',
      `      - url: "${webhookTarget}"`,
      '        send_resolved: true',
    ].join('\n');

    return `
      <div style="display:flex;flex-direction:column;gap:16px;">
        <div class="grid-2">
          <div class="card">
            <div class="card-header">
              <h3 class="card-title">服务端运行参数</h3>
              <span class="mono text-xs text-muted">${escapeHtml(configPath || './config.yaml')}</span>
            </div>
            <div class="card-body" style="display:flex;flex-direction:column;gap:14px;">
              <div class="form-group">
                <label class="form-label" for="sv-port">监听地址与端口 (server.port)</label>
                <input id="sv-port" class="form-control mono" type="text" value="${escapeHtml(sv.port)}" data-path="server.port" placeholder="0.0.0.0:18082">
                <div class="form-hint">提示：修改监听端口需重启服务进程后才会生效。</div>
              </div>

              <div class="form-group">
                <label class="form-label" for="sv-token">API 访问密钥 (server.auth_token / API_KEY)</label>
                <div class="input-with-action">
                  <input id="sv-token" class="form-control mono" type="${revealed ? 'text' : 'password'}" value="${escapeHtml(sv.auth_token)}" data-path="server.auth_token" placeholder="留空表示不启用登录鉴权" autocomplete="off">
                  <button type="button" class="input-action-btn" data-toggle-reveal="auth_token" aria-label="显示或隐藏密钥">
                    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"/><circle cx="12" cy="12" r="3"/></svg>
                  </button>
                </div>
                <div class="form-hint">对应 <code class="mono">.env</code> 中的 <code class="mono">API_KEY</code>（兼容 <code class="mono">ALERT_WEBHOOK_AUTH_TOKEN</code>）；非空时 Web 控制台将启用独立登录页面鉴权。</div>
              </div>

              <div class="grid-2">
                <div class="form-group">
                  <label class="form-label" for="sv-data-dir">数据目录 (data_dir)</label>
                  <input id="sv-data-dir" class="form-control mono" type="text" value="${escapeHtml(sv.data_dir)}" data-path="server.data_dir">
                </div>
                <div class="form-group">
                  <label class="form-label" for="sv-log-dir">日志目录 (log_dir)</label>
                  <input id="sv-log-dir" class="form-control mono" type="text" value="${escapeHtml(sv.log_dir)}" data-path="server.log_dir">
                </div>
              </div>

              <div style="display:flex;align-items:center;justify-content:space-between;padding-top:4px;">
                <div>
                  <div class="form-label">启用内嵌 Web 控制台 (ui_enabled)</div>
                  <div class="form-hint">关闭后重启服务将仅保留 API 与 Webhook 接收接口。</div>
                </div>
                <label class="toggle-switch">
                  <input type="checkbox" data-path="server.ui_enabled" data-type="bool" ${sv.ui_enabled ? 'checked' : ''}>
                  <span class="toggle-track"></span>
                </label>
              </div>
            </div>
          </div>

          <div class="card">
            <div class="card-header">
              <h3 class="card-title">Alertmanager 集成配置片段</h3>
              <button id="btn-copy-am" class="btn btn-secondary btn-sm" type="button">复制片段</button>
            </div>
            <div class="card-body">
              <div class="text-xs text-muted mb-3" style="margin-bottom:8px;">将以下 receiver 配置添加到 Prometheus Alertmanager 的 <code class="mono">alertmanager.yml</code> 中即可接入：</div>
              <pre class="code-block">${escapeHtml(amSnippet)}</pre>
            </div>
          </div>
        </div>

        <div class="card">
          <div class="card-header">
            <h3 class="card-title">原始 YAML 配置编辑器</h3>
            <div style="display:flex;align-items:center;gap:8px;flex-wrap:wrap;">
              <label class="btn btn-secondary btn-sm" style="cursor:pointer;">
                导入 YAML 文件
                <input id="yaml-file-input" type="file" accept=".yaml,.yml,text/yaml" class="hidden">
              </label>
              <button id="btn-download-yaml" class="btn btn-secondary btn-sm" type="button">下载 config.yaml</button>
              <button id="btn-apply-yaml" class="btn btn-primary btn-sm" type="button">应用 YAML</button>
            </div>
          </div>
          <div class="card-body">
            <textarea id="yaml-editor" class="form-control yaml-textarea" spellcheck="false">${escapeHtml(draftYaml)}</textarea>
            <div class="form-hint mt-2">点击「应用 YAML」将直接解析、校验并覆盖磁盘配置且立即热生效。</div>
          </div>
        </div>
      </div>
    `;
  }

  function setByPath(obj, path, val) {
    const parts = path.split('.');
    let curr = obj;
    for (let i = 0; i < parts.length - 1; i += 1) {
      if (!curr[parts[i]] || typeof curr[parts[i]] !== 'object') {
        curr[parts[i]] = {};
      }
      curr = curr[parts[i]];
    }
    curr[parts[parts.length - 1]] = val;
  }

  function getChipTargetArray(id) {
    switch (id) {
      case 'filter.alert_name.include':
        return draftConfig.filter.alert_name.include;
      case 'filter.alert_name.exclude':
        return draftConfig.filter.alert_name.exclude;
      case 'filter.severity.include':
        return draftConfig.filter.severity.include;
      case 'filter.severity.exclude':
        return draftConfig.filter.severity.exclude;
      case 'kafka_monitor.monitored_groups':
        return draftConfig.kafka_monitor.monitored_groups;
      case 'kafka_monitor.ignored_groups':
        return draftConfig.kafka_monitor.ignored_groups;
      default:
        return null;
    }
  }

  function updateDirtyBar() {
    const bar = root.querySelector('#cfg-save-bar');
    if (!bar) return;
    bar.classList.toggle('hidden', !isDirty());
  }

  function render() {
    if (destroyed || !draftConfig) return;

    const tabs = [
      { id: 'channels', label: '通知渠道' },
      { id: 'filter', label: '过滤规则' },
      { id: 'traffic', label: '流量监控' },
      { id: 'kafka', label: 'Kafka 监控' },
      { id: 'server', label: '服务与 YAML' },
    ];

    let tabBody = '';
    if (activeTab === 'channels') tabBody = renderChannelsTab();
    else if (activeTab === 'filter') tabBody = renderFilterTab();
    else if (activeTab === 'traffic') tabBody = renderTrafficTab();
    else if (activeTab === 'kafka') tabBody = renderKafkaTab();
    else tabBody = renderServerYamlTab();

    const dirty = isDirty();

    root.innerHTML = `
      <div style="display:flex;flex-direction:column;gap:16px;">
        <div class="card">
          <div style="display:flex;align-items:center;justify-content:space-between;gap:12px;padding:4px 16px 0;flex-wrap:wrap;">
            <div class="tabs-bar" role="tablist">
              ${tabs
                .map(
                  (t) => `<button type="button" class="tab-btn ${activeTab === t.id ? 'active' : ''}" data-tab="${t.id}" role="tab" aria-selected="${activeTab === t.id}">${escapeHtml(t.label)}</button>`
                )
                .join('')}
            </div>
            <div style="display:flex;align-items:center;gap:8px;padding:6px 0;">
              <button id="cfg-reload-top" class="btn btn-ghost btn-sm" type="button">从磁盘重载</button>
              <button id="cfg-save-top" class="btn btn-primary btn-sm" type="button" ${dirty ? '' : 'disabled'}>保存并热生效</button>
            </div>
          </div>
        </div>

        ${renderBanners()}

        <div id="cfg-tab-body">${tabBody}</div>

        <div id="cfg-save-bar" class="sticky-save-bar ${dirty ? '' : 'hidden'}">
          <div style="display:flex;align-items:center;gap:8px;">
            <span class="pill pill-warning">未保存修改</span>
            <span class="text-sm text-secondary">配置修改尚未写入磁盘并应用到运行中服务</span>
          </div>
          <div style="display:flex;align-items:center;gap:8px;flex-wrap:wrap;">
            <button id="cfg-reset" class="btn btn-secondary btn-sm" type="button">重置修改</button>
            <button id="cfg-reload-bar" class="btn btn-ghost btn-sm" type="button">从磁盘重载</button>
            <button id="cfg-save-bar-btn" class="btn btn-primary btn-sm" type="button">保存并热生效</button>
          </div>
        </div>
      </div>
    `;

    bindEvents();
  }

  function bindEvents() {
    root.querySelectorAll('[data-tab]').forEach((btn) => {
      btn.addEventListener('click', () => {
        activeTab = btn.dataset.tab;
        render();
      });
    });

    root.querySelector('#cfg-dismiss-restart')?.addEventListener('click', () => {
      restartRequired = false;
      render();
    });

    root.querySelector('#cfg-reset')?.addEventListener('click', () => {
      draftConfig = deepClone(savedConfig);
      draftYaml = rawYaml;
      saveError = '';
      saveIssues = [];
      Object.keys(notifierTestState).forEach((k) => delete notifierTestState[k]);
      render();
      ctx.toast('已还原为当前生效配置', 'info', 2000);
    });

    root.querySelector('#cfg-reload-top')?.addEventListener('click', handleReload);
    root.querySelector('#cfg-reload-bar')?.addEventListener('click', handleReload);
    root.querySelector('#cfg-save-top')?.addEventListener('click', handleSaveConfig);
    root.querySelector('#cfg-save-bar-btn')?.addEventListener('click', handleSaveConfig);

    const syncDirtyControls = () => {
      updateDirtyBar();
      const topSave = root.querySelector('#cfg-save-top');
      if (topSave) topSave.disabled = !isDirty();
    };

    // Notifier channel toggles & multi-webhook rows
    root.querySelectorAll('[data-client-toggle]').forEach((chk) => {
      chk.addEventListener('change', () => {
        const key = chk.dataset.clientToggle;
        const idx = draftConfig.client.indexOf(key);
        if (chk.checked && idx === -1) {
          draftConfig.client.push(key);
        } else if (!chk.checked && idx !== -1) {
          draftConfig.client.splice(idx, 1);
        }
        render();
      });
    });

    root.querySelectorAll('[data-add-webhook]').forEach((btn) => {
      btn.addEventListener('click', () => {
        const key = btn.dataset.addWebhook;
        if (!key) return;
        if (!draftConfig.notifiers[key]) draftConfig.notifiers[key] = { webhooks: [] };
        if (!Array.isArray(draftConfig.notifiers[key].webhooks)) {
          draftConfig.notifiers[key].webhooks = [];
        }
        const nextIdx = draftConfig.notifiers[key].webhooks.length;
        draftConfig.notifiers[key].webhooks.push({ name: '', url: '', enabled: true });
        delete notifierTestState[key];
        render();
        setTimeout(() => {
          root.querySelector(`[data-webhook-url="${key}:${nextIdx}"]`)?.focus();
        }, 0);
      });
    });

    root.querySelectorAll('[data-webhook-name]').forEach((inp) => {
      inp.addEventListener('input', () => {
        const parsed = parseRowKey(inp.dataset.webhookName);
        const item = parsed && draftConfig.notifiers[parsed.key]?.webhooks?.[parsed.idx];
        if (!item) return;
        item.name = inp.value;
        syncDirtyControls();
      });
    });

    root.querySelectorAll('[data-webhook-url]').forEach((inp) => {
      inp.addEventListener('input', () => {
        const parsed = parseRowKey(inp.dataset.webhookUrl);
        const item = parsed && draftConfig.notifiers[parsed.key]?.webhooks?.[parsed.idx];
        if (!item) return;
        item.url = inp.value.trim();
        syncDirtyControls();
      });
    });

    root.querySelectorAll('[data-toggle-reveal]').forEach((btn) => {
      btn.addEventListener('click', () => {
        const key = btn.dataset.toggleReveal;
        revealSecrets[key] = !revealSecrets[key];
        render();
      });
    });

    root.querySelectorAll('[data-webhook-toggle]').forEach((chk) => {
      chk.addEventListener('change', () => {
        const parsed = parseRowKey(chk.dataset.webhookToggle);
        const item = parsed && draftConfig.notifiers[parsed.key]?.webhooks?.[parsed.idx];
        if (!item) return;
        item.enabled = Boolean(chk.checked);
        delete notifierTestState[parsed.key];
        render();
      });
    });

    root.querySelectorAll('[data-remove-webhook]').forEach((btn) => {
      btn.addEventListener('click', () => {
        const parsed = parseRowKey(btn.dataset.removeWebhook);
        const list = parsed && draftConfig.notifiers[parsed.key]?.webhooks;
        if (!Array.isArray(list) || parsed.idx >= list.length) return;
        list.splice(parsed.idx, 1);
        shiftIndexedState(revealSecrets, parsed.key, parsed.idx);
        shiftIndexedState(notifierTestState, parsed.key, parsed.idx);
        delete notifierTestState[parsed.key];
        render();
      });
    });

    root.querySelectorAll('[data-test-webhook]').forEach((btn) => {
      btn.addEventListener('click', async () => {
        const parsed = parseRowKey(btn.dataset.testWebhook);
        if (!parsed) return;
        const { key, idx } = parsed;
        const webhooks = draftConfig.notifiers[key]?.webhooks || [];
        const item = webhooks[idx];
        if (!item) return;
        const rowKey = `${key}:${idx}`;
        const url = String(item.url || '').trim();
        if (!url) {
          notifierTestState[rowKey] = { loading: false, error: '请先填写 Webhook URL' };
          render();
          return;
        }
        const target = String(item.name || '').trim() || (webhooks.length > 1 ? `#${idx + 1}` : '');
        notifierTestState[rowKey] = { loading: true };
        render();
        try {
          const res = await api.testNotifier({
            client: key,
            webhook_url: item.url,
            target,
          });
          const first = Array.isArray(res?.results) && res.results[0];
          notifierTestState[rowKey] = {
            loading: false,
            result: first || { client: key, target, success: Boolean(res?.ok), duration_ms: 0 },
          };
        } catch (err) {
          notifierTestState[rowKey] = { loading: false, error: err.message };
        }
        if (!destroyed) render();
      });
    });

    root.querySelectorAll('[data-test-notifier]').forEach((btn) => {
      btn.addEventListener('click', async () => {
        const key = btn.dataset.testNotifier;
        const meta = clientMeta(key);
        const webhooks = Array.isArray(draftConfig.notifiers[key]?.webhooks)
          ? draftConfig.notifiers[key].webhooks
          : [];
        const enabledEntries = webhooks
          .map((item, idx) => ({ item, idx }))
          .filter(({ item }) => item.enabled !== false);

        if (enabledEntries.length === 0) {
          ctx.toast(`${meta.name} 没有已启用的 Webhook 目标`, 'warning');
          return;
        }

        notifierTestState[key] = { loading: true };
        enabledEntries.forEach(({ idx }) => {
          notifierTestState[`${key}:${idx}`] = { loading: true };
        });
        render();

        try {
          const payloadWebhooks = webhooks.map((w) => ({
            name: String(w?.name || '').trim(),
            url: String(w?.url || '').trim(),
            enabled: w?.enabled !== false,
          }));
          const res = await api.testNotifier({ client: key, webhooks: payloadWebhooks });
          const results = Array.isArray(res?.results) ? res.results : [];

          notifierTestState[key] = { loading: false };
          enabledEntries.forEach(({ idx }) => {
            delete notifierTestState[`${key}:${idx}`];
          });

          if (results.length === 0) {
            const msg = '未返回测试结果，请确认已启用目标填写了有效的 Webhook URL';
            notifierTestState[key] = { loading: false, error: msg };
            ctx.toast(`${meta.name}：${msg}`, 'warning');
          } else {
            if (results.length === enabledEntries.length) {
              results.forEach((r, i) => {
                const targetIdx = enabledEntries[i].idx;
                notifierTestState[`${key}:${targetIdx}`] = { loading: false, result: r };
              });
            } else {
              const unmatched = enabledEntries.slice();
              results.forEach((r) => {
                const rTarget = String(r.target || '').trim();
                let matchPos = -1;
                if (rTarget) {
                  matchPos = unmatched.findIndex((entry) => {
                    const entryName = String(entry.item.name || '').trim();
                    return (
                      entryName === rTarget ||
                      (`#${entry.idx + 1}` === rTarget && Boolean(String(entry.item.url || '').trim()))
                    );
                  });
                }
                if (matchPos === -1) {
                  matchPos = unmatched.findIndex((entry) => Boolean(String(entry.item.url || '').trim()));
                }
                if (matchPos === -1) {
                  matchPos = unmatched.length > 0 ? 0 : -1;
                }
                if (matchPos !== -1) {
                  const [matched] = unmatched.splice(matchPos, 1);
                  notifierTestState[`${key}:${matched.idx}`] = { loading: false, result: r };
                }
              });
            }

            const okCount = results.filter((r) => r.success).length;
            if (okCount === results.length) {
              ctx.toast(`${meta.name} 全部 ${okCount} 个目标连通性测试成功`, 'success');
            } else if (okCount > 0) {
              ctx.toast(`${meta.name} 测试完成：${okCount}/${results.length} 个目标成功`, 'warning');
            } else {
              ctx.toast(`${meta.name} 全部 ${results.length} 个目标测试失败`, 'error');
            }
          }
        } catch (err) {
          enabledEntries.forEach(({ idx }) => {
            const rowKey = `${key}:${idx}`;
            if (notifierTestState[rowKey]?.loading) {
              delete notifierTestState[rowKey];
            }
          });
          notifierTestState[key] = { loading: false, error: err.message };
          ctx.toast(`${meta.name} 测试失败: ${err.message}`, 'error');
        }
        if (!destroyed) render();
      });
    });

    // Generic bound fields via data-path
    root.querySelectorAll('[data-path]').forEach((el) => {
      const evt = el.type === 'checkbox' ? 'change' : 'input';
      el.addEventListener(evt, () => {
        const path = el.dataset.path;
        const dtype = el.dataset.type || 'string';
        let val = el.value;
        if (dtype === 'bool') val = Boolean(el.checked);
        else if (dtype === 'int') val = parseInt(el.value, 10) || 0;
        setByPath(draftConfig, path, val);
        if (dtype === 'bool') {
          render();
        } else {
          updateDirtyBar();
          const topSave = root.querySelector('#cfg-save-top');
          if (topSave) topSave.disabled = !isDirty();
        }
      });
    });

    // Byte unit threshold fields
    root.querySelectorAll('[data-byte-val]').forEach((inp) => {
      inp.addEventListener('input', () => {
        const field = inp.dataset.byteVal;
        const num = Number(inp.value);
        const mult = byteUnits[field] || 1;
        const bytes = Number.isFinite(num) && num >= 0 ? Math.round(num * mult) : 0;
        draftConfig.traffic_alert[field] = bytes;
        const hint = inp.closest('.form-group')?.querySelector('.form-hint');
        if (hint) {
          hint.textContent = `精确值: ${bytes.toLocaleString()} B (${formatBytes(bytes)})`;
        }
        updateDirtyBar();
        const topSave = root.querySelector('#cfg-save-top');
        if (topSave) topSave.disabled = !isDirty();
      });
    });

    root.querySelectorAll('[data-byte-unit]').forEach((sel) => {
      sel.addEventListener('change', () => {
        const field = sel.dataset.byteUnit;
        const mult = Number(sel.value) || 1;
        byteUnits[field] = mult;
        const inp = root.querySelector(`[data-byte-val="${field}"]`);
        const num = Number(inp?.value);
        if (Number.isFinite(num) && num >= 0) {
          draftConfig.traffic_alert[field] = Math.round(num * mult);
        }
        render();
      });
    });

    // Chip Editors
    root.querySelectorAll('[data-chip-remove]').forEach((btn) => {
      btn.addEventListener('click', () => {
        const arr = getChipTargetArray(btn.dataset.chipRemove);
        const idx = Number(btn.dataset.idx);
        if (arr && idx >= 0 && idx < arr.length) {
          arr.splice(idx, 1);
          render();
        }
      });
    });

    root.querySelectorAll('[data-chip-preset]').forEach((btn) => {
      btn.addEventListener('click', () => {
        const arr = getChipTargetArray(btn.dataset.chipPreset);
        const val = btn.dataset.val;
        if (!arr || !val) return;
        const idx = arr.indexOf(val);
        if (idx === -1) arr.push(val);
        else arr.splice(idx, 1);
        render();
      });
    });

    root.querySelectorAll('[data-chip-input]').forEach((inp) => {
      const id = inp.dataset.chipInput;
      const isExclude = id.endsWith('.exclude') || id.endsWith('ignored_groups');
      const commitToken = ({ rerenderAll = true } = {}) => {
        const raw = inp.value.trim().replace(/,+$/, '');
        if (!raw) return false;
        const arr = getChipTargetArray(id);
        if (!arr) return false;
        const added = [];
        raw
          .split(',')
          .map((s) => s.trim())
          .filter(Boolean)
          .forEach((part) => {
            if (!arr.includes(part)) {
              arr.push(part);
              added.push(part);
            }
          });
        inp.value = '';
        if (rerenderAll) {
          render();
          setTimeout(() => {
            root.querySelector(`[data-chip-input="${id}"]`)?.focus();
          }, 0);
        } else if (added.length > 0) {
          const box = inp.closest('[data-chip-box]');
          added.forEach((val) => {
            const idx = arr.indexOf(val);
            const chip = document.createElement('span');
            chip.className = `tag-chip ${isExclude ? 'is-exclude' : ''}`;
            chip.innerHTML = `<span>${escapeHtml(val)}</span><button type="button" class="tag-chip-remove" data-chip-remove="${escapeHtml(id)}" data-idx="${idx}" aria-label="移除 ${escapeHtml(val)}">×</button>`;
            chip.querySelector('.tag-chip-remove')?.addEventListener('click', () => {
              const curIdx = arr.indexOf(val);
              if (curIdx !== -1) {
                arr.splice(curIdx, 1);
                render();
              }
            });
            box?.insertBefore(chip, inp);
          });
          updateDirtyBar();
          const topSave = root.querySelector('#cfg-save-top');
          if (topSave) topSave.disabled = !isDirty();
        }
        return true;
      };

      inp.addEventListener('keydown', (e) => {
        if (e.key === 'Enter' || e.key === ',') {
          e.preventDefault();
          commitToken({ rerenderAll: true });
        } else if (e.key === 'Backspace' && inp.value === '') {
          const arr = getChipTargetArray(id);
          if (arr && arr.length > 0) {
            arr.pop();
            render();
            setTimeout(() => {
              root.querySelector(`[data-chip-input="${id}"]`)?.focus();
            }, 0);
          }
        }
      });

      inp.addEventListener('blur', () => {
        if (inp.value.trim()) commitToken({ rerenderAll: false });
      });
    });

    // Filter live tester
    const fName = root.querySelector('#filter-test-name');
    const fSev = root.querySelector('#filter-test-sev');
    fName?.addEventListener('input', () => {
      filterTestInput.alert_name = fName.value;
    });
    fSev?.addEventListener('input', () => {
      filterTestInput.severity = fSev.value;
    });
    root.querySelector('#btn-test-filter')?.addEventListener('click', async () => {
      filterTestState = { loading: true };
      render();
      try {
        const decision = await api.testFilter({
          alert_name: filterTestInput.alert_name,
          severity: filterTestInput.severity,
          filter: draftConfig.filter,
        });
        filterTestState = { loading: false, decision };
      } catch (err) {
        filterTestState = { loading: false, error: err.message };
      }
      if (!destroyed) render();
    });

    // ClickHouse tester
    root.querySelector('#btn-test-clickhouse')?.addEventListener('click', async () => {
      chTestState = { loading: true };
      render();
      try {
        const result = await api.testClickHouse(draftConfig.clickhouse);
        chTestState = { loading: false, result };
      } catch (err) {
        chTestState = { loading: false, error: err.message };
      }
      if (!destroyed) render();
    });

    // Kafka tester & group quick buttons
    root.querySelector('#btn-test-kafka')?.addEventListener('click', async () => {
      kafkaTestState = { loading: true };
      render();
      try {
        const result = await api.testKafka(draftConfig.kafka_monitor);
        kafkaTestState = { loading: false, result };
      } catch (err) {
        kafkaTestState = { loading: false, error: err.message };
      }
      if (!destroyed) render();
    });

    root.querySelectorAll('[data-kafka-add]').forEach((btn) => {
      btn.addEventListener('click', () => {
        const target = btn.dataset.kafkaAdd;
        const grp = btn.dataset.group;
        if (!grp) return;
        const mon = draftConfig.kafka_monitor.monitored_groups;
        const ign = draftConfig.kafka_monitor.ignored_groups;
        if (target === 'monitored') {
          const iIdx = ign.indexOf(grp);
          if (iIdx !== -1) ign.splice(iIdx, 1);
          const mIdx = mon.indexOf(grp);
          if (mIdx === -1) mon.push(grp);
          else mon.splice(mIdx, 1);
        } else {
          const mIdx = mon.indexOf(grp);
          if (mIdx !== -1) mon.splice(mIdx, 1);
          const iIdx = ign.indexOf(grp);
          if (iIdx === -1) ign.push(grp);
          else ign.splice(iIdx, 1);
        }
        render();
      });
    });

    // Server & YAML tab bindings
    root.querySelector('#btn-copy-am')?.addEventListener('click', async () => {
      const code = root.querySelector('.code-block')?.textContent || '';
      const ok = await copyToClipboard(code);
      ctx.toast(ok ? '已复制 Alertmanager 配置片段' : '复制失败', ok ? 'success' : 'error');
    });

    const yamlEditor = root.querySelector('#yaml-editor');
    yamlEditor?.addEventListener('input', () => {
      draftYaml = yamlEditor.value;
    });

    root.querySelector('#btn-download-yaml')?.addEventListener('click', () => {
      const content = yamlEditor ? yamlEditor.value : draftYaml;
      const blob = new Blob([content], { type: 'text/yaml;charset=utf-8' });
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = 'config.yaml';
      document.body.appendChild(a);
      a.click();
      document.body.removeChild(a);
      URL.revokeObjectURL(url);
      ctx.toast('已导出 config.yaml', 'success');
    });

    root.querySelector('#yaml-file-input')?.addEventListener('change', async (e) => {
      const file = e.target.files?.[0];
      if (!file) return;
      try {
        const text = await file.text();
        draftYaml = text;
        if (yamlEditor) yamlEditor.value = text;
        ctx.toast(`已加载文件 ${file.name}，请核对后点击“应用 YAML”`, 'info');
      } catch (err) {
        ctx.toast(`读取文件失败: ${err.message}`, 'error');
      }
    });

    root.querySelector('#btn-apply-yaml')?.addEventListener('click', async () => {
      const yamlText = yamlEditor ? yamlEditor.value : draftYaml;
      const ok = await ctx.confirm({
        title: '应用原始 YAML 配置',
        message: '将直接校验并保存当前 YAML 到磁盘配置文件并热生效，确定继续吗？',
        confirmText: '确认应用',
      });
      if (!ok) return;
      saveError = '';
      saveIssues = [];
      try {
        const res = await api.saveConfigYaml(yamlText);
        applyServerResponse(res);
        ctx.toast('YAML 配置已保存并热生效', 'success');
        api.getOverview().then((ov) => ctx.onOverviewUpdated?.(ov)).catch(() => {});
      } catch (err) {
        saveError = err.message;
        saveIssues = err.issues || [];
        render();
        ctx.toast(`应用 YAML 失败: ${err.message}`, 'error');
      }
    });
  }

  function applyServerResponse(res) {
    const norm = normalizeConfig(res.config || {});
    savedConfig = deepClone(norm);
    draftConfig = deepClone(norm);
    Object.keys(norm.notifiers).forEach((k) => {
      const len = norm.notifiers[k]?.webhooks?.length || 0;
      const prefix = `${k}:`;
      [notifierTestState, revealSecrets].forEach((mapObj) => {
        Object.keys(mapObj).forEach((rk) => {
          if (!rk.startsWith(prefix)) return;
          const idx = Number(rk.slice(prefix.length));
          if (!Number.isInteger(idx) || idx < 0 || idx >= len) {
            delete mapObj[rk];
          }
        });
      });
    });
    if (res.config_path) configPath = res.config_path;
    if (typeof res.yaml === 'string') {
      rawYaml = res.yaml;
      draftYaml = res.yaml;
    }
    warnings = Array.isArray(res.warnings) ? res.warnings : [];
    if (Array.isArray(res.env_overrides)) envOverrides = res.env_overrides;
    if (typeof res.restart_required === 'boolean') {
      restartRequired = res.restart_required;
    }
    byteUnits.request_size_threshold = detectBestUnit(norm.traffic_alert.request_size_threshold);
    byteUnits.response_size_threshold = detectBestUnit(norm.traffic_alert.response_size_threshold);
    render();
  }

  async function handleSaveConfig() {
    saveError = '';
    saveIssues = [];
    const btns = [root.querySelector('#cfg-save-top'), root.querySelector('#cfg-save-bar-btn')].filter(Boolean);
    btns.forEach((b) => {
      b.disabled = true;
      b.textContent = '保存中...';
    });

    try {
      const res = await api.saveConfig(draftConfig);
      applyServerResponse(res);
      ctx.toast('配置已保存并热生效', 'success');
      api.getOverview().then((ov) => ctx.onOverviewUpdated?.(ov)).catch(() => {});
    } catch (err) {
      saveError = err.message;
      saveIssues = err.issues || [];
      render();
      ctx.toast(`保存失败: ${err.message}`, 'error');
    }
  }

  async function handleReload() {
    if (isDirty()) {
      const ok = await ctx.confirm({
        title: '从磁盘重载配置',
        message: '当前存在未保存的界面修改，从磁盘重载将丢弃这些未保存更改。确定继续吗？',
        confirmText: '确认重载',
        danger: true,
      });
      if (!ok) return;
    }
    saveError = '';
    saveIssues = [];
    try {
      const res = await api.reloadConfig();
      Object.keys(notifierTestState).forEach((k) => delete notifierTestState[k]);
      applyServerResponse(res);
      ctx.toast('已从磁盘重载配置文件并热生效', 'success');
      api.getOverview().then((ov) => ctx.onOverviewUpdated?.(ov)).catch(() => {});
    } catch (err) {
      ctx.toast(`重载失败: ${err.message}`, 'error');
    }
  }

  async function load() {
    try {
      const res = await api.getConfig();
      if (destroyed) return;
      applyServerResponse(res);
    } catch (err) {
      if (destroyed) return;
      root.innerHTML = `
        <div class="banner banner-danger">
          <div class="banner-content">
            <div>
              <div class="banner-title">加载配置中心失败</div>
              <div class="banner-desc">${escapeHtml(err.message)}</div>
            </div>
          </div>
          <button id="cfg-retry" class="btn btn-secondary btn-sm" type="button">重试</button>
        </div>
      `;
      root.querySelector('#cfg-retry')?.addEventListener('click', load);
    }
  }

  load();

  return {
    isPaused: () => true,
    refresh: () => {},
    destroy: () => {
      destroyed = true;
    },
  };
}
