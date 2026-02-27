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

  window.LegatorUI = {
    showToast,
    updateBadges,
    connectSSE,
    initResizable,
  };
})();
