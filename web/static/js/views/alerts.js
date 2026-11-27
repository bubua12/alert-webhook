import {
  api,
  escapeHtml,
  formatTimestamp,
  severityMeta,
  statusMeta,
  deliveryStateMeta,
  sourceMeta,
  clientMeta,
  debounce,
} from '../api.js';

export function mountAlerts(root, ctx) {
  let destroyed = false;
  let reqSeq = 0;
  let activeController = null;

  const query = {
    page: 1,
    page_size: 20,
    status: '',
    delivery_state: '',
    source: '',
    severity: '',
    q: '',
  };

  let currentItems = [];
  let total = 0;

  root.innerHTML = `
    <div style="display:flex;flex-direction:column;gap:16px;">
      <div class="card">
        <div class="card-body">
          <div class="filter-bar">
            <input id="alerts-q" class="form-control search-input" type="search" placeholder="搜索告警名称、实例、摘要或标签...">
            <select id="alerts-status" class="form-control" style="width:auto;min-width:120px;">
              <option value="">全部状态</option>
              <option value="firing">告警中 (firing)</option>
              <option value="resolved">已恢复 (resolved)</option>
            </select>
            <select id="alerts-delivery" class="form-control" style="width:auto;min-width:136px;">
              <option value="">全部投递状态</option>
              <option value="sent">已发送 (sent)</option>
              <option value="partial">部分成功 (partial)</option>
              <option value="failed">发送失败 (failed)</option>
              <option value="filtered">规则拦截 (filtered)</option>
              <option value="dry_run">沙箱演练 (dry_run)</option>
            </select>
            <select id="alerts-source" class="form-control" style="width:auto;min-width:130px;">
              <option value="">全部来源</option>
              <option value="alertmanager">Alertmanager</option>
              <option value="traffic">流量监控</option>
              <option value="kafka">Kafka 积压</option>
              <option value="test">沙箱测试</option>
            </select>
            <select id="alerts-severity" class="form-control" style="width:auto;min-width:124px;">
              <option value="">全部级别</option>
              <option value="emergency">紧急 (P0)</option>
              <option value="critical">严重 (P1)</option>
              <option value="warning">警告 (P2)</option>
              <option value="info">信息 (P3)</option>
            </select>
            <button id="alerts-reset" class="btn btn-ghost btn-sm" type="button">重置</button>
          </div>

          <div style="display:flex;align-items:center;justify-content:space-between;gap:10px;flex-wrap:wrap;margin-top:12px;">
            <div id="alerts-summary" class="text-sm text-muted">加载中...</div>
            <div style="display:flex;align-items:center;gap:8px;flex-wrap:wrap;">
              <button id="alerts-refresh" class="btn btn-secondary btn-sm" type="button">刷新</button>
              <button id="alerts-export" class="btn btn-secondary btn-sm" type="button">导出 JSON</button>
              <button id="alerts-clear" class="btn btn-danger btn-sm" type="button">清空记录</button>
            </div>
          </div>
        </div>
      </div>

      <div class="card">
        <div class="card-body no-pad">
          <div class="table-wrap">
            <table class="data-table">
              <thead>
                <tr>
                  <th>时间</th>
                  <th>告警名称 / 摘要</th>
                  <th>级别</th>
                  <th>来源</th>
                  <th>状态</th>
                  <th>投递状态</th>
                  <th>渠道回执</th>
                  <th style="text-align:right;">操作</th>
                </tr>
              </thead>
              <tbody id="alerts-tbody">
                <tr><td colspan="8"><div class="empty-state"><span class="spinner"></span> 正在加载告警记录...</div></td></tr>
              </tbody>
            </table>
          </div>
          <div id="alerts-pagination" class="pagination"></div>
        </div>
      </div>
    </div>
  `;

  const qInput = root.querySelector('#alerts-q');
  const statusSelect = root.querySelector('#alerts-status');
  const deliverySelect = root.querySelector('#alerts-delivery');
  const sourceSelect = root.querySelector('#alerts-source');
  const severitySelect = root.querySelector('#alerts-severity');
  const tbody = root.querySelector('#alerts-tbody');
  const paginationEl = root.querySelector('#alerts-pagination');
  const summaryEl = root.querySelector('#alerts-summary');

  function renderDeliveryPills(deliveries) {
    if (!Array.isArray(deliveries) || deliveries.length === 0) {
      return '<span class="text-muted text-xs">--</span>';
    }
    const pillsHtml = deliveries
      .map((d) => {
        const cm = clientMeta(d.client);
        const cls = d.success ? 'pill-success' : 'pill-emergency';
        const target = String(d.target || '').trim();
        const label = target ? `${cm.short}·${target}` : cm.short;
        const titleTarget = target ? `${cm.name} (${target})` : cm.name;
        const title = `${titleTarget} · ${d.error || `${d.duration_ms ?? 0}ms`}`;
        return `<span class="pill ${cls}" title="${escapeHtml(title)}">${escapeHtml(label)} ${d.success ? '✓' : '✕'}</span>`;
      })
      .join('');
    return `<div class="delivery-pills-wrap">${pillsHtml}</div>`;
  }

  function renderTable() {
    if (destroyed || !tbody || !paginationEl || !summaryEl) return;

    summaryEl.textContent = `共 ${total} 条记录 · 第 ${query.page} 页`;

    if (currentItems.length === 0) {
      tbody.innerHTML = '<tr><td colspan="8"><div class="empty-state">没有符合筛选条件的告警记录</div></td></tr>';
    } else {
      tbody.innerHTML = currentItems
        .map((rec, idx) => {
          const sev = severityMeta(rec.severity, rec.severity_code);
          const st = statusMeta(rec.status);
          const del = deliveryStateMeta(rec.delivery_state);
          const src = sourceMeta(rec.source);
          return `
            <tr class="is-clickable" data-idx="${idx}">
              <td class="mono text-sm" style="white-space:nowrap;">${escapeHtml(formatTimestamp(rec.timestamp))}</td>
              <td>
                <div class="cell-title">${escapeHtml(rec.alert_name || '--')}</div>
                <div class="cell-sub">${escapeHtml(rec.instance ? `${rec.instance} · ` : '')}${escapeHtml(rec.summary || rec.description || '--')}</div>
              </td>
              <td><span class="pill ${sev.pillClass}">${escapeHtml(sev.label)}</span></td>
              <td><span class="pill ${src.pillClass}">${escapeHtml(src.label)}</span></td>
              <td><span class="pill ${st.pillClass}">${escapeHtml(st.label)}</span></td>
              <td><span class="pill ${del.pillClass}">${escapeHtml(del.label)}</span></td>
              <td>${renderDeliveryPills(rec.deliveries)}</td>
              <td style="text-align:right;white-space:nowrap;">
                <button class="btn btn-ghost btn-xs" type="button" data-view-idx="${idx}">详情</button>
              </td>
            </tr>
          `;
        })
        .join('');
    }

    tbody.querySelectorAll('tr[data-idx]').forEach((row) => {
      row.addEventListener('click', () => {
        const rec = currentItems[Number(row.dataset.idx)];
        if (rec) ctx.openAlertDetail(rec);
      });
    });

    const totalPages = Math.max(1, Math.ceil(total / query.page_size));
    paginationEl.innerHTML = `
      <div style="display:flex;align-items:center;gap:8px;">
        <span class="text-sm text-muted">每页</span>
        <select id="alerts-page-size" class="form-control" style="width:auto;padding:4px 8px;">
          <option value="10" ${query.page_size === 10 ? 'selected' : ''}>10</option>
          <option value="20" ${query.page_size === 20 ? 'selected' : ''}>20</option>
          <option value="50" ${query.page_size === 50 ? 'selected' : ''}>50</option>
        </select>
        <span class="text-sm text-muted">条 · 共 ${totalPages} 页</span>
      </div>
      <div class="pagination-pages">
        <button id="page-prev" class="btn btn-secondary btn-sm" type="button" ${query.page <= 1 ? 'disabled' : ''}>上一页</button>
        <span class="mono text-sm" style="padding:0 6px;">${query.page} / ${totalPages}</span>
        <button id="page-next" class="btn btn-secondary btn-sm" type="button" ${query.page >= totalPages ? 'disabled' : ''}>下一页</button>
      </div>
    `;

    paginationEl.querySelector('#alerts-page-size')?.addEventListener('change', (e) => {
      query.page_size = Number(e.target.value) || 20;
      query.page = 1;
      load();
    });
    paginationEl.querySelector('#page-prev')?.addEventListener('click', () => {
      if (query.page > 1) {
        query.page -= 1;
        load();
      }
    });
    paginationEl.querySelector('#page-next')?.addEventListener('click', () => {
      if (query.page < totalPages) {
        query.page += 1;
        load();
      }
    });
  }

  async function load({ silent = false } = {}) {
    const thisReq = ++reqSeq;
    activeController?.abort();
    const controller = new AbortController();
    activeController = controller;
    try {
      const res = await api.getAlerts(query, { signal: controller.signal });
      if (destroyed || thisReq !== reqSeq) return;
      currentItems = Array.isArray(res?.items) ? res.items : [];
      total = Number(res?.total) || 0;
      renderTable();
    } catch (err) {
      if (err?.aborted || destroyed || thisReq !== reqSeq) return;
      if (!silent && tbody) {
        tbody.innerHTML = `<tr><td colspan="8"><div class="empty-state text-danger">加载失败: ${escapeHtml(err.message)}</div></td></tr>`;
      }
    } finally {
      if (activeController === controller) {
        activeController = null;
      }
    }
  }

  const debouncedSearch = debounce(() => {
    query.q = (qInput?.value || '').trim();
    query.page = 1;
    load();
  }, 280);

  qInput?.addEventListener('input', debouncedSearch);
  qInput?.addEventListener('keydown', (e) => {
    if (e.key === 'Enter') {
      query.q = (qInput.value || '').trim();
      query.page = 1;
      load();
    }
  });

  [
    [statusSelect, 'status'],
    [deliverySelect, 'delivery_state'],
    [sourceSelect, 'source'],
    [severitySelect, 'severity'],
  ].forEach(([el, key]) => {
    el?.addEventListener('change', () => {
      query[key] = el.value;
      query.page = 1;
      load();
    });
  });

  root.querySelector('#alerts-reset')?.addEventListener('click', () => {
    query.page = 1;
    query.status = '';
    query.delivery_state = '';
    query.source = '';
    query.severity = '';
    query.q = '';
    if (qInput) qInput.value = '';
    if (statusSelect) statusSelect.value = '';
    if (deliverySelect) deliverySelect.value = '';
    if (sourceSelect) sourceSelect.value = '';
    if (severitySelect) severitySelect.value = '';
    load();
  });

  root.querySelector('#alerts-refresh')?.addEventListener('click', () => load());

  root.querySelector('#alerts-export')?.addEventListener('click', () => {
    if (currentItems.length === 0) {
      ctx.toast('当前列表没有可导出的告警记录', 'warning');
      return;
    }
    const blob = new Blob([JSON.stringify(currentItems, null, 2)], { type: 'application/json;charset=utf-8' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    const stamp = new Date().toISOString().replace(/[:.]/g, '-').slice(0, 19);
    a.href = url;
    a.download = `alerts-export-${stamp}.json`;
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    URL.revokeObjectURL(url);
    ctx.toast(`已导出当前页 ${currentItems.length} 条告警记录`, 'success');
  });

  root.querySelector('#alerts-clear')?.addEventListener('click', async () => {
    const ok = await ctx.confirm({
      title: '清空所有告警记录',
      message: '确定要清空全部历史告警记录（包括持久化文件中的记录）吗？此操作不可撤销。',
      confirmText: '确认清空',
      danger: true,
    });
    if (!ok) return;
    const clearBtn = root.querySelector('#alerts-clear');
    if (clearBtn) {
      clearBtn.disabled = true;
      clearBtn.textContent = '清空中...';
    }
    try {
      await api.clearAlerts();
      ctx.toast('已清空全部告警记录', 'success');
      query.page = 1;
      await load();
      api.getOverview().then((ov) => ctx.onOverviewUpdated?.(ov)).catch(() => {});
    } catch (err) {
      ctx.toast(`清空失败: ${err.message}`, 'error');
    } finally {
      if (!destroyed && clearBtn) {
        clearBtn.disabled = false;
        clearBtn.textContent = '清空记录';
      }
    }
  });

  load();

  return {
    refresh: load,
    destroy: () => {
      destroyed = true;
      activeController?.abort();
    },
  };
}
