(() => {
  "use strict";

  const reducedMotion = window.matchMedia("(prefers-reduced-motion: reduce)");
  const loadingTimers = new WeakMap();

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
})();
