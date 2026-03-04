(() => {
  function initNav() {
    const openButtons = document.querySelectorAll('[data-nav-toggle]');
    const closeTargets = document.querySelectorAll('[data-nav-close]');

    function setOpen(open) {
      document.body.classList.toggle('nav-open', Boolean(open));
    }

    openButtons.forEach((button) => {
      button.addEventListener('click', () => setOpen(!document.body.classList.contains('nav-open')));
    });

    closeTargets.forEach((target) => {
      target.addEventListener('click', () => setOpen(false));
    });

    window.addEventListener('keydown', (event) => {
      if (event.key === 'Escape') {
        setOpen(false);
      }
    });
  }

  function setBadge(kind, value) {
    if (value === null || value === undefined) return;
    document.querySelectorAll(`[data-badge="${kind}"]`).forEach((el) => {
      el.textContent = String(value);
    });
  }

  function normalizeApprovals(payload) {
    if (Array.isArray(payload)) return payload;
    if (payload && Array.isArray(payload.approvals)) return payload.approvals;
    return [];
  }

  async function updateBadges() {
    try {
      const probesResp = await fetch('/api/v1/probes', { cache: 'no-store' });
      if (probesResp.ok) {
        const probes = await probesResp.json();
        if (Array.isArray(probes)) {
          setBadge('probes', probes.length);
        }
      }
    } catch {
      // best effort
    }

    try {
      const approvalsResp = await fetch('/api/v1/approvals?status=pending', { cache: 'no-store' });
      if (approvalsResp.ok) {
        const payload = await approvalsResp.json();
        const approvals = normalizeApprovals(payload);
        const pending = approvals.filter((item) => {
          const state = item && (item.decision || item.status);
          return state === 'pending';
        }).length;
        setBadge('approvals', pending);
      }
    } catch {
      // best effort
    }
  }

  function connectSSE(handlers = {}) {
    const source = new EventSource('/api/v1/events');

    if (typeof handlers.onopen === 'function') {
      source.onopen = handlers.onopen;
    }
    if (typeof handlers.onerror === 'function') {
      source.onerror = handlers.onerror;
    }

    Object.entries(handlers).forEach(([eventName, fn]) => {
      if (eventName === 'onopen' || eventName === 'onerror' || typeof fn !== 'function') {
        return;
      }

      source.addEventListener(eventName, (event) => {
        let payload = null;
        try {
          payload = JSON.parse(event.data || '{}');
        } catch {
          payload = null;
        }
        fn(payload, event);
      });
    });

    return {
      close: () => source.close(),
      source,
    };
  }

  function initResizable() {
    document.querySelectorAll('.drag-handle').forEach((handle) => {
      let startX;
      let leftEl;
      let rightEl;
      let leftStart;
      let rightStart;

      handle.addEventListener('pointerdown', (event) => {
        event.preventDefault();
        handle.classList.add('active');
        startX = event.clientX;
        leftEl = handle.previousElementSibling;
        rightEl = handle.nextElementSibling;

        if (!leftEl || !rightEl) {
          handle.classList.remove('active');
          return;
        }

        leftStart = leftEl.getBoundingClientRect().width;
        rightStart = rightEl.getBoundingClientRect().width;

        const onMove = (moveEvent) => {
          const dx = moveEvent.clientX - startX;
          const leftMin = parseInt(getComputedStyle(leftEl).minWidth, 10) || 180;
          const rightMin = parseInt(getComputedStyle(rightEl).minWidth, 10) || 200;
          const newLeft = Math.max(leftMin, leftStart + dx);
          const newRight = Math.max(rightMin, rightStart - dx);
          leftEl.style.flexBasis = newLeft + 'px';
          rightEl.style.flexBasis = newRight + 'px';
        };

        const onUp = () => {
          handle.classList.remove('active');
          document.removeEventListener('pointermove', onMove);
          document.removeEventListener('pointerup', onUp);
        };

        document.addEventListener('pointermove', onMove);
        document.addEventListener('pointerup', onUp);
      });
    });
  }

  function showToast(message, kind = 'info', timeoutMs = 3000) {
    const toast = document.getElementById('toast');
    if (!toast) return;

    toast.textContent = message;
    toast.classList.add('show');

    toast.style.borderColor =
      kind === 'success' ? 'rgba(74, 222, 128, 0.5)' :
      kind === 'error' ? 'rgba(248, 113, 113, 0.5)' :
      'rgba(96, 165, 250, 0.5)';

    window.clearTimeout(showToast.timer);
    showToast.timer = window.setTimeout(() => {
      toast.classList.remove('show');
    }, timeoutMs);
  }

  document.addEventListener('DOMContentLoaded', () => {
    initNav();
    updateBadges();
    window.setInterval(updateBadges, 30000);
  });


  // ── Sandbox helpers ────────────────────────────────────────────────────────

  function sandboxEsc(value) {
    return String(value || '')
      .replaceAll('&', '&amp;')
      .replaceAll('<', '&lt;')
      .replaceAll('>', '&gt;')
      .replaceAll('"', '&quot;')
      .replaceAll("'", '&#39;');
  }

  function sandboxRelTime(isoStr) {
    if (!isoStr) return '—';
    const dt = new Date(isoStr);
    if (Number.isNaN(dt.getTime())) return isoStr;
    const diff = Math.floor((Date.now() - dt.getTime()) / 1000);
    if (diff < 60) return `${diff}s ago`;
    if (diff < 3600) return `${Math.floor(diff / 60)}m ago`;
    if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`;
    return `${Math.floor(diff / 86400)}d ago`;
  }

  function sandboxDuration(sess) {
    if (!sess || !sess.created_at) return '—';
    const start = new Date(sess.created_at).getTime();
    if (Number.isNaN(start)) return '—';
    const endRaw = sess.destroyed_at ? new Date(sess.destroyed_at) : new Date();
    const secs = Math.floor((endRaw.getTime() - start) / 1000);
    if (secs < 60) return `${secs}s`;
    const m = Math.floor(secs / 60);
    const s = secs % 60;
    if (m < 60) return `${m}m ${s}s`;
    const h = Math.floor(m / 60);
    return `${h}h ${m % 60}m`;
  }

  function sandboxStateTag(state) {
    const s = sandboxEsc(String(state || 'unknown').toLowerCase());
    return `<span class="tag sandbox-state-${s}">${s}</span>`;
  }

  function taskStateTag(state) {
    const s = sandboxEsc(String(state || 'unknown').toLowerCase());
    return `<span class="tag task-state-${s}">${s}</span>`;
  }

  async function sandboxRequest(url, options = {}) {
    const response = await fetch(url, { cache: 'no-store', credentials: 'include', ...options });
    let payload = null;
    if (response.status !== 204) {
      try { payload = await response.json(); } catch { payload = null; }
    }
    if (!response.ok) {
      const msg = payload?.message || payload?.error || response.statusText || 'Request failed';
      const err = new Error(msg);
      err.status = response.status;
      throw err;
    }
    return payload;
  }

  function initSandboxes() {
    const tableBody = document.getElementById('sandboxes-table-body');
    const listMeta  = document.getElementById('sandboxes-list-meta');
    const lastUpd   = document.getElementById('sandboxes-last-updated');
    const filterState   = document.getElementById('sandboxes-filter-state');
    const filterRuntime = document.getElementById('sandboxes-filter-runtime');
    const filterForm    = document.getElementById('sandboxes-filter-form');

    if (!tableBody) return;

    let allSessions = [];
    let refreshTimer = null;

    function applyFilters() {
      const stateVal   = filterState   ? filterState.value   : '';
      const runtimeVal = filterRuntime ? filterRuntime.value : '';
      return allSessions.filter((s) => {
        if (stateVal   && s.state         !== stateVal)   return false;
        if (runtimeVal && s.runtime_class !== runtimeVal) return false;
        return true;
      });
    }

    function renderSummary(sessions) {
      document.getElementById('sandboxes-count-total').textContent   = String(sessions.length);
      document.getElementById('sandboxes-count-running').textContent = String(sessions.filter((s) => s.state === 'running').length);
      document.getElementById('sandboxes-count-ready').textContent   = String(sessions.filter((s) => s.state === 'ready').length);
      document.getElementById('sandboxes-count-failed').textContent  = String(sessions.filter((s) => s.state === 'failed').length);
    }

    function renderRuntimeFilter(sessions) {
      if (!filterRuntime) return;
      const runtimes = [...new Set(sessions.map((s) => s.runtime_class).filter(Boolean))].sort();
      const cur = filterRuntime.value;
      filterRuntime.innerHTML = '<option value="">All runtimes</option>' +
        runtimes.map((r) => `<option value="${sandboxEsc(r)}">${sandboxEsc(r)}</option>`).join('');
      if (cur && runtimes.includes(cur)) filterRuntime.value = cur;
    }

    function renderTable(sessions) {
      const filtered = applyFilters();
      listMeta && (listMeta.textContent = `${filtered.length} session${filtered.length === 1 ? '' : 's'}`);
      if (!filtered.length) {
        tableBody.innerHTML = '<tr><td colspan="6" class="empty-state">No sandboxes found.</td></tr>';
        return;
      }
      tableBody.innerHTML = filtered.map((s) => `
        <tr style="cursor:pointer" onclick="location.href='/sandboxes/${sandboxEsc(s.id)}'">
          <td class="id-text">${sandboxEsc((s.id || '').substring(0, 8))}</td>
          <td>${sandboxEsc(s.runtime_class || '—')}</td>
          <td>${sandboxStateTag(s.state)}</td>
          <td class="id-text">${sandboxEsc(s.probe_id || '—')}</td>
          <td>${sandboxEsc(sandboxRelTime(s.created_at))}</td>
          <td>${sandboxEsc(sandboxDuration(s))}</td>
        </tr>
      `).join('');
    }

    async function refresh() {
      try {
        const sessions = await sandboxRequest('/api/v1/sandboxes');
        allSessions = Array.isArray(sessions) ? sessions : [];
        renderSummary(allSessions);
        renderRuntimeFilter(allSessions);
        renderTable(allSessions);
        lastUpd && (lastUpd.textContent = `Last updated: ${new Date().toLocaleTimeString()}`);
      } catch (err) {
        window.LegatorUI?.showToast?.(`Sandboxes refresh failed: ${err.message}`, 'error');
      }
    }

    document.getElementById('sandboxes-refresh')?.addEventListener('click', refresh);

    filterForm?.addEventListener('submit', (e) => { e.preventDefault(); renderTable(allSessions); });

    document.getElementById('sandboxes-filter-reset')?.addEventListener('click', () => {
      if (filterState)   filterState.value   = '';
      if (filterRuntime) filterRuntime.value = '';
      renderTable(allSessions);
    });

    refresh();
    refreshTimer = window.setInterval(refresh, 10000);
    window.addEventListener('beforeunload', () => window.clearInterval(refreshTimer));
  }

  function initSandboxDetail(sandboxId) {
    const termPane     = document.getElementById('terminal-pane');
    const termStatus   = document.getElementById('terminal-status');
    const scrollLock   = document.getElementById('scroll-lock-btn');
    const tasksBody    = document.getElementById('tasks-table-body');
    const tasksMeta    = document.getElementById('tasks-meta');
    const submitBtn    = document.getElementById('sandbox-submit-task-btn');
    const destroyBtn   = document.getElementById('sandbox-destroy-btn');
    const taskFormPanel = document.getElementById('sandbox-task-form-panel');
    const taskForm     = document.getElementById('sandbox-task-form');
    const cancelFormBtn = document.getElementById('sandbox-task-cancel-btn');

    if (!termPane || !sandboxId) return;

    let autoScroll = true;
    let lineCount  = 0;
    const MAX_LINES = 10000;
    let ws = null;

    function appendLine(text, kind) {
      const line = document.createElement('span');
      line.className = `terminal-line terminal-${kind}`;
      line.textContent = text;
      termPane.appendChild(line);
      lineCount++;

      // Trim oldest lines if over cap
      while (lineCount > MAX_LINES) {
        if (termPane.firstChild) {
          termPane.removeChild(termPane.firstChild);
          lineCount--;
        } else break;
      }

      if (autoScroll) termPane.scrollTop = termPane.scrollHeight;
    }

    function appendChunk(chunk) {
      const lines = (chunk.data || '').split('\n');
      const kind  = chunk.stream === 'stderr' ? 'stderr' : 'stdout';
      lines.forEach((line, i) => {
        // Last empty string from trailing newline: skip
        if (i === lines.length - 1 && line === '') return;
        appendLine(line, kind);
      });
    }

    function connectWS() {
      const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
      const wsUrl = `${proto}//${window.location.host}/ws/sandboxes/${encodeURIComponent(sandboxId)}/stream`;

      ws = new WebSocket(wsUrl);
      termStatus && (termStatus.textContent = 'Connecting…');

      ws.addEventListener('open', () => {
        termStatus && (termStatus.textContent = 'Connected');
      });

      ws.addEventListener('message', (event) => {
        try {
          const msg = JSON.parse(event.data);
          // The server sends chunk objects: {sequence, stream, data, ...}
          if (Array.isArray(msg)) {
            msg.forEach(appendChunk);
          } else if (msg && (msg.data !== undefined || msg.stream !== undefined)) {
            appendChunk(msg);
          }
        } catch {
          // raw text fallback
          appendLine(event.data, 'stdout');
        }
      });

      ws.addEventListener('close', () => {
        termStatus && (termStatus.textContent = 'Disconnected');
      });

      ws.addEventListener('error', () => {
        termStatus && (termStatus.textContent = 'Connection error');
      });
    }

    // Scroll lock toggle
    scrollLock?.addEventListener('click', () => {
      autoScroll = !autoScroll;
      scrollLock.textContent = `Auto-scroll: ${autoScroll ? 'ON' : 'OFF'}`;
      scrollLock.classList.toggle('locked', !autoScroll);
    });

    // Manual scroll → disable auto-scroll
    termPane.addEventListener('scroll', () => {
      const atBottom = termPane.scrollHeight - termPane.scrollTop - termPane.clientHeight < 20;
      if (!atBottom && autoScroll) {
        autoScroll = false;
        scrollLock && (scrollLock.textContent = 'Auto-scroll: OFF');
        scrollLock?.classList.add('locked');
      }
    });

    // Render tasks table from JSON payload
    function renderTasks(tasks) {
      if (!tasksBody) return;
      if (!tasks || !tasks.length) {
        tasksBody.innerHTML = '<tr><td colspan="8" class="empty-state">No tasks yet.</td></tr>';
        tasksMeta && (tasksMeta.textContent = '0 tasks');
        return;
      }
      tasksMeta && (tasksMeta.textContent = `${tasks.length} task${tasks.length === 1 ? '' : 's'}`);
      tasksBody.innerHTML = tasks.map((t) => {
        const cmd = t.command ? t.command.join(' ') : (t.repo_url || '—');
        const truncCmd = cmd.length > 40 ? cmd.substring(0, 40) + '…' : cmd;
        const isTerminal = ['succeeded', 'failed', 'cancelled'].includes(t.state);
        const cancelBtn = !isTerminal
          ? `<button class="btn btn-small" type="button" data-cancel-task="${sandboxEsc(t.id)}">Cancel</button>`
          : '<span class="muted">—</span>';
        const started   = t.started_at   ? new Date(t.started_at).toLocaleTimeString()   : '—';
        const completed = t.completed_at ? new Date(t.completed_at).toLocaleTimeString() : '—';
        return `<tr>
          <td class="id-text">${sandboxEsc((t.id || '').substring(0, 8))}</td>
          <td>${sandboxEsc(t.kind || '—')}</td>
          <td class="id-text" title="${sandboxEsc(cmd)}">${sandboxEsc(truncCmd)}</td>
          <td>${taskStateTag(t.state)}</td>
          <td>${isTerminal ? sandboxEsc(String(t.exit_code)) : '—'}</td>
          <td>${sandboxEsc(started)}</td>
          <td>${sandboxEsc(completed)}</td>
          <td>${cancelBtn}</td>
        </tr>`;
      }).join('');
    }

    // Cancel task
    tasksBody?.addEventListener('click', async (e) => {
      const btn = e.target.closest('button[data-cancel-task]');
      if (!btn) return;
      const taskId = btn.dataset.cancelTask;
      try {
        await sandboxRequest(
          `/api/v1/sandboxes/${encodeURIComponent(sandboxId)}/tasks/${encodeURIComponent(taskId)}/cancel`,
          { method: 'POST' }
        );
        window.LegatorUI?.showToast?.('Task cancelled', 'success');
        const tasks = await sandboxRequest(`/api/v1/sandboxes/${encodeURIComponent(sandboxId)}/tasks`);
        renderTasks(Array.isArray(tasks) ? tasks : []);
      } catch (err) {
        window.LegatorUI?.showToast?.(`Cancel failed: ${err.message}`, 'error');
      }
    });

    // Submit task form
    submitBtn?.addEventListener('click', () => {
      taskFormPanel && (taskFormPanel.style.display = '');
    });

    cancelFormBtn?.addEventListener('click', () => {
      taskFormPanel && (taskFormPanel.style.display = 'none');
    });

    taskForm?.addEventListener('submit', async (e) => {
      e.preventDefault();
      const kind    = document.getElementById('task-kind')?.value    || 'command';
      const cmdRaw  = document.getElementById('task-command')?.value || '';
      const image   = document.getElementById('task-image')?.value   || '';
      const timeout = parseInt(document.getElementById('task-timeout')?.value || '300', 10);
      const command = cmdRaw.trim().split(/\s+/).filter(Boolean);
      const body = { kind, command, timeout_secs: timeout };
      if (image) body.image = image;
      try {
        await sandboxRequest(
          `/api/v1/sandboxes/${encodeURIComponent(sandboxId)}/tasks`,
          { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }
        );
        window.LegatorUI?.showToast?.('Task submitted', 'success');
        taskFormPanel && (taskFormPanel.style.display = 'none');
        taskForm.reset();
        const tasks = await sandboxRequest(`/api/v1/sandboxes/${encodeURIComponent(sandboxId)}/tasks`);
        renderTasks(Array.isArray(tasks) ? tasks : []);
      } catch (err) {
        window.LegatorUI?.showToast?.(`Submit failed: ${err.message}`, 'error');
      }
    });

    // Destroy sandbox
    destroyBtn?.addEventListener('click', async () => {
      if (!window.confirm(`Destroy sandbox ${sandboxId}? This cannot be undone.`)) return;
      try {
        await sandboxRequest(
          `/api/v1/sandboxes/${encodeURIComponent(sandboxId)}`,
          { method: 'DELETE' }
        );
        window.LegatorUI?.showToast?.('Sandbox destroyed', 'success');
        window.setTimeout(() => { window.location.href = '/sandboxes'; }, 1000);
      } catch (err) {
        window.LegatorUI?.showToast?.(`Destroy failed: ${err.message}`, 'error');
      }
    });

    // Load tasks from API (refreshes server-side rendered table)
    sandboxRequest(`/api/v1/sandboxes/${encodeURIComponent(sandboxId)}/tasks`)
      .then((tasks) => renderTasks(Array.isArray(tasks) ? tasks : []))
      .catch(() => {}); // silently ignore; server-rendered table is still visible

    // ── Artifacts ─────────────────────────────────────────────────────────────
    const artifactsBody  = document.getElementById('artifacts-table-body');
    const artifactsMeta  = document.getElementById('artifacts-meta');
    const diffViewer     = document.getElementById('artifact-diff-viewer');
    const diffTitle      = document.getElementById('artifact-diff-title');
    const diffSummaryEl  = document.getElementById('artifact-diff-summary');
    const diffContent    = document.getElementById('artifact-diff-content');
    const diffClose      = document.getElementById('artifact-diff-close');

    function humanBytes(n) {
      if (n === undefined || n === null) return '—';
      if (n < 1024) return n + ' B';
      if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KB';
      return (n / (1024 * 1024)).toFixed(1) + ' MB';
    }

    function kindBadge(kind) {
      const k = sandboxEsc(String(kind || 'file').toLowerCase());
      return '<span class="artifact-kind artifact-kind-' + k + '">' + k + '</span>';
    }

    function renderDiffInline(raw) {
      if (!diffContent) return;
      const lines = raw.split('\n');
      diffContent.innerHTML = '';
      lines.forEach(function(line) {
        const span = document.createElement('span');
        if (line.startsWith('+++') || line.startsWith('---')) {
          span.className = 'diff-ctx diff-header';
        } else if (line.startsWith('+')) {
          span.className = 'diff-add';
        } else if (line.startsWith('-')) {
          span.className = 'diff-del';
        } else if (line.startsWith('@')) {
          span.className = 'diff-ctx diff-hunk';
        } else {
          span.className = 'diff-ctx';
        }
        span.textContent = line + '\n';
        diffContent.appendChild(span);
      });
    }

    async function showDiff(artifactId, path, diffSummary) {
      if (!diffViewer) return;
      try {
        const resp = await fetch(
          '/api/v1/sandboxes/' + encodeURIComponent(sandboxId) + '/artifacts/' + encodeURIComponent(artifactId) + '/content'
        );
        if (!resp.ok) throw new Error('HTTP ' + resp.status);
        const text = await resp.text();
        diffTitle && (diffTitle.textContent = path);
        diffSummaryEl && (diffSummaryEl.textContent = diffSummary || '');
        renderDiffInline(text);
        diffViewer.style.display = '';
        diffViewer.scrollIntoView({ behavior: 'smooth', block: 'nearest' });
      } catch (err) {
        window.LegatorUI && window.LegatorUI.showToast && window.LegatorUI.showToast('Failed to load diff: ' + err.message, 'error');
      }
    }

    diffClose && diffClose.addEventListener('click', function() {
      diffViewer && (diffViewer.style.display = 'none');
    });

    function renderArtifacts(artifacts) {
      if (!artifactsBody) return;
      if (!artifacts || !artifacts.length) {
        artifactsBody.innerHTML = '<tr><td colspan="6" class="empty-state">No artifacts yet.</td></tr>';
        artifactsMeta && (artifactsMeta.textContent = '0 artifacts');
        return;
      }
      const count = artifacts.length;
      artifactsMeta && (artifactsMeta.textContent = count + ' artifact' + (count === 1 ? '' : 's'));
      artifactsBody.innerHTML = artifacts.map(function(a) {
        const sha = (a.sha256 || '').substring(0, 12);
        const path = sandboxEsc(a.path || '—');
        const isDiff = a.kind === 'diff';
        const viewBtn = isDiff
          ? '<button class="btn btn-small" type="button" data-view-diff="' + sandboxEsc(a.id) + '" data-diff-path="' + sandboxEsc(a.path) + '" data-diff-summary="' + sandboxEsc(a.diff_summary || '') + '">View Diff</button>'
          : '';
        const dlUrl = '/api/v1/sandboxes/' + encodeURIComponent(sandboxId) + '/artifacts/' + encodeURIComponent(a.id) + '/content';
        return '<tr>' +
          '<td class="id-text" title="' + path + '">' + path + '</td>' +
          '<td>' + kindBadge(a.kind) + '</td>' +
          '<td class="muted">' + sandboxEsc(humanBytes(a.size)) + '</td>' +
          '<td class="id-text" title="' + sandboxEsc(a.sha256 || '') + '">' + sandboxEsc(sha) + '</td>' +
          '<td class="id-text">' + sandboxEsc((a.task_id || '').substring(0, 8) || '—') + '</td>' +
          '<td>' + viewBtn + ' <a class="btn btn-small" href="' + dlUrl + '" download>Download</a></td>' +
          '</tr>';
      }).join('');
    }

    // Click handler for "View Diff" buttons
    artifactsBody && artifactsBody.addEventListener('click', async function(e) {
      const btn = e.target.closest('button[data-view-diff]');
      if (!btn) return;
      await showDiff(btn.dataset.viewDiff, btn.dataset.diffPath, btn.dataset.diffSummary);
    });

    // Load artifacts from API
    sandboxRequest('/api/v1/sandboxes/' + encodeURIComponent(sandboxId) + '/artifacts')
      .then(function(resp) { renderArtifacts(resp && resp.artifacts ? resp.artifacts : []); })
      .catch(function() {
        if (artifactsBody) artifactsBody.innerHTML = '<tr><td colspan="6" class="empty-state muted">Could not load artifacts.</td></tr>';
      });

    // Connect WebSocket for live output
    connectWS();
  }

  window.LegatorUI = {
    showToast,
    updateBadges,
    connectSSE,
    initResizable,
    initSandboxes,
    initSandboxDetail,
  };
})();
