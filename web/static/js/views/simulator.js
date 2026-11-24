import {
  api,
  escapeHtml,
  clientMeta,
  copyToClipboard,
} from '../api.js';

const PRESETS = [
  {
    id: 'p1-cpu',
    label: 'P1 CPU 告警',
    data: {
      status: 'firing',
      alert_name: 'HighCPUUsage',
      severity: 'critical',
      instance: 'prod-web-01:9100',
      summary: 'CPU 使用率持续超过 90%',
      description: '节点 prod-web-01 最近 5 分钟 CPU 平均负载达到 94.6%，请及时排查热点进程。',
      labels: [
        { k: 'cluster', v: 'prod-cn-east' },
        { k: 'service', v: 'api-gateway' },
      ],
    },
  },
  {
    id: 'p2-disk',
    label: 'P2 磁盘预警',
    data: {
      status: 'firing',
      alert_name: 'DiskSpaceWarning',
      severity: 'warning',
      instance: 'prod-db-02:9100',
      summary: '/data 分区可用空间低于 15%',
      description: '当前磁盘使用率 86.2%，预计 48 小时内写满，请清理历史归档或扩容。',
      labels: [
        { k: 'mountpoint', v: '/data' },
        { k: 'fstype', v: 'ext4' },
      ],
    },
  },
  {
    id: 'resolved',
    label: '告警恢复 Resolved',
    data: {
      status: 'resolved',
      alert_name: 'HighCPUUsage',
      severity: 'critical',
      instance: 'prod-web-01:9100',
      summary: 'CPU 使用率已恢复至正常水位',
      description: '节点 prod-web-01 CPU 负载已回落至 32%，告警自动恢复。',
      labels: [
        { k: 'cluster', v: 'prod-cn-east' },
        { k: 'service', v: 'api-gateway' },
      ],
    },
  },
  {
    id: 'filtered-test',
    label: '测试告警(被拦截)',
    data: {
      status: 'firing',
      alert_name: 'TestDebugAlert',
      severity: 'info',
      instance: 'staging-node-01:9100',
      summary: '测试环境信息级别验证告警',
      description: '用于验证默认 filter.severity.exclude (info) 或名称排除规则是否正常拦截。',
      labels: [
        { k: 'env', v: 'staging' },
      ],
    },
  },
];

export function mountSimulator(root, ctx) {
  let destroyed = false;
  let activePreviewTab = 'wechat';
  let result = null;
  let running = false;

  const form = {
    status: 'firing',
    alert_name: 'HighCPUUsage',
    severity: 'critical',
    instance: 'prod-web-01:9100',
    summary: 'CPU 使用率持续超过 90%',
    description: '节点 prod-web-01 最近 5 分钟 CPU 平均负载达到 94.6%，请及时排查热点进程。',
    labels: [
      { k: 'cluster', v: 'prod-cn-east' },
      { k: 'service', v: 'api-gateway' },
    ],
    clients: ['wechat', 'dingtalk', 'feishu'],
  };

  function buildLabelsMap() {
    const map = {};
    form.labels.forEach((row) => {
      const k = row.k.trim();
      if (k) map[k] = row.v;
    });
    return map;
  }

  function renderResultPanel() {
    if (running) {
      return `
        <div class="card card-body">
          <div class="empty-state"><span class="spinner"></span> 正在执行过滤评估与消息渲染...</div>
        </div>
      `;
    }
    if (!result) {
      return `
        <div class="card card-body">
          <div class="empty-state">填写左侧告警负载并点击「预览渲染 (Dry Run)」或「真实发送测试」查看过滤判定、各渠道排版预览及投递回执。</div>
        </div>
      `;
    }

    const f = result.filter || { allowed: true, reason: '' };
    const previews = result.previews || {};
    const deliveries = Array.isArray(result.deliveries) ? result.deliveries : [];

    const previewTabs = [
      { id: 'wechat', label: '企业微信' },
      { id: 'dingtalk', label: '钉钉' },
      { id: 'feishu', label: '飞书' },
      { id: 'payload', label: 'Alertmanager JSON' },
    ];

    let previewText = '';
    if (activePreviewTab === 'payload') {
      previewText = JSON.stringify(previews.payload || {}, null, 2);
    } else {
      previewText = previews[activePreviewTab] || '（该渠道暂无预览输出）';
    }

    const deliveriesHtml = deliveries.length
      ? deliveries
          .map((d) => {
            const cm = clientMeta(d.client);
            return `
              <div class="delivery-card">
                <div class="delivery-card-top">
                  <strong>${escapeHtml(cm.name)}</strong>
                  <div style="display:flex;align-items:center;gap:6px;">
                    ${d.status_code ? `<span class="pill pill-neutral mono">HTTP ${escapeHtml(d.status_code)}</span>` : ''}
                    ${d.batches ? `<span class="pill pill-info mono">${escapeHtml(d.batches)} 批</span>` : ''}
                    <span class="pill pill-neutral mono">${escapeHtml(d.duration_ms ?? 0)} ms</span>
                    ${d.success ? '<span class="pill pill-success">发送成功</span>' : '<span class="pill pill-emergency">发送失败</span>'}
                  </div>
                </div>
                ${d.error ? `<div class="text-danger text-xs mt-2 mono">${escapeHtml(d.error)}</div>` : ''}
                ${d.response ? `<pre class="code-block mt-2" style="max-height:96px;">${escapeHtml(d.response)}</pre>` : ''}
              </div>
            `;
          })
          .join('')
      : '<div class="text-muted text-sm">本次为 Dry Run 演练或未触发外发推送</div>';

    return `
      <div style="display:flex;flex-direction:column;gap:16px;">
        <div class="banner ${f.allowed ? 'banner-success' : 'banner-warning'}">
          <div class="banner-content">
            <div>
              <div class="banner-title">${f.allowed ? '✅ 过滤规则判定：允许发送' : '🚫 过滤规则判定：已拦截'}</div>
              <div class="banner-desc">
                ${escapeHtml(f.reason || '通过当前配置的过滤规则')}
                ${f.rule ? ` · 规则：<code class="mono">${escapeHtml(f.rule)}</code>` : ''}
              </div>
            </div>
          </div>
        </div>

        <div class="card">
          <div style="display:flex;align-items:center;justify-content:space-between;padding:4px 16px 0;gap:8px;flex-wrap:wrap;border-bottom:1px solid var(--border-color);">
            <div class="tabs-bar" style="border-bottom:none;">
              ${previewTabs
                .map(
                  (t) => `<button type="button" class="tab-btn ${activePreviewTab === t.id ? 'active' : ''}" data-preview-tab="${t.id}">${escapeHtml(t.label)}</button>`
                )
                .join('')}
            </div>
            <button id="sim-copy-preview" class="btn btn-secondary btn-xs" type="button">复制当前内容</button>
          </div>
          <div class="card-body">
            <pre id="sim-preview-content" class="code-block" style="max-height:320px;">${escapeHtml(previewText)}</pre>
          </div>
        </div>

        <div class="card">
          <div class="card-header">
            <h3 class="card-title">渠道投递回执 (${deliveries.length})</h3>
            ${result.record ? `<span class="pill pill-info mono">已记录: ${escapeHtml(result.record.id || '')}</span>` : ''}
          </div>
          <div class="card-body">${deliveriesHtml}</div>
        </div>
      </div>
    `;
  }

  function render() {
    if (destroyed) return;

    const presetsHtml = PRESETS.map(
      (p) => `<button type="button" class="preset-chip-btn" data-sim-preset="${escapeHtml(p.id)}">${escapeHtml(p.label)}</button>`
    ).join('');

    const labelsHtml = form.labels
      .map(
        (row, idx) => `
        <div style="display:flex;gap:8px;align-items:center;">
          <input class="form-control mono" type="text" value="${escapeHtml(row.k)}" placeholder="label key" data-label-k="${idx}">
          <input class="form-control mono" type="text" value="${escapeHtml(row.v)}" placeholder="label value" data-label-v="${idx}">
          <button type="button" class="icon-btn sm" data-label-del="${idx}" aria-label="删除标签">×</button>
        </div>
      `
      )
      .join('');

    const clientCheckboxes = ['wechat', 'dingtalk', 'feishu']
      .map((c) => {
        const meta = clientMeta(c);
        const checked = form.clients.includes(c);
        return `
          <label style="display:inline-flex;align-items:center;gap:6px;cursor:pointer;padding:6px 10px;border:1px solid var(--border-color);border-radius:var(--radius-sm);background:var(--bg-subtle);">
            <input type="checkbox" value="${escapeHtml(c)}" data-sim-client="${escapeHtml(c)}" ${checked ? 'checked' : ''}>
            <span class="text-sm">${escapeHtml(meta.name)}</span>
          </label>
        `;
      })
      .join('');

    root.innerHTML = `
      <div class="grid-2" style="align-items:start;">
        <div class="card">
          <div class="card-header">
            <h2 class="card-title">告警负载编辑器</h2>
            <span class="text-xs text-muted">支持 Dry Run 渲染与真实发送测试</span>
          </div>
          <div class="card-body" style="display:flex;flex-direction:column;gap:14px;">
            <div>
              <div class="form-label mb-3" style="margin-bottom:6px;">快捷场景模板</div>
              <div class="preset-chips" style="margin-top:0;">${presetsHtml}</div>
            </div>

            <div class="grid-2">
              <div class="form-group">
                <label class="form-label" for="sim-status">告警状态 (status)</label>
                <select id="sim-status" class="form-control">
                  <option value="firing" ${form.status === 'firing' ? 'selected' : ''}>告警中 (firing)</option>
                  <option value="resolved" ${form.status === 'resolved' ? 'selected' : ''}>已恢复 (resolved)</option>
                </select>
              </div>
              <div class="form-group">
                <label class="form-label" for="sim-severity">严重级别 (severity)</label>
                <select id="sim-severity" class="form-control">
                  <option value="emergency" ${form.severity === 'emergency' ? 'selected' : ''}>emergency (P0)</option>
                  <option value="critical" ${form.severity === 'critical' ? 'selected' : ''}>critical (P1)</option>
                  <option value="warning" ${form.severity === 'warning' ? 'selected' : ''}>warning (P2)</option>
                  <option value="info" ${form.severity === 'info' ? 'selected' : ''}>info (P3)</option>
                  <option value="none" ${form.severity === 'none' ? 'selected' : ''}>none</option>
                </select>
              </div>
            </div>

            <div class="grid-2">
              <div class="form-group">
                <label class="form-label" for="sim-alertname">告警名称 (alertname)</label>
                <input id="sim-alertname" class="form-control mono" type="text" value="${escapeHtml(form.alert_name)}">
              </div>
              <div class="form-group">
                <label class="form-label" for="sim-instance">监控实例 (instance)</label>
                <input id="sim-instance" class="form-control mono" type="text" value="${escapeHtml(form.instance)}">
              </div>
            </div>

            <div class="form-group">
              <label class="form-label" for="sim-summary">告警摘要 (summary)</label>
              <input id="sim-summary" class="form-control" type="text" value="${escapeHtml(form.summary)}">
            </div>

            <div class="form-group">
              <label class="form-label" for="sim-desc">详细描述 (description)</label>
              <textarea id="sim-desc" class="form-control" rows="3">${escapeHtml(form.description)}</textarea>
            </div>

            <div class="form-group">
              <div class="form-label">
                <span>自定义标签 (labels)</span>
                <button id="sim-add-label" class="btn btn-ghost btn-xs" type="button">+ 添加标签</button>
              </div>
              <div style="display:flex;flex-direction:column;gap:8px;">
                ${labelsHtml || '<div class="text-xs text-muted">暂无额外标签</div>'}
              </div>
            </div>

            <div class="form-group">
              <div class="form-label">目标推送渠道 (仅真实发送时生效)</div>
              <div style="display:flex;gap:8px;flex-wrap:wrap;">${clientCheckboxes}</div>
            </div>

            <div style="display:flex;align-items:center;gap:10px;flex-wrap:wrap;padding-top:4px;">
              <button id="sim-dry-run" class="btn btn-secondary" type="button" ${running ? 'disabled' : ''}>预览渲染 (Dry Run)</button>
              <button id="sim-real-send" class="btn btn-primary" type="button" ${running ? 'disabled' : ''}>真实发送测试</button>
            </div>
          </div>
        </div>

        <div id="sim-result-wrap">${renderResultPanel()}</div>
      </div>
    `;

    bindEvents();
  }

  function bindEvents() {
    root.querySelectorAll('[data-sim-preset]').forEach((btn) => {
      btn.addEventListener('click', () => {
        const preset = PRESETS.find((p) => p.id === btn.dataset.simPreset);
        if (!preset) return;
        form.status = preset.data.status;
        form.alert_name = preset.data.alert_name;
        form.severity = preset.data.severity;
        form.instance = preset.data.instance;
        form.summary = preset.data.summary;
        form.description = preset.data.description;
        form.labels = preset.data.labels.map((x) => ({ ...x }));
        render();
        runSimulate(false);
      });
    });

    root.querySelector('#sim-status')?.addEventListener('change', (e) => {
      form.status = e.target.value;
    });
    root.querySelector('#sim-severity')?.addEventListener('change', (e) => {
      form.severity = e.target.value;
    });
    root.querySelector('#sim-alertname')?.addEventListener('input', (e) => {
      form.alert_name = e.target.value;
    });
    root.querySelector('#sim-instance')?.addEventListener('input', (e) => {
      form.instance = e.target.value;
    });
    root.querySelector('#sim-summary')?.addEventListener('input', (e) => {
      form.summary = e.target.value;
    });
    root.querySelector('#sim-desc')?.addEventListener('input', (e) => {
      form.description = e.target.value;
    });

    root.querySelectorAll('[data-label-k]').forEach((inp) => {
      inp.addEventListener('input', () => {
        const idx = Number(inp.dataset.labelK);
        if (form.labels[idx]) form.labels[idx].k = inp.value;
      });
    });
    root.querySelectorAll('[data-label-v]').forEach((inp) => {
      inp.addEventListener('input', () => {
        const idx = Number(inp.dataset.labelV);
        if (form.labels[idx]) form.labels[idx].v = inp.value;
      });
    });
    root.querySelectorAll('[data-label-del]').forEach((btn) => {
      btn.addEventListener('click', () => {
        const idx = Number(btn.dataset.labelDel);
        form.labels.splice(idx, 1);
        render();
      });
    });
    root.querySelector('#sim-add-label')?.addEventListener('click', () => {
      form.labels.push({ k: '', v: '' });
      render();
    });

    root.querySelectorAll('[data-sim-client]').forEach((chk) => {
      chk.addEventListener('change', () => {
        const c = chk.dataset.simClient;
        const idx = form.clients.indexOf(c);
        if (chk.checked && idx === -1) form.clients.push(c);
        else if (!chk.checked && idx !== -1) form.clients.splice(idx, 1);
      });
    });

    root.querySelector('#sim-dry-run')?.addEventListener('click', () => runSimulate(false));
    root.querySelector('#sim-real-send')?.addEventListener('click', async () => {
      const ok = await ctx.confirm({
        title: '确认真实发送测试告警',
        message: '该操作将实际调用所选渠道的机器人 Webhook 发送测试告警，并在告警记录中生成一条 source=test 记录。确定发送吗？',
        confirmText: '确认发送',
      });
      if (ok) runSimulate(true);
    });

    root.querySelectorAll('[data-preview-tab]').forEach((btn) => {
      btn.addEventListener('click', () => {
        activePreviewTab = btn.dataset.previewTab;
        const wrap = root.querySelector('#sim-result-wrap');
        if (wrap) {
          wrap.innerHTML = renderResultPanel();
          bindResultEvents();
        }
      });
    });

    bindResultEvents();
  }

  function bindResultEvents() {
    root.querySelectorAll('[data-preview-tab]').forEach((btn) => {
      btn.addEventListener('click', () => {
        activePreviewTab = btn.dataset.previewTab;
        const wrap = root.querySelector('#sim-result-wrap');
        if (wrap) {
          wrap.innerHTML = renderResultPanel();
          bindResultEvents();
        }
      });
    });

    root.querySelector('#sim-copy-preview')?.addEventListener('click', async () => {
      const content = root.querySelector('#sim-preview-content')?.textContent || '';
      const ok = await copyToClipboard(content);
      ctx.toast(ok ? '已复制渲染内容' : '复制失败', ok ? 'success' : 'error');
    });
  }

  async function runSimulate(send) {
    if (!form.alert_name.trim()) {
      ctx.toast('请填写告警名称 (alertname)', 'warning');
      return;
    }
    running = true;
    render();
    try {
      const res = await api.simulate({
        status: form.status,
        alert_name: form.alert_name.trim(),
        severity: form.severity,
        instance: form.instance.trim(),
        summary: form.summary,
        description: form.description,
        labels: buildLabelsMap(),
        send,
        clients: form.clients.slice(),
      });
      if (destroyed) return;
      result = res;
      if (send) {
        const allOk = Array.isArray(res.deliveries) && res.deliveries.length > 0 && res.deliveries.every((d) => d.success);
        ctx.toast(
          res.filter && !res.filter.allowed
            ? '告警已被过滤规则拦截，未外发'
            : allOk
              ? '模拟告警已真实发送并记录'
              : '模拟执行完成，请查看投递回执',
          allOk ? 'success' : 'info'
        );
      }
    } catch (err) {
      if (!destroyed) ctx.toast(`模拟失败: ${err.message}`, 'error');
    } finally {
      running = false;
      if (!destroyed) render();
    }
  }

  // Pre-sync enabled clients if available
  api
    .getConfig()
    .then((res) => {
      if (destroyed) return;
      const enabled = res?.config?.client;
      if (Array.isArray(enabled) && enabled.length > 0) {
        form.clients = enabled.slice();
        render();
      }
    })
    .catch(() => {});

  render();

  return {
    refresh: () => {},
    destroy: () => {
      destroyed = true;
    },
  };
}
