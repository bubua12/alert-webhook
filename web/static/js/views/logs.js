import {
  api,
  escapeHtml,
  formatTimestamp,
  copyToClipboard,
  debounce,
} from '../api.js';

export function mountLogs(root, ctx) {
  let destroyed = false;
  let reqSeq = 0;

  let level = '';
  let keyword = '';
  let limit = 200;
  let autoScroll = true;
  let paused = false;
  let items = [];
  let total = 0;

  root.innerHTML = `
    <div style="display:flex;flex-direction:column;gap:16px;">
      <div class="card">
        <div class="card-body">
          <div style="display:flex;align-items:center;justify-content:space-between;gap:12px;flex-wrap:wrap;">
            <div style="display:flex;align-items:center;gap:6px;flex-wrap:wrap;" role="group" aria-label="日志级别筛选">
              <button type="button" class="btn btn-sm btn-primary" data-log-level="">ALL</button>
              <button type="button" class="btn btn-sm btn-secondary" data-log-level="INFO">INFO</button>
              <button type="button" class="btn btn-sm btn-secondary" data-log-level="WARN">WARN</button>
              <button type="button" class="btn btn-sm btn-secondary" data-log-level="ERROR">ERROR</button>
            </div>

            <div style="display:flex;align-items:center;gap:8px;flex-wrap:wrap;flex:1;justify-content:flex-end;">
              <input id="logs-q" class="form-control" style="max-width:260px;min-width:160px;" type="search" placeholder="过滤日志关键词...">
              <select id="logs-limit" class="form-control" style="width:auto;">
                <option value="100">最近 100 条</option>
                <option value="200" selected>最近 200 条</option>
                <option value="500">最近 500 条</option>
              </select>
              <label class="refresh-switch" title="更新日志后自动滚动到底部">
                <input id="logs-autoscroll" type="checkbox" checked>
                <span class="switch-track"></span>
                <span>自动滚动</span>
              </label>
              <button id="logs-pause" class="btn btn-secondary btn-sm" type="button">暂停刷新</button>
              <button id="logs-refresh" class="btn btn-secondary btn-sm" type="button">立即刷新</button>
              <button id="logs-copy" class="btn btn-ghost btn-sm" type="button">复制日志</button>
            </div>
          </div>

          <div style="display:flex;align-items:center;justify-content:space-between;gap:8px;margin-top:10px;">
            <div id="logs-meta" class="text-xs text-muted mono">加载中...</div>
            <span id="logs-paused-badge" class="pill pill-warning hidden">已暂停实时更新</span>
          </div>
        </div>
      </div>

      <div id="logs-terminal" class="log-terminal" aria-live="off">
        <div class="empty-state"><span class="spinner"></span> 正在加载运行日志...</div>
      </div>
    </div>
  `;

  const terminal = root.querySelector('#logs-terminal');
  const metaEl = root.querySelector('#logs-meta');
  const pausedBadge = root.querySelector('#logs-paused-badge');
  const pauseBtn = root.querySelector('#logs-pause');
  const qInput = root.querySelector('#logs-q');
  const limitSelect = root.querySelector('#logs-limit');
  const autoScrollChk = root.querySelector('#logs-autoscroll');

  function updateLevelButtons() {
    root.querySelectorAll('[data-log-level]').forEach((btn) => {
      const active = btn.dataset.logLevel === level;
      btn.className = `btn btn-sm ${active ? 'btn-primary' : 'btn-secondary'}`;
    });
  }

  function renderLogs() {
    if (destroyed || !terminal || !metaEl) return;
    metaEl.textContent = `显示 ${items.length} / 缓冲区共 ${total} 条日志`;

    if (items.length === 0) {
      terminal.innerHTML = '<div class="empty-state">暂无匹配的运行日志</div>';
      return;
    }

    terminal.innerHTML = items
      .map((entry) => {
        const lvl = String(entry.level || 'INFO').toUpperCase();
        const safeLvl = lvl === 'ERROR' || lvl === 'WARN' || lvl === 'INFO' ? lvl : 'INFO';
        return `
          <div class="log-line">
            <span class="log-time">${escapeHtml(formatTimestamp(entry.timestamp))}</span>
            <span class="log-level ${safeLvl}">${escapeHtml(safeLvl)}</span>
            <span class="log-msg">${escapeHtml(entry.message || '')}</span>
          </div>
        `;
      })
      .join('');

    if (autoScroll) {
      terminal.scrollTop = terminal.scrollHeight;
    }
  }

  async function load({ silent = false, force = false } = {}) {
    if (paused && !force) return;
    const thisReq = ++reqSeq;
    try {
      const res = await api.getLogs({ level, q: keyword, limit });
      if (destroyed || thisReq !== reqSeq) return;
      items = Array.isArray(res?.items) ? res.items : [];
      total = Number(res?.total) || items.length;
      renderLogs();
    } catch (err) {
      if (destroyed || thisReq !== reqSeq) return;
      if (!silent && terminal) {
        terminal.innerHTML = `<div class="empty-state text-danger">加载日志失败: ${escapeHtml(err.message)}</div>`;
      }
    }
  }

  root.querySelectorAll('[data-log-level]').forEach((btn) => {
    btn.addEventListener('click', () => {
      level = btn.dataset.logLevel || '';
      updateLevelButtons();
      load({ force: true });
    });
  });

  const debouncedSearch = debounce(() => {
    keyword = (qInput?.value || '').trim();
    load({ force: true });
  }, 250);
  qInput?.addEventListener('input', debouncedSearch);

  limitSelect?.addEventListener('change', () => {
    limit = Number(limitSelect.value) || 200;
    load({ force: true });
  });

  autoScrollChk?.addEventListener('change', () => {
    autoScroll = Boolean(autoScrollChk.checked);
    if (autoScroll && terminal) {
      terminal.scrollTop = terminal.scrollHeight;
    }
  });

  pauseBtn?.addEventListener('click', () => {
    paused = !paused;
    pauseBtn.textContent = paused ? '恢复刷新' : '暂停刷新';
    pauseBtn.className = `btn btn-sm ${paused ? 'btn-primary' : 'btn-secondary'}`;
    pausedBadge?.classList.toggle('hidden', !paused);
    if (!paused) load({ force: true });
  });

  root.querySelector('#logs-refresh')?.addEventListener('click', () => load({ force: true }));

  root.querySelector('#logs-copy')?.addEventListener('click', async () => {
    if (items.length === 0) {
      ctx.toast('当前没有可复制的日志', 'warning');
      return;
    }
    const text = items
      .map((it) => `${formatTimestamp(it.timestamp)} [${it.level || 'INFO'}] ${it.message || ''}`)
      .join('\n');
    const ok = await copyToClipboard(text);
    ctx.toast(ok ? `已复制 ${items.length} 条日志` : '复制失败', ok ? 'success' : 'error');
  });

  load({ force: true });

  return {
    isPaused: () => paused,
    refresh: load,
    destroy: () => {
      destroyed = true;
    },
  };
}
