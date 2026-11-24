import {
  api,
  escapeHtml,
  formatTimestamp,
  severityMeta,
  statusMeta,
  deliveryStateMeta,
  sourceMeta,
  copyToClipboard,
} from '../api.js';
import {
  renderTimelineChart,
  renderSeverityDonut,
  renderChannelHealth,
} from '../charts.js';

export function mountDashboard(root, ctx) {
  let destroyed = false;
  let overviewData = null;

  root.innerHTML = `
    <div id="dash-content" style="display:flex;flex-direction:column;gap:14px;">
      <div class="empty-state"><span class="spinner"></span> 正在加载监控总览数据...</div>
    </div>
  `;

  function serviceStatusInline(svc) {
    if (!svc || !svc.enabled) {
      return '<span class="status-inline is-off"><span class="dot"></span>未启用</span>';
    }
    if (svc.running && svc.healthy) {
      return '<span class="status-inline is-ok"><span class="dot"></span>运行健康</span>';
    }
    if (svc.running && !svc.healthy) {
      return '<span class="status-inline is-warn"><span class="dot"></span>检测异常</span>';
    }
    return '<span class="status-inline is-off"><span class="dot"></span>已停止</span>';
  }

  function render(data) {
    if (destroyed) return;
    overviewData = data;
    ctx.onOverviewUpdated?.(data);

    const container = root.querySelector('#dash-content');
    if (!container) return;

    const stats = data.stats || {};
    const bySeverity = stats.by_severity || {};
    const bySource = stats.by_source || {};
    const byChannel = stats.by_channel || {};
    const services = data.services || {};
    const traffic = services.traffic_alert || {};
    const kafka = services.kafka_monitor || {};
    const recent = Array.isArray(data.recent_alerts) ? data.recent_alerts : [];

    const total = Number(stats.total) || 0;
    const firing = Number(stats.firing) || 0;
    const resolved = Number(stats.resolved) || 0;
    const filtered = Number(stats.filtered) || 0;
    const sent = Number(stats.sent) || 0;
    const partial = Number(stats.partial) || 0;
    const failed = Number(stats.failed) || 0;
    const successRate = Number(stats.success_rate || 0).toFixed(1);
    const filterPct = total > 0 ? ((filtered / total) * 100).toFixed(1) : '0.0';
    const webhookUrl = `${window.location.origin}/webhook-alert`;

    const onboardingHtml = !data.setup_complete
      ? `
        <div class="banner banner-warning">
          <div class="banner-content">
            <span class="banner-icon" aria-hidden="true">
              <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><line x1="12" y1="8" x2="12" y2="12"/><line x1="12" y1="16" x2="12.01" y2="16"/></svg>
            </span>
            <div>
              <div class="banner-title">尚未完成通知渠道配置</div>
              <div class="banner-desc">当前尚未启用有效的企业微信、钉钉或飞书机器人 Webhook，系统将记录收到的告警但暂不会外发通知。</div>
            </div>
          </div>
          <div class="banner-actions">
            <button id="dash-go-config" class="btn btn-primary btn-sm" type="button">前往配置中心 →</button>
            <button id="dash-go-sim" class="btn btn-secondary btn-sm" type="button">前往调试沙箱体验</button>
          </div>
        </div>
      `
      : '';

    const recentRowsHtml = recent.length
      ? recent
          .map((rec, idx) => {
            const sev = severityMeta(rec.severity, rec.severity_code);
            const st = statusMeta(rec.status);
            const del = deliveryStateMeta(rec.delivery_state);
            const src = sourceMeta(rec.source);
            return `
              <tr class="is-clickable" data-alert-idx="${idx}">
                <td class="mono text-sm" style="white-space:nowrap;">${escapeHtml(formatTimestamp(rec.timestamp))}</td>
                <td>
                  <div class="cell-title">${escapeHtml(rec.alert_name || '--')}</div>
                  <div class="cell-sub">${escapeHtml(rec.instance || rec.summary || '--')}</div>
                </td>
                <td><span class="pill ${sev.pillClass}">${escapeHtml(sev.label)}</span></td>
                <td><span class="pill ${src.pillClass}">${escapeHtml(src.label)}</span></td>
                <td><span class="pill ${st.pillClass}">${escapeHtml(st.label)}</span></td>
                <td><span class="pill ${del.pillClass}">${escapeHtml(del.label)}</span></td>
              </tr>
            `;
          })
          .join('')
      : `<tr><td colspan="6"><div class="empty-state">暂无告警记录，可通过「调试沙箱」模拟或等待 Alertmanager 推送。</div></td></tr>`;

    container.innerHTML = `
      ${onboardingHtml}

      <section class="kpi-strip" aria-label="核心指标">
        <div class="kpi-cell">
          <div class="kpi-label">告警总数</div>
          <div class="kpi-value">${total}</div>
          <div class="kpi-sub">
            <span>AM ${Number(bySource.alertmanager) || 0}</span>
            <span>· 流量 ${Number(bySource.traffic) || 0}</span>
            <span>· Kafka ${Number(bySource.kafka) || 0}</span>
            <span>· 测试 ${Number(bySource.test) || 0}</span>
          </div>
        </div>

        <div class="kpi-cell">
          <div class="kpi-label">推送成功率</div>
          <div class="kpi-value">${successRate}%</div>
          <div class="kpi-sub">
            <span class="text-success">成功 ${sent}</span>
            <span>· 部分 ${partial}</span>
            <span class="${failed > 0 ? 'text-danger' : ''}">· 失败 ${failed}</span>
          </div>
        </div>

        <div class="kpi-cell">
          <div class="kpi-label">当前告警中 / 已恢复</div>
          <div class="kpi-value">
            <span class="${firing > 0 ? 'text-danger' : ''}">${firing}</span>
            <span class="text-muted" style="font-size:18px;font-weight:500;"> / ${resolved}</span>
          </div>
          <div class="kpi-sub">
            <span>告警触发与恢复事件统计</span>
          </div>
        </div>

        <div class="kpi-cell">
          <div class="kpi-label">规则已拦截</div>
          <div class="kpi-value">${filtered}</div>
          <div class="kpi-sub">
            <span>拦截占比 ${filterPct}%</span>
            <span>· 依据 Filter 策略</span>
          </div>
        </div>
      </section>

      <section class="charts-grid" aria-label="趋势与分布图表">
        <div class="card">
          <div class="card-header">
            <h2 class="card-title">24 小时告警趋势</h2>
            <div class="chart-legend">
              <span class="legend-item"><span class="legend-dot" style="background:#c6362f"></span>告警中</span>
              <span class="legend-item"><span class="legend-dot" style="background:#0e7a56"></span>已恢复</span>
              <span class="legend-item"><span class="legend-dot" style="background:#9ca3af"></span>规则拦截</span>
            </div>
          </div>
          <div class="card-body">
            <div id="dash-timeline-chart"></div>
          </div>
        </div>

        <div class="card">
          <div class="card-header">
            <h2 class="card-title">告警级别分布</h2>
          </div>
          <div class="card-body">
            <div id="dash-severity-donut"></div>
          </div>
        </div>

        <div class="card">
          <div class="card-header">
            <h2 class="card-title">渠道投递健康度</h2>
          </div>
          <div class="card-body">
            <div id="dash-channel-health"></div>
          </div>
        </div>
      </section>

      <section class="grid-3" aria-label="服务运行状态">
        <div class="card">
          <div class="card-header">
            <h2 class="card-title">Webhook 接收器</h2>
            <span class="status-inline is-ok"><span class="dot"></span>监听中</span>
          </div>
          <div class="card-body">
            <div class="text-muted text-xs" style="margin-bottom:6px;">Alertmanager 推送目标 URL</div>
            <div class="copy-box">
              <code title="${escapeHtml(webhookUrl)}">${escapeHtml(webhookUrl)}</code>
              <button id="dash-copy-webhook" class="btn btn-secondary btn-xs" type="button">复制</button>
            </div>
            <div class="kv-list">
              <div class="kv-row">
                <span class="kv-label">监听端口</span>
                <span class="kv-value mono" title="${escapeHtml(data.server_port || '--')}">${escapeHtml(data.server_port || '--')}</span>
              </div>
              <div class="kv-row">
                <span class="kv-label">API 鉴权</span>
                <span class="kv-value">${data.auth_required ? '已开启令牌' : '未开启'}</span>
              </div>
              <div class="kv-row">
                <span class="kv-label">配置文件</span>
                <span class="kv-value mono" title="${escapeHtml(data.config_path || './config.yaml')}">${escapeHtml(data.config_path || './config.yaml')}</span>
              </div>
            </div>
          </div>
        </div>

        <div class="card">
          <div class="card-header">
            <h2 class="card-title">ClickHouse 大流量监控</h2>
            ${serviceStatusInline(traffic)}
          </div>
          <div class="card-body">
            <div class="kv-list" style="margin-top:0;">
              <div class="kv-row">
                <span class="kv-label">最近异常数</span>
                <span class="kv-value mono">${escapeHtml(traffic.anomalies_last ?? 0)}</span>
              </div>
              <div class="kv-row">
                <span class="kv-label">累计告警次数</span>
                <span class="kv-value mono">${escapeHtml(traffic.total_alerts ?? 0)}</span>
              </div>
              <div class="kv-row">
                <span class="kv-label">最近检查</span>
                <span class="kv-value mono">${escapeHtml(formatTimestamp(traffic.last_check))}</span>
              </div>
            </div>
            ${traffic.last_error ? `<div class="text-xs text-danger mt-2 mono" style="word-break:break-word;">${escapeHtml(traffic.last_error)}</div>` : ''}
            <div class="service-card-footer">
              <span class="text-xs text-muted">${traffic.enabled ? '定时巡检已开启' : '未启用巡检'}</span>
              <button id="dash-check-traffic" class="btn btn-secondary btn-xs" type="button" ${!traffic.enabled ? 'disabled title="请先在配置中心启用大流量监控"' : ''}>立即检测</button>
            </div>
          </div>
        </div>

        <div class="card">
          <div class="card-header">
            <h2 class="card-title">Kafka 消费积压监控</h2>
            ${serviceStatusInline(kafka)}
          </div>
          <div class="card-body">
            <div class="kv-list" style="margin-top:0;">
              <div class="kv-row">
                <span class="kv-label">积压 / 总消费组</span>
                <span class="kv-value mono">${escapeHtml(kafka.lagged_last ?? 0)} / ${escapeHtml(kafka.total_groups ?? 0)}</span>
              </div>
              <div class="kv-row">
                <span class="kv-label">累计告警次数</span>
                <span class="kv-value mono">${escapeHtml(kafka.total_alerts ?? 0)}</span>
              </div>
              <div class="kv-row">
                <span class="kv-label">最近检查</span>
                <span class="kv-value mono">${escapeHtml(formatTimestamp(kafka.last_check))}</span>
              </div>
            </div>
            ${kafka.last_error ? `<div class="text-xs text-danger mt-2 mono" style="word-break:break-word;">${escapeHtml(kafka.last_error)}</div>` : ''}
            <div class="service-card-footer">
              <span class="text-xs text-muted">${kafka.enabled ? '定时巡检已开启' : '未启用巡检'}</span>
              <button id="dash-check-kafka" class="btn btn-secondary btn-xs" type="button" ${!kafka.enabled ? 'disabled title="请先在配置中心启用 Kafka 监控"' : ''}>立即检测</button>
            </div>
          </div>
        </div>
      </section>

      <section class="card" aria-label="最近告警">
        <div class="card-header">
          <h2 class="card-title">最近告警记录</h2>
          <button id="dash-all-alerts" class="btn btn-ghost btn-sm" type="button">查看全部记录 →</button>
        </div>
        <div class="card-body no-pad">
          <div class="table-wrap">
            <table class="data-table">
              <thead>
                <tr>
                  <th>时间</th>
                  <th>告警名称 / 实例</th>
                  <th>级别</th>
                  <th>来源</th>
                  <th>状态</th>
                  <th>投递结果</th>
                </tr>
              </thead>
              <tbody>${recentRowsHtml}</tbody>
            </table>
          </div>
        </div>
      </section>
    `;

    renderTimelineChart(container.querySelector('#dash-timeline-chart'), stats.timeline || []);
    renderSeverityDonut(container.querySelector('#dash-severity-donut'), bySeverity);
    renderChannelHealth(container.querySelector('#dash-channel-health'), byChannel);

    container.querySelector('#dash-go-config')?.addEventListener('click', () => ctx.navigate('config'));
    container.querySelector('#dash-go-sim')?.addEventListener('click', () => ctx.navigate('simulator'));
    container.querySelector('#dash-all-alerts')?.addEventListener('click', () => ctx.navigate('alerts'));

    container.querySelector('#dash-copy-webhook')?.addEventListener('click', async () => {
      const ok = await copyToClipboard(webhookUrl);
      ctx.toast(ok ? '已复制 Webhook 接收地址' : '复制失败', ok ? 'success' : 'error');
    });

    container.querySelector('#dash-check-traffic')?.addEventListener('click', async (e) => {
      const btn = e.currentTarget;
      btn.disabled = true;
      btn.innerHTML = '<span class="spinner"></span> 检测中...';
      try {
        const res = await api.triggerTrafficCheck();
        ctx.toast(res.ok ? 'ClickHouse 大流量检测已完成' : (res.error || '检测未通过'), res.ok ? 'success' : 'warning');
        await load();
      } catch (err) {
        ctx.toast(err.message, 'error');
        btn.disabled = false;
        btn.textContent = '立即检测';
      }
    });

    container.querySelector('#dash-check-kafka')?.addEventListener('click', async (e) => {
      const btn = e.currentTarget;
      btn.disabled = true;
      btn.innerHTML = '<span class="spinner"></span> 检测中...';
      try {
        const res = await api.triggerKafkaCheck();
        ctx.toast(res.ok ? 'Kafka 消费积压检测已完成' : (res.error || '检测未通过'), res.ok ? 'success' : 'warning');
        await load();
      } catch (err) {
        ctx.toast(err.message, 'error');
        btn.disabled = false;
        btn.textContent = '立即检测';
      }
    });

    container.querySelectorAll('tr[data-alert-idx]').forEach((row) => {
      row.addEventListener('click', () => {
        const idx = Number(row.dataset.alertIdx);
        const rec = recent[idx];
        if (rec) ctx.openAlertDetail(rec);
      });
    });
  }

  async function load({ silent = false } = {}) {
    try {
      const data = await api.getOverview();
      if (!destroyed && data) {
        render(data);
      }
    } catch (err) {
      if (destroyed) return;
      if (!silent || !overviewData) {
        const container = root.querySelector('#dash-content');
        if (container) {
          container.innerHTML = `
            <div class="banner banner-danger">
              <div class="banner-content">
                <div>
                  <div class="banner-title">加载监控总览失败</div>
                  <div class="banner-desc">${escapeHtml(err.message)}</div>
                </div>
              </div>
              <button id="dash-retry" class="btn btn-secondary btn-sm" type="button">重试</button>
            </div>
          `;
          container.querySelector('#dash-retry')?.addEventListener('click', () => load());
        }
      }
    }
  }

  load();

  return {
    refresh: load,
    destroy: () => {
      destroyed = true;
    },
  };
}
