import { escapeHtml, clientMeta } from './api.js';

/**
 * Render 24h Alert Timeline stacked bar chart (SVG) with interactive hover tooltips.
 */
export function renderTimelineChart(container, timeline = []) {
  if (!container) return;

  const buckets = Array.isArray(timeline) && timeline.length > 0
    ? timeline
    : Array.from({ length: 24 }, (_, idx) => ({
        hour: `${String(idx).padStart(2, '0')}:00`,
        firing: 0,
        resolved: 0,
        filtered: 0,
      }));

  const width = 680;
  const height = 210;
  const padLeft = 36;
  const padRight = 14;
  const padTop = 14;
  const padBottom = 28;
  const plotW = width - padLeft - padRight;
  const plotH = height - padTop - padBottom;

  const totals = buckets.map((b) => (Number(b.firing) || 0) + (Number(b.resolved) || 0) + (Number(b.filtered) || 0));
  const rawMax = Math.max(0, ...totals);
  const yMax = rawMax <= 4 ? 4 : Math.ceil(rawMax / 4) * 4;
  const yTicks = [0, Math.round(yMax / 2), yMax];

  const colStep = plotW / buckets.length;
  const barW = Math.max(6, Math.min(18, colStep * 0.58));

  const gridLinesHtml = yTicks
    .map((tick) => {
      const y = padTop + plotH - (tick / yMax) * plotH;
      return `
        <line x1="${padLeft}" y1="${y.toFixed(1)}" x2="${width - padRight}" y2="${y.toFixed(1)}" class="chart-grid-line" />
        <text x="${padLeft - 6}" y="${(y + 3.5).toFixed(1)}" text-anchor="end" class="chart-axis-text">${tick}</text>
      `;
    })
    .join('');

  const barsHtml = buckets
    .map((b, i) => {
      const firing = Number(b.firing) || 0;
      const resolved = Number(b.resolved) || 0;
      const filtered = Number(b.filtered) || 0;
      const hour = String(b.hour || `${String(i).padStart(2, '0')}:00`);

      const xCol = padLeft + i * colStep;
      const xBar = xCol + (colStep - barW) / 2;

      const hFiring = (firing / yMax) * plotH;
      const hResolved = (resolved / yMax) * plotH;
      const hFiltered = (filtered / yMax) * plotH;

      let cursorY = padTop + plotH;
      const segments = [];

      if (firing > 0) {
        cursorY -= hFiring;
        segments.push(`<rect x="${xBar.toFixed(1)}" y="${cursorY.toFixed(1)}" width="${barW.toFixed(1)}" height="${Math.max(2, hFiring).toFixed(1)}" rx="1.5" fill="#ef4444" />`);
      }
      if (resolved > 0) {
        cursorY -= hResolved;
        segments.push(`<rect x="${xBar.toFixed(1)}" y="${cursorY.toFixed(1)}" width="${barW.toFixed(1)}" height="${Math.max(2, hResolved).toFixed(1)}" rx="1.5" fill="#10b981" />`);
      }
      if (filtered > 0) {
        cursorY -= hFiltered;
        segments.push(`<rect x="${xBar.toFixed(1)}" y="${cursorY.toFixed(1)}" width="${barW.toFixed(1)}" height="${Math.max(2, hFiltered).toFixed(1)}" rx="1.5" fill="#94a3b8" />`);
      }

      const showXLabel = i % 4 === 0 || i === buckets.length - 1;
      const xLabel = showXLabel
        ? `<text x="${(xCol + colStep / 2).toFixed(1)}" y="${height - 8}" text-anchor="middle" class="chart-axis-text">${escapeHtml(hour)}</text>`
        : '';

      const topY = Math.max(padTop, cursorY);

      return `
        <g class="chart-col-group" data-idx="${i}" data-hour="${escapeHtml(hour)}" data-firing="${firing}" data-resolved="${resolved}" data-filtered="${filtered}" data-x="${((xCol + colStep / 2) / width * 100).toFixed(2)}" data-y="${(topY / height * 100).toFixed(2)}">
          <rect x="${xCol.toFixed(1)}" y="${padTop}" width="${colStep.toFixed(1)}" height="${plotH}" rx="4" class="chart-col-highlight" />
          ${segments.join('')}
          ${xLabel}
          <rect x="${xCol.toFixed(1)}" y="${padTop}" width="${colStep.toFixed(1)}" height="${plotH}" class="chart-hit" />
        </g>
      `;
    })
    .join('');

  container.innerHTML = `
    <div class="chart-box">
      <svg class="chart-svg" viewBox="0 0 ${width} ${height}" role="img" aria-label="24小时告警趋势图">
        ${gridLinesHtml}
        ${barsHtml}
      </svg>
      <div class="chart-tooltip hidden" id="timeline-tooltip"></div>
    </div>
  `;

  const box = container.querySelector('.chart-box');
  const tooltip = container.querySelector('#timeline-tooltip');
  const groups = container.querySelectorAll('.chart-col-group');

  groups.forEach((group) => {
    group.addEventListener('mouseenter', () => {
      const { hour, firing, resolved, filtered, x, y } = group.dataset;
      const total = Number(firing) + Number(resolved) + Number(filtered);
      tooltip.innerHTML = `
        <div style="font-weight:700;margin-bottom:4px;font-family:var(--font-mono)">${escapeHtml(hour)} <span style="color:var(--text-muted);font-weight:500">(共 ${total})</span></div>
        <div style="display:flex;justify-content:space-between;gap:12px;"><span><span style="color:#ef4444">●</span> 告警中</span><strong>${escapeHtml(firing)}</strong></div>
        <div style="display:flex;justify-content:space-between;gap:12px;"><span><span style="color:#10b981">●</span> 已恢复</span><strong>${escapeHtml(resolved)}</strong></div>
        <div style="display:flex;justify-content:space-between;gap:12px;"><span><span style="color:#94a3b8">●</span> 已拦截</span><strong>${escapeHtml(filtered)}</strong></div>
      `;
      const clampedX = Math.max(15, Math.min(85, Number(x)));
      const clampedY = Math.max(24, Number(y));
      tooltip.style.left = `${clampedX}%`;
      tooltip.style.top = `${clampedY}%`;
      tooltip.classList.remove('hidden');
      group.classList.add('is-active');
    });

    group.addEventListener('mouseleave', () => {
      tooltip.classList.add('hidden');
      group.classList.remove('is-active');
    });
  });

  if (box) {
    box.addEventListener('mouseleave', () => {
      tooltip.classList.add('hidden');
    });
  }
}

/**
 * Render Severity Donut Chart (SVG) with legend highlighting.
 */
export function renderSeverityDonut(container, bySeverity = {}) {
  if (!container) return;

  const items = [
    { key: 'emergency', label: '紧急 (P0)', color: '#ef4444', value: Number(bySeverity.emergency) || 0 },
    { key: 'critical', label: '严重 (P1)', color: '#f97316', value: Number(bySeverity.critical) || 0 },
    { key: 'warning', label: '警告 (P2)', color: '#f59e0b', value: Number(bySeverity.warning) || 0 },
    { key: 'info', label: '信息 (P3)', color: '#0ea5e9', value: Number(bySeverity.info) || 0 },
    { key: 'other', label: '其他', color: '#94a3b8', value: Number(bySeverity.other) || 0 },
  ];

  const total = items.reduce((acc, item) => acc + item.value, 0);
  const size = 148;
  const cx = size / 2;
  const cy = size / 2;
  const r = 52;
  const strokeWidth = 18;
  const circumference = 2 * Math.PI * r;

  let offset = 0;
  const segmentsHtml = total > 0
    ? items
        .filter((it) => it.value > 0)
        .map((it) => {
          const frac = it.value / total;
          const len = frac * circumference;
          const dashArray = `${len.toFixed(2)} ${(circumference - len).toFixed(2)}`;
          const seg = `
            <circle
              class="donut-seg"
              data-key="${it.key}"
              cx="${cx}"
              cy="${cy}"
              r="${r}"
              fill="none"
              stroke="${it.color}"
              stroke-width="${strokeWidth}"
              stroke-dasharray="${dashArray}"
              stroke-dashoffset="${(-offset).toFixed(2)}"
              transform="rotate(-90 ${cx} ${cy})"
              style="transition: stroke-width 0.15s ease, opacity 0.15s ease; cursor: pointer;"
            />
          `;
          offset += len;
          return seg;
        })
        .join('')
    : `<circle cx="${cx}" cy="${cy}" r="${r}" fill="none" stroke="var(--border-color)" stroke-width="${strokeWidth}" />`;

  const legendHtml = items
    .map((it) => {
      const pct = total > 0 ? Math.round((it.value / total) * 100) : 0;
      return `
        <div class="donut-legend-row" data-key="${it.key}">
          <span class="legend-item">
            <span class="legend-dot" style="background:${it.color}"></span>
            <span>${escapeHtml(it.label)}</span>
          </span>
          <span class="mono" style="font-size:12px;">
            <strong>${it.value}</strong>
            <span class="text-muted"> (${pct}%)</span>
          </span>
        </div>
      `;
    })
    .join('');

  container.innerHTML = `
    <div class="donut-layout">
      <div class="donut-svg-wrap">
        <svg viewBox="0 0 ${size} ${size}" style="width:100%;height:100%;display:block;" role="img" aria-label="告警级别分布环形图">
          <circle cx="${cx}" cy="${cy}" r="${r}" fill="none" stroke="var(--bg-subtle)" stroke-width="${strokeWidth}" />
          ${segmentsHtml}
        </svg>
        <div class="donut-center-label">
          <div class="donut-total">${total}</div>
          <div class="donut-sub">${total > 0 ? '总告警' : '暂无数据'}</div>
        </div>
      </div>
      <div class="donut-legend-list">${legendHtml}</div>
    </div>
  `;

  const highlight = (activeKey) => {
    container.querySelectorAll('.donut-seg').forEach((el) => {
      const isMatch = !activeKey || el.dataset.key === activeKey;
      el.style.opacity = isMatch ? '1' : '0.35';
      el.setAttribute('stroke-width', activeKey && el.dataset.key === activeKey ? String(strokeWidth + 3) : String(strokeWidth));
    });
    container.querySelectorAll('.donut-legend-row').forEach((el) => {
      el.classList.toggle('is-active', Boolean(activeKey && el.dataset.key === activeKey));
    });
  };

  container.querySelectorAll('.donut-seg, .donut-legend-row').forEach((el) => {
    el.addEventListener('mouseenter', () => highlight(el.dataset.key));
    el.addEventListener('mouseleave', () => highlight(null));
  });
}

/**
 * Render Channel Delivery Health bars (wechat / dingtalk / feishu).
 */
export function renderChannelHealth(container, byChannel = {}) {
  if (!container) return;

  const order = ['wechat', 'dingtalk', 'feishu'];
  const html = order
    .map((key) => {
      const meta = clientMeta(key);
      const stat = (byChannel && byChannel[key]) || {};
      const enabled = Boolean(stat.enabled);
      const configured = Boolean(stat.configured);
      const sent = Number(stat.sent) || 0;
      const failed = Number(stat.failed) || 0;
      const avgLatency = Number(stat.avg_latency_ms) || 0;
      const total = sent + failed;
      const okPct = total > 0 ? (sent / total) * 100 : 0;
      const failPct = total > 0 ? (failed / total) * 100 : 0;
      const rateLabel = total > 0 ? `${Math.round(okPct)}%` : '--';

      const statusBadge = !enabled
        ? '<span class="pill pill-neutral">未启用</span>'
        : configured
          ? '<span class="pill pill-success">已就绪</span>'
          : '<span class="pill pill-warning">待配置 URL</span>';

      return `
        <div class="channel-health-item">
          <div class="channel-health-top">
            <div class="channel-health-name">
              <span>${escapeHtml(meta.name)}</span>
              ${statusBadge}
            </div>
            <span class="mono text-sm">成功率 <strong>${rateLabel}</strong></span>
          </div>
          <div class="progress-track" title="成功 ${sent} / 失败 ${failed}">
            <div class="progress-fill-ok" style="width:${okPct.toFixed(1)}%"></div>
            <div class="progress-fill-fail" style="width:${failPct.toFixed(1)}%"></div>
          </div>
          <div class="channel-health-meta">
            <span>成功 ${sent} · 失败 ${failed}</span>
            <span>平均延迟 ${avgLatency} ms</span>
          </div>
        </div>
      `;
    })
    .join('');

  container.innerHTML = `<div class="channel-health-list">${html}</div>`;
}
