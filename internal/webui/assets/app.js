(() => {
  "use strict";

  const reducedMotion = window.matchMedia("(prefers-reduced-motion: reduce)");
  const loadingTimers = new WeakMap();

  document.addEventListener("htmx:beforeTransition", (event) => {
    if (reducedMotion.matches || document.visibilityState !== "visible") event.preventDefault();
  });

  function startLoading(target) {
    if (!(target instanceof HTMLElement) || target.id !== "app-frame") return;
    target.setAttribute("aria-busy", "true");
    const timer = window.setTimeout(() => target.classList.add("is-loading"), 120);
    loadingTimers.set(target, timer);
  }

  function stopLoading(target) {
    if (!(target instanceof HTMLElement)) return;
    const timer = loadingTimers.get(target);
    if (timer) window.clearTimeout(timer);
    loadingTimers.delete(target);
    target.classList.remove("is-loading");
    target.setAttribute("aria-busy", "false");
  }

  function pulse(element, className) {
    if (!(element instanceof HTMLElement)) return;
    element.classList.remove(className);
    void element.offsetWidth;
    element.classList.add(className);
    window.setTimeout(() => element.classList.remove(className), 260);
  }

  document.addEventListener("htmx:beforeRequest", (event) => startLoading(event.detail.target));
  document.addEventListener("htmx:beforeSwap", (event) => stopLoading(event.detail.target));
  document.addEventListener("htmx:afterRequest", (event) => {
    stopLoading(event.detail.target);
    if (event.detail.successful) pulse(event.detail.elt, "action-confirmed");
  });
  document.addEventListener("htmx:afterSwap", (event) => {
    const frame = event.detail.target && event.detail.target.closest("#app-frame");
    if (frame) pulse(frame, "is-entering");
  });
  document.addEventListener("htmx:responseError", (event) => {
    const source = event.detail.elt;
    pulse(source instanceof HTMLElement ? source.closest("form, .workspace, .login-panel") : null, "request-failed");
  });

  document.addEventListener("pointerup", (event) => {
    const button = event.target instanceof Element ? event.target.closest("button[type='submit']") : null;
    const form = button && button.closest("form");
    if (!form || !form.hasAttribute("hx-post") || reducedMotion.matches) return;
    if (typeof navigator.vibrate === "function") navigator.vibrate(8);
  }, { passive: true });
  const removeToast = (toast) => {
    toast.classList.add("toast-leaving");
    window.setTimeout(() => toast.remove(), reducedMotion.matches ? 0 : 140);
  };

  const installToasts = (root = document) => {
    const region = document.getElementById("toast-region");
    if (!region) return;
    root.querySelectorAll(".notice[data-toast]:not([data-toast-ready])").forEach((notice) => {
      notice.dataset.toastReady = "true";
      const key = `${notice.dataset.toastKind || "notice"}:${notice.textContent.trim()}`;
      const duplicate = Array.from(region.children).find((item) => item.dataset.toastKey === key);
      if (duplicate) {
        pulse(duplicate);
        notice.remove();
        return;
      }
      notice.dataset.toastKey = key;
      notice.classList.add("toast");
      const close = document.createElement("button");
      close.type = "button";
      close.className = "toast-close";
      close.setAttribute("aria-label", "Dismiss notification");
      close.textContent = "CLOSE";
      close.addEventListener("click", () => removeToast(notice));
      notice.append(close);
      region.append(notice);
      const lifetime = notice.dataset.toastKind === "error" ? 8000 : 4000;
      window.setTimeout(() => {
        if (notice.isConnected) removeToast(notice);
      }, lifetime);
    });
  };

  const installProjectSearch = (root = document) => {
    root.querySelectorAll("[data-project-search-region]:not([data-search-ready])").forEach((region) => {
      region.dataset.searchReady = "true";
      const input = region.querySelector("[data-project-search]");
      const list = region.parentElement.querySelector("[data-project-candidates]");
      const count = region.querySelector("[data-project-visible-count]");
      if (!input || !list || !count) return;
      const candidates = Array.from(list.querySelectorAll("[data-project-candidate]"));
      const empty = document.createElement("p");
      empty.className = "empty-state project-search-empty";
      empty.textContent = "NO MATCHING CHECKOUTS";
      empty.hidden = true;
      list.append(empty);
      input.addEventListener("input", () => {
        const query = input.value.trim().toLocaleLowerCase();
        let visible = 0;
        candidates.forEach((candidate) => {
          const matches = !query || candidate.dataset.projectSearchValue.toLocaleLowerCase().includes(query);
          candidate.hidden = !matches;
          if (matches) visible += 1;
        });
        count.textContent = String(visible);
        empty.hidden = visible !== 0;
      });
    });
  };

  const installEnhancements = (root = document) => {
    installToasts(root);
    installProjectSearch(root);
  };

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", () => installEnhancements());
  } else {
    installEnhancements();
  }
  document.addEventListener("htmx:afterSwap", (event) => installEnhancements(event.target));
})();
