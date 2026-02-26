(() => {
  function refreshIcons() {
    if (!window.lucide || typeof window.lucide.createIcons !== 'function') return;
    window.lucide.createIcons();
  }

  function toggleSidebar(force) {
    const shouldOpen = typeof force === 'boolean' ? force : !document.body.classList.contains('sidebar-open');
    document.body.classList.toggle('sidebar-open', shouldOpen);
  }

  function initSidebar() {
    document.querySelectorAll('[data-sidebar-toggle]').forEach((button) => {
      button.addEventListener('click', () => toggleSidebar());
    });

    document.querySelectorAll('[data-sidebar-close]').forEach((target) => {
      target.addEventListener('click', () => toggleSidebar(false));
    });

    window.addEventListener('keydown', (event) => {
      if (event.key === 'Escape') {
        toggleSidebar(false);
      }
    });
  }

  function setBadge(selector, value) {
    document.querySelectorAll(selector).forEach((badge) => {
      if (value === null || value === undefined) return;
      badge.textContent = String(value);
    });
  }

  async function updateSidebarCounts() {
    try {
      const probesResp = await fetch('/api/v1/probes', { cache: 'no-store' });
      if (probesResp.ok) {
        const probes = await probesResp.json();
        if (Array.isArray(probes)) {
          setBadge('[data-badge="probes"]', probes.length);

          const chatLink = document.querySelector('[data-nav-chat]');
          if (chatLink && probes.length > 0 && probes[0].id) {
            chatLink.href = `/probe/${encodeURIComponent(probes[0].id)}/chat`;
          }
        }
      }
    } catch {
      // best effort
    }

    try {
      const approvalsResp = await fetch('/api/v1/approvals', { cache: 'no-store' });
      if (approvalsResp.ok) {
        const approvals = await approvalsResp.json();
        if (Array.isArray(approvals)) {
          const pending = approvals.filter((item) => item && item.status === 'pending').length;
          setBadge('[data-badge="approvals"]', pending);
        }
      }
    } catch {
      // best effort
    }
  }

  function showToast(message, kind = 'info', timeoutMs = 3000) {
    const toast = document.getElementById('toast');
    if (!toast) return;
    toast.textContent = message;
    toast.classList.add('show');
    toast.style.borderColor =
      kind === 'success' ? 'rgba(34, 197, 94, 0.5)' :
      kind === 'error' ? 'rgba(239, 68, 68, 0.5)' :
      'rgba(59, 130, 246, 0.5)';

    window.clearTimeout(showToast.timer);
    showToast.timer = window.setTimeout(() => {
      toast.classList.remove('show');
    }, timeoutMs);
  }

  document.addEventListener('DOMContentLoaded', () => {
    initSidebar();
    refreshIcons();

    if (document.querySelector('.sidebar')) {
      void updateSidebarCounts();
      window.setInterval(updateSidebarCounts, 30000);
    }
  });

  window.LegatorUI = {
    refreshIcons,
    showToast,
    updateSidebarCounts,
    toggleSidebar,
  };
})();
