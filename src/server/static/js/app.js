// Pastebin — Main JavaScript
// Progressive enhancement only. All core features work without this file.

// ─── i18n ─────────────────────────────────────────────────────────────────────

// English fallback strings for the few messages this script generates at
// runtime. The server embeds the active-locale bundle as an inert JSON
// script block (id="pb-i18n-data") rather than an executable inline
// <script>, per project rules (no inline JS). This map is used when that
// bundle is absent or a key is missing (e.g. cached page, no template).
const I18N_FALLBACK = {
    update_available: 'A new version is available.',
    update_now: 'Update Now',
    dismiss: 'Dismiss',
    copied: 'Copied!',
    copy: 'Copy',
    api_error: 'API error',
    creating: 'Creating…',
    saving: 'Saving…',
    submitting: 'Submitting…',
    deleting: 'Deleting…',
    sending: 'Sending…',
    searching: 'Searching…',
    uploading: 'Uploading…',
    working: '…',
    offline: 'You are offline',
    content_link_label: 'Target URL',
};

// loadI18nBundle parses the server-rendered <script type="application/json"
// id="pb-i18n-data"> block into a plain object. Returns {} when the block is
// absent or unparsable, so callers always fall back to I18N_FALLBACK.
function loadI18nBundle() {
    const el = document.getElementById('pb-i18n-data');
    if (!el) return {};
    try {
        return JSON.parse(el.textContent || '{}');
    } catch (_) {
        return {};
    }
}

const PB_I18N = loadI18nBundle();

// t returns the localized string for key, falling back to English.
function t(key) {
    return PB_I18N[key] || I18N_FALLBACK[key] || key;
}

// ─── Toast Notifications ─────────────────────────────────────────────────────

// getToastContainer returns the #toast-container element, creating it if missing.
function getToastContainer() {
    var container = document.getElementById('toast-container');
    if (!container) {
        container = document.createElement('div');
        container.id = 'toast-container';
        document.body.appendChild(container);
    }
    return container;
}

// Auto-dismiss durations per toast type (PART 16 Toast Behavior Rules):
// success/info 3s, warning 5s, error never auto-dismisses (0 = stays until
// clicked/closed).
const TOAST_DURATIONS = { success: 3000, info: 3000, warning: 5000, error: 0 };
const TOAST_ICONS = { success: '✓', error: '✗', warning: '⚠', info: 'ℹ' };
const TOAST_MAX_VISIBLE = 5;

// toastQueue holds toasts created while TOAST_MAX_VISIBLE are already shown;
// they render as space frees up (PART 16: "Older toasts queue until space
// available").
var toastQueue = [];

// visibleToastCount counts transient toasts currently shown (persistent
// entries like the SW update banner and offline indicator are excluded —
// they are not marked data-toast).
function visibleToastCount() {
    return getToastContainer().querySelectorAll('.toast[data-toast="true"]:not(.toast-dismissing)').length;
}

// processToastQueue renders the next queued toast once a slot frees up.
function processToastQueue() {
    if (toastQueue.length === 0 || visibleToastCount() >= TOAST_MAX_VISIBLE) return;
    getToastContainer().prepend(toastQueue.shift());
}

// armToastTimer starts (or resumes) the auto-dismiss countdown for a toast.
function armToastTimer(toast, duration) {
    toast.__toastRemaining = duration;
    toast.__toastStart = Date.now();
    toast.__toastTimer = setTimeout(function () { dismissToast(toast); }, duration);
}

// pauseToastTimer stops the countdown on hover; the progress bar itself
// pauses via the pure-CSS `.toast:hover .toast-progress` rule.
function pauseToastTimer(toast) {
    if (!toast.__toastTimer) return;
    clearTimeout(toast.__toastTimer);
    toast.__toastTimer = null;
    toast.__toastRemaining -= Date.now() - toast.__toastStart;
}

// resumeToastTimer restarts the countdown with the time remaining when hover ends.
function resumeToastTimer(toast) {
    if (toast.__toastTimer || toast.__toastRemaining == null) return;
    toast.__toastStart = Date.now();
    toast.__toastTimer = setTimeout(function () { dismissToast(toast); }, Math.max(toast.__toastRemaining, 0));
}

// dismissToast plays the fade-out animation, removes the toast, then
// promotes the next queued toast (if any) into its place.
function dismissToast(toast) {
    if (toast.__toastTimer) clearTimeout(toast.__toastTimer);
    if (toast.classList.contains('toast-dismissing')) return;
    toast.classList.add('toast-dismissing');
    toast.addEventListener('animationend', function onEnd() {
        toast.removeEventListener('animationend', onEnd);
        if (toast.parentNode) toast.parentNode.removeChild(toast);
        processToastQueue();
    });
}

// showToast displays a transient notification in the #toast-container element.
// type: 'info' | 'success' | 'warning' | 'error'
function showToast(message, type) {
    var toastType = type || 'info';
    var toast = document.createElement('div');
    toast.className = 'toast toast-' + toastType;
    toast.setAttribute('role', 'alert');
    toast.setAttribute('aria-live', 'polite');
    toast.dataset.toast = 'true';

    var icon = document.createElement('span');
    icon.className = 'toast-icon';
    icon.setAttribute('aria-hidden', 'true');
    icon.textContent = TOAST_ICONS[toastType] || TOAST_ICONS.info;

    var msg = document.createElement('span');
    msg.className = 'toast-message';
    msg.textContent = message;

    var close = document.createElement('button');
    close.type = 'button';
    close.className = 'toast-close';
    close.setAttribute('aria-label', t('dismiss'));
    close.innerHTML = '&times;';
    close.addEventListener('click', function (event) {
        event.stopPropagation();
        dismissToast(toast);
    });

    toast.append(icon, msg, close);
    toast.addEventListener('click', function () { dismissToast(toast); });

    var duration = TOAST_DURATIONS[toastType];
    if (duration === undefined) duration = TOAST_DURATIONS.info;
    if (duration > 0) {
        var progress = document.createElement('div');
        progress.className = 'toast-progress';
        progress.style.animationDuration = duration + 'ms';
        toast.appendChild(progress);
        armToastTimer(toast, duration);
        toast.addEventListener('mouseenter', function () { pauseToastTimer(toast); });
        toast.addEventListener('mouseleave', function () { resumeToastTimer(toast); });
    }

    if (visibleToastCount() >= TOAST_MAX_VISIBLE) {
        toastQueue.push(toast);
        return;
    }
    getToastContainer().prepend(toast);
}

// Escape dismisses the topmost (newest, first-rendered) toast.
document.addEventListener('keydown', function (event) {
    if (event.key !== 'Escape') return;
    var topmost = document.querySelector('#toast-container .toast[data-toast="true"]:not(.toast-dismissing)');
    if (topmost) dismissToast(topmost);
});

// ─── Service worker ───────────────────────────────────────────────────────────

if ('serviceWorker' in navigator) {
    window.addEventListener('load', async () => {
        try {
            const registration = await navigator.serviceWorker.register('/sw.js', { scope: '/' });

            // Detect when a new service worker version is waiting.
            registration.addEventListener('updatefound', () => {
                const newWorker = registration.installing;
                if (!newWorker) return;
                newWorker.addEventListener('statechange', () => {
                    if (newWorker.state === 'installed' && navigator.serviceWorker.controller) {
                        showUpdateBanner(registration);
                    }
                });
            });

            // Check for SW updates hourly when the app is active.
            setInterval(() => { registration.update(); }, 60 * 60 * 1000);
        } catch (_) {
            // Service worker unavailable — not an error condition.
        }
    });

    // Reload when a new SW takes control (after user clicks "Update Now").
    navigator.serviceWorker.addEventListener('controllerchange', () => {
        window.location.reload();
    });
}

// showUpdateBanner injects an update-available banner into the #toast-container,
// so it shares the project's single fixed-position notification slot rather than
// pinning its own element to the viewport.
function showUpdateBanner(registration) {
    if (document.getElementById('sw-update-banner')) return;
    const banner = document.createElement('div');
    banner.id = 'sw-update-banner';
    banner.className = 'toast toast-info sw-update-banner';
    banner.setAttribute('role', 'status');
    banner.setAttribute('aria-live', 'polite');

    const msg = document.createElement('span');
    msg.textContent = t('update_available');

    const applyBtn = document.createElement('button');
    applyBtn.className = 'sw-update-banner__btn-apply';
    applyBtn.textContent = t('update_now');
    applyBtn.addEventListener('click', applyUpdate);

    const dismissBtn = document.createElement('button');
    dismissBtn.className = 'sw-update-banner__btn-dismiss';
    dismissBtn.textContent = '✕';
    dismissBtn.setAttribute('aria-label', t('dismiss'));
    dismissBtn.addEventListener('click', () => banner.remove());

    banner.appendChild(msg);
    banner.appendChild(applyBtn);
    banner.appendChild(dismissBtn);
    getToastContainer().appendChild(banner);
    window.__swRegistration = registration;
}

// applyUpdate signals the waiting service worker to skip waiting.
function applyUpdate() {
    const reg = window.__swRegistration;
    if (reg && reg.waiting) {
        reg.waiting.postMessage({ type: 'SKIP_WAITING' });
    }
}

// ─── App install prompt ──────────────────────────────────────────────────────

let deferredInstallPrompt = null;

// isInstalledPWA reports whether the page runs as an installed app.
function isInstalledPWA() {
    return window.matchMedia('(display-mode: standalone)').matches
        || window.navigator.standalone === true;
}

// isIOSSafari reports iOS devices, which never fire beforeinstallprompt and
// need manual "Add to Home Screen" instructions instead.
function isIOSSafari() {
    return /iPad|iPhone|iPod/.test(navigator.userAgent);
}

// showInstallButton reveals the server-rendered footer install affordance.
function showInstallButton() {
    const wrap = document.querySelector('.footer-install');
    if (wrap) wrap.hidden = false;
}

// hideInstallButton hides the footer install affordance.
function hideInstallButton() {
    const wrap = document.querySelector('.footer-install');
    if (wrap) wrap.hidden = true;
}

// installApp triggers the captured browser prompt, or the iOS instructions
// dialog when no prompt event exists (iOS Safari).
function installApp() {
    if (deferredInstallPrompt) {
        deferredInstallPrompt.prompt();
        deferredInstallPrompt.userChoice.then(function () {
            deferredInstallPrompt = null;
            hideInstallButton();
        });
        return;
    }
    const dlg = document.getElementById('ios-install-dialog');
    if (dlg && typeof dlg.showModal === 'function') dlg.showModal();
}

// Language selector (PART 30): the footer form is a plain GET form that works
// without JavaScript. As progressive enhancement, changing the select submits
// it immediately and the Go button is hidden.
document.addEventListener('DOMContentLoaded', function () {
    document.querySelectorAll('form.lang-selector').forEach(function (form) {
        const select = form.querySelector('select[name="lang"]');
        const go = form.querySelector('.lang-selector-go');
        if (!select) return;
        if (go) go.hidden = true;
        select.addEventListener('change', function () {
            form.requestSubmit();
        });
    });
});

// Destructive-action confirmation (PART 16: native <dialog>, never confirm()).
// A form carrying data-confirm="message" is intercepted on submit, the shared
// #confirm-dialog is shown with that message, and the form is re-submitted only
// when the user picks Confirm. Without JavaScript the form submits directly;
// the server enforces authorization either way.
document.addEventListener('submit', function (event) {
    const form = event.target;
    if (!(form instanceof HTMLFormElement) || !form.hasAttribute('data-confirm')) return;
    if (form.dataset.confirmed === 'true') {
        delete form.dataset.confirmed;
        return;
    }
    const dlg = document.getElementById('confirm-dialog');
    if (!dlg || typeof dlg.showModal !== 'function') return;
    event.preventDefault();
    const message = document.getElementById('confirm-dialog-message');
    if (message) message.textContent = form.getAttribute('data-confirm');
    dlg.returnValue = '';
    dlg.addEventListener('close', function onClose() {
        dlg.removeEventListener('close', onClose);
        if (dlg.returnValue === 'confirm') {
            form.dataset.confirmed = 'true';
            form.requestSubmit();
        }
    });
    dlg.showModal();
});

// Capture the install prompt; never show the browser default automatically.
window.addEventListener('beforeinstallprompt', function (event) {
    event.preventDefault();
    if (isInstalledPWA()) return;
    deferredInstallPrompt = event;
    showInstallButton();
});

// Hide the affordance once the app is installed.
window.addEventListener('appinstalled', function () {
    deferredInstallPrompt = null;
    hideInstallButton();
});

// Wire the install button and surface it on iOS Safari, where
// beforeinstallprompt never fires but manual install is possible.
document.addEventListener('DOMContentLoaded', function () {
    const btn = document.querySelector('[data-action="install-app"]');
    if (btn) btn.addEventListener('click', installApp);
    if (isIOSSafari() && !isInstalledPWA()) showInstallButton();
});

// ─── Offline Detection ───────────────────────────────────────────────────────

// Toggle a persistent #offline-indicator entry inside #toast-container based on
// network connectivity, so it shares the single fixed-position notification slot
// rather than pinning its own element to the viewport.
function updateOfflineIndicator() {
    const existing = document.getElementById('offline-indicator');
    if (navigator.onLine) {
        if (existing) existing.remove();
        return;
    }
    if (existing) return;
    const indicator = document.createElement('div');
    indicator.id = 'offline-indicator';
    indicator.className = 'toast toast-warning';
    indicator.setAttribute('role', 'status');
    indicator.setAttribute('aria-live', 'polite');
    indicator.textContent = t('offline');
    getToastContainer().appendChild(indicator);
}

window.addEventListener('online', updateOfflineIndicator);
window.addEventListener('offline', updateOfflineIndicator);

document.addEventListener('DOMContentLoaded', updateOfflineIndicator);

// ─── Theme ────────────────────────────────────────────────────────────────────

// Source of truth is the server-readable `theme` cookie rendered as the class on
// <html> (PART 16). No preference is read from localStorage. Without JS, the
// header <form> POSTs to /server/preferences, sets the cookie, and reloads. This enhancement
// intercepts the submit to cycle the theme in place — no reload, no FOUC.

// themeCycle advances dark → light → auto → dark, matching nextTheme() on the server.
function themeCycle(current) {
    if (current === 'dark') return 'light';
    if (current === 'light') return 'auto';
    return 'dark';
}

// currentTheme reads the active mode from the class on <html> (theme-dark by default).
function currentTheme() {
    const cls = document.documentElement.className || '';
    const m = cls.match(/theme-(dark|light|auto)/);
    return m ? m[1] : 'dark';
}

document.addEventListener('DOMContentLoaded', () => {
    const form = document.querySelector('form.theme-toggle');
    if (!form) return;
    form.addEventListener('submit', (e) => {
        e.preventDefault();
        const next = themeCycle(currentTheme());
        document.documentElement.className = `theme-${next}`;
        document.cookie = `theme=${next}; path=/; max-age=31536000; SameSite=Lax`;
        const hidden = form.querySelector('input[name="theme"]');
        if (hidden) hidden.value = themeCycle(next);
    });
});

// ─── Copy: code blocks (home page quick-start) ───────────────────────────────

// copyToClipboard copies the text content of the element with the given id,
// updating the button that triggered the copy (never a global lookup, so
// multiple data-copy-paste buttons on one page never cross-update).
// Called via data-copy-paste attribute buttons.
function copyToClipboard(elementId, btn) {
    const el = document.getElementById(elementId || 'paste-code');
    if (!el) return;
    navigator.clipboard.writeText(el.textContent).then(() => {
        if (btn) {
            const orig = btn.textContent;
            btn.textContent = t('copied');
            setTimeout(() => { btn.textContent = orig; }, 2000);
        }
    }).catch(() => {
        if (btn) {
            btn.textContent = 'Failed';
            setTimeout(() => { btn.textContent = t('copy'); }, 2000);
        }
    });
}

// ─── Copy to clipboard (data-copy attribute) ─────────────────────────────────

document.addEventListener('DOMContentLoaded', () => {
    document.querySelectorAll('[data-copy]').forEach(btn => {
        btn.addEventListener('click', () => {
            const text = btn.getAttribute('data-copy');
            if (!text) return;

            navigator.clipboard.writeText(text).then(() => {
                // Icon-only buttons (footer/preferences/help) mark their icon span
                // aria-hidden; labeled buttons carry plain visible text in .copy-text.
                // Both cases save their original content and restore it on revert,
                // instead of assuming a fixed pre-copy label.
                const icon = btn.querySelector('.copy-icon');
                const iconOnly = btn.querySelector('.copy-text[aria-hidden]');
                const label = btn.querySelector('.copy-text:not([aria-hidden])');
                const copiedLabel = btn.getAttribute('data-copied-label') || t('copied');
                const restore = [];

                if (icon) {
                    restore.push([icon, icon.textContent]);
                    icon.textContent = '✓';
                }
                if (iconOnly) {
                    restore.push([iconOnly, iconOnly.textContent]);
                    iconOnly.textContent = '✓';
                }
                if (label) {
                    restore.push([label, label.textContent]);
                    label.textContent = copiedLabel;
                }
                if (!icon && !iconOnly && !label) {
                    restore.push([btn, btn.textContent]);
                    btn.textContent = '✓ ' + copiedLabel;
                }

                btn.classList.add('copied');

                setTimeout(() => {
                    restore.forEach(([el, val]) => { el.textContent = val; });
                    btn.classList.remove('copied');
                }, 2000);
            }).catch(() => {
                // Fallback: select the adjacent code element
                const block = btn.closest('.code-block');
                const code = block && block.querySelector('.code-content');
                if (code) {
                    const range = document.createRange();
                    range.selectNodeContents(code);
                    const sel = window.getSelection();
                    sel.removeAllRanges();
                    sel.addRange(range);
                }
            });
        });
    });
});

// ─── data-copy-paste buttons ─────────────────────────────────────────────────

document.addEventListener('DOMContentLoaded', () => {
    document.querySelectorAll('[data-copy-paste]').forEach(btn => {
        btn.addEventListener('click', () => {
            const targetId = btn.getAttribute('data-copy-paste');
            copyToClipboard(targetId, btn);
        });
    });
});

// ─── Site Banner dismissal (AI.md 22589-22605) ───────────────────────────────

// No-reload enhancement only — the POST /announcements/dismiss form fallback
// always works without this. Dismissal is keyed on the announcement id, so
// changing the id in server.yml resets dismissals for everyone.
document.addEventListener('DOMContentLoaded', () => {
    document.querySelectorAll('.site-banner .site-banner-dismiss').forEach((form) => {
        form.addEventListener('submit', (e) => {
            e.preventDefault();
            const id = form.elements.id.value;
            const match = document.cookie.match(/(?:^|;\s*)dismissed_announcements=([^;]*)/);
            const ids = match ? decodeURIComponent(match[1]).split(',').filter(Boolean) : [];
            if (!ids.includes(id)) ids.push(id);
            const value = encodeURIComponent(ids.join(','));
            const secure = location.protocol === 'https:' ? '; Secure' : '';
            document.cookie = `dismissed_announcements=${value}; path=/; max-age=${60 * 60 * 24 * 365}; SameSite=Lax${secure}`;
            const banner = form.closest('.site-banner');
            if (banner) banner.remove();
        });
    });
});

// ─── QR download ─────────────────────────────────────────────────────────────

document.addEventListener('DOMContentLoaded', () => {
    const dlBtn = document.querySelector('[data-qr-download]');
    if (!dlBtn) return;
    dlBtn.addEventListener('click', () => {
        const src = dlBtn.getAttribute('data-qr-download');
        const name = dlBtn.getAttribute('data-qr-name') || 'paste-qr.png';
        const a = document.createElement('a');
        a.href = src;
        a.download = name;
        a.click();
    });
});

// ─── Submit button loading state ─────────────────────────────────────────────

// Per PART 16: disable submit on click, show loading text, re-enable on
// response. Only buttons opting in via data-action="submit-loading" are
// bound; the server renders the loading text via data-loading-text.
document.addEventListener('DOMContentLoaded', () => {
    document.querySelectorAll('[data-action="submit-loading"]').forEach(btn => {
        const form = btn.closest('form');
        if (!form) return;

        form.addEventListener('submit', () => {
            if (btn.disabled) return;

            btn.style.minWidth = btn.offsetWidth + 'px';
            btn.disabled = true;
            btn.textContent = btn.dataset.loadingText || btn.textContent;
        });
    });
});

// ─── API helper ───────────────────────────────────────────────────────────────

async function fetchAPI(endpoint, options) {
    const opts = options || {};
    const defaults = {
        headers: {
            'Content-Type': 'application/json',
            'Accept': 'application/json',
        },
    };
    const response = await fetch(endpoint, Object.assign({}, defaults, opts));
    if (!response.ok) {
        const err = await response.json().catch(() => ({}));
        throw new Error(err.detail || err.error || t('api_error'));
    }
    return response.json();
}

// ─── Cookie consent & tracking (merged from consent.js) ───────────────────────

// Pastebin — Cookie Consent & Tracking (PART 31)
// Progressive enhancement only. The banner is server-rendered and its forms POST
// to /server/consent; consent lives in the server-readable cookie_consent cookie
// (never localStorage). Analytics scripts are shipped inert inside
// <template id="pb-tracking-snippet"> and only activated here once the
// visitor's analytics consent is known.
//
// The consent config itself is embedded by the server as an inert JSON
// script block (id="pb-consent-data") rather than an executable inline
// <script>, per project rules (no inline JS).

(function () {
    'use strict';

    // loadConsentConfig parses the server-rendered JSON config block.
    // Returns {} when absent or unparsable, matching prior window.PB_CONSENT
    // fallback behavior.
    function loadConsentConfig() {
        var el = document.getElementById('pb-consent-data');
        if (!el) return {};
        try {
            return JSON.parse(el.textContent || '{}');
        } catch (e) {
            return {};
        }
    }

    var cfg = loadConsentConfig();

    // ─── Consent state ───────────────────────────────────────────────────────

    // readConsent returns the ConsentState from the cookie_consent cookie, or
    // null when absent/corrupt.
    function readConsent() {
        var m = document.cookie.match(/(?:^|;\s*)cookie_consent=([^;]+)/);
        if (!m) {
            return null;
        }
        try {
            var c = JSON.parse(decodeURIComponent(m[1]));
            return (typeof c === 'object' && c !== null) ? c : null;
        } catch (e) {
            return null;
        }
    }

    // writeConsent persists the ConsentState in the cookie_consent cookie and
    // applies it immediately.
    function writeConsent(consent) {
        var cookieState = {
            essential: true,
            preferences: !!consent.preferences,
            analytics: !!consent.analytics,
            timestamp: consent.timestamp || nowSeconds()
        };
        document.cookie = 'cookie_consent=' + encodeURIComponent(JSON.stringify(cookieState)) +
            '; path=/; max-age=31536000; samesite=lax';
        applyConsent(consent);
    }

    // nowSeconds returns the current UNIX time in seconds.
    function nowSeconds() {
        return Math.floor(Date.now() / 1000);
    }

    // ─── Tracking activation ─────────────────────────────────────────────────

    var trackingActivated = false;

    // activateTracking clones the inert tracking snippet into <head>, causing its
    // <script> elements to execute. Cloned template content does not run, so each
    // script node is recreated to trigger loading. Runs at most once.
    function activateTracking() {
        if (trackingActivated) {
            return;
        }
        var tpl = document.getElementById('pb-tracking-snippet');
        if (!tpl || !tpl.content) {
            return;
        }
        trackingActivated = true;
        var nodes = tpl.content.childNodes;
        for (var i = 0; i < nodes.length; i++) {
            var node = nodes[i];
            if (node.nodeType === 1 && node.tagName === 'SCRIPT') {
                var s = document.createElement('script');
                for (var a = 0; a < node.attributes.length; a++) {
                    var attr = node.attributes[a];
                    s.setAttribute(attr.name, attr.value);
                }
                s.text = node.textContent;
                document.head.appendChild(s);
            } else if (node.nodeType === 1) {
                document.head.appendChild(node.cloneNode(true));
            }
        }
    }

    // applyConsent enacts a ConsentState: preference cookie + analytics loading.
    function applyConsent(consent) {
        if (consent.preferences) {
            document.cookie = 'preferencesEnabled=true; path=/; max-age=31536000; samesite=lax';
        }
        if (consent.analytics && cfg.analyticsConfigured) {
            activateTracking();
        }
    }

    // ─── CCPA "Do Not Sell" ──────────────────────────────────────────────────

    // applyCCPAOptOut records the opt-out cookie and blocks data sharing.
    function applyCCPAOptOut() {
        document.cookie = 'ccpa_opt_out=true; path=/; max-age=31536000; samesite=lax';
    }

    // ccpaDoNotSell opts the visitor out of data sales and declines non-essential
    // cookies (CCPA right-to-opt-out).
    function ccpaDoNotSell() {
        applyCCPAOptOut();
        writeConsent({
            essential: true,
            preferences: false,
            analytics: false,
            ccpaOptOut: true,
            timestamp: nowSeconds()
        });
        hideBanner();
    }

    // ─── DOM helpers ─────────────────────────────────────────────────────────

    function el(tag, attrs, text) {
        var node = document.createElement(tag);
        if (attrs) {
            Object.keys(attrs).forEach(function (k) {
                node.setAttribute(k, attrs[k]);
            });
        }
        if (text != null) {
            node.textContent = text;
        }
        return node;
    }

    // ─── Consent banner ──────────────────────────────────────────────────────

    function hideBanner() {
        var banner = document.getElementById('cookie-consent');
        if (banner && banner.parentNode) {
            banner.parentNode.removeChild(banner);
        }
    }

    // bindBanner intercepts the server-rendered banner forms so a JS-capable
    // browser skips the reload; without JS the forms POST to /server/consent.
    function bindBanner() {
        var forms = document.querySelectorAll('#cookie-consent form');
        for (var i = 0; i < forms.length; i++) {
            forms[i].addEventListener('submit', function (ev) {
                ev.preventDefault();
                var accepted = ev.target.elements.choice.value === 'accept';
                writeConsent({
                    essential: true,
                    preferences: accepted,
                    analytics: accepted && !!cfg.analyticsConfigured,
                    timestamp: nowSeconds()
                });
                hideBanner();
            });
        }
    }

    // ─── Preferences modal ───────────────────────────────────────────────────

    var modalEl = null;

    function closePreferences() {
        if (modalEl && modalEl.parentNode) {
            modalEl.parentNode.removeChild(modalEl);
            modalEl = null;
        }
    }

    // categoryRow builds one cookie-category toggle row.
    function categoryRow(id, label, description, checked, locked) {
        var row = el('div', { class: 'cookie-category' });
        var head = el('label', { class: 'cookie-category-head', for: id });
        var input = el('input', { type: 'checkbox', id: id });
        if (checked) {
            input.checked = true;
        }
        if (locked) {
            input.checked = true;
            input.disabled = true;
        }
        head.appendChild(input);
        head.appendChild(el('span', { class: 'cookie-category-label' }, label));
        row.appendChild(head);
        if (description) {
            row.appendChild(el('p', { class: 'cookie-category-desc' }, description));
        }
        return row;
    }

    // openPreferences renders the granular preferences modal.
    function openPreferences() {
        if (modalEl) {
            return;
        }
        var current = readConsent() || {
            preferences: !!cfg.defaultPreferences,
            analytics: !!cfg.defaultAnalytics
        };
        var desc = cfg.descriptions || {};

        var overlay = el('div', {
            id: 'cookie-preferences-modal',
            class: 'cookie-modal-overlay',
            role: 'dialog',
            'aria-modal': 'true',
            'aria-label': cfg.preferencesText || 'Manage Preferences'
        });
        var dialog = el('div', { class: 'cookie-modal' });
        dialog.appendChild(el('h2', { class: 'cookie-modal-title' }, cfg.preferencesText || 'Manage Preferences'));

        dialog.appendChild(categoryRow('pref-essential', 'Essential',
            desc.essential || 'Required for the site to function.', true, true));
        dialog.appendChild(categoryRow('pref-preferences', 'Preferences',
            desc.preferences || 'Remember theme and language.', !!current.preferences, false));

        if (cfg.analyticsConfigured) {
            dialog.appendChild(categoryRow('pref-analytics', 'Analytics',
                desc.analytics || 'Anonymous usage statistics.', !!current.analytics, false));
        }

        var actions = el('div', { class: 'cookie-modal-actions' });
        var cancelBtn = el('button', { type: 'button', class: 'btn-decline' }, 'Cancel');
        cancelBtn.addEventListener('click', closePreferences);
        var saveBtn = el('button', { type: 'button', class: 'btn-accept' }, 'Save');
        saveBtn.addEventListener('click', savePreferences);
        actions.appendChild(cancelBtn);
        actions.appendChild(saveBtn);
        dialog.appendChild(actions);

        overlay.appendChild(dialog);
        overlay.addEventListener('click', function (ev) {
            if (ev.target === overlay) {
                closePreferences();
            }
        });
        document.addEventListener('keydown', escClose);
        modalEl = overlay;
        document.body.appendChild(overlay);
        saveBtn.focus();
    }

    function escClose(ev) {
        if (ev.key === 'Escape') {
            closePreferences();
            document.removeEventListener('keydown', escClose);
        }
    }

    // savePreferences persists the granular selections from the modal.
    function savePreferences() {
        var analyticsInput = document.getElementById('pref-analytics');
        var prefInput = document.getElementById('pref-preferences');
        writeConsent({
            essential: true,
            preferences: prefInput ? prefInput.checked : false,
            analytics: analyticsInput ? analyticsInput.checked : false,
            timestamp: nowSeconds()
        });
        closePreferences();
        hideBanner();
    }

    // ─── Init ────────────────────────────────────────────────────────────────

    function init() {
        // Honor a prior CCPA opt-out immediately.
        if (/(?:^|;\s*)ccpa_opt_out=true/.test(document.cookie)) {
            applyCCPAOptOut();
        }

        var consent = readConsent();
        if (consent) {
            applyConsent(consent);
        } else {
            bindBanner();
        }

        // Expose the preferences modal so any page (e.g. /server/privacy) can
        // reopen it via a "Manage Preferences" control.
        window.pbShowCookiePreferences = openPreferences;

        // Wire any server-rendered "Manage Preferences" trigger.
        var triggers = document.querySelectorAll('[data-cookie-preferences]');
        for (var i = 0; i < triggers.length; i++) {
            triggers[i].addEventListener('click', function (ev) {
                ev.preventDefault();
                openPreferences();
            });
        }
    }

    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', init);
    } else {
        init();
    }
})();

// ─── Create page enhancements (merged from create.js) ─────────────────────────

// Pastebin — Create page enhancements
// Progressive enhancement only — the form works as a plain POST without any
// of this. These handlers add convenience for JS-capable browsers.
(function () {
    // Canonical browser-storage name shared with the owner_token cookie
    // (AI.md PART 11 "Naming"; suffix recorded in IDEA.md).
    const TOKEN_KEY = 'pastebin_owner_token_rU3uW5Ze';

    // Persist a freshly issued owner token (rendered server-side after a
    // successful create) so the next paste can reuse it.
    const createdToken = document.getElementById('created-token');
    if (createdToken && createdToken.textContent.trim()) {
        localStorage.setItem(TOKEN_KEY, createdToken.textContent.trim());
    }

    // Pre-fill the token field from a previously saved token.
    const tokenField = document.getElementById('owner_token');
    if (tokenField && !tokenField.value) {
        const saved = localStorage.getItem(TOKEN_KEY) || '';
        if (saved) {
            tokenField.value = saved;
        }
    }

    // Copy-target buttons (server-rendered result block on create.tmpl, and
    // the multi-line code-block-multi quick-start buttons on home.tmpl).
    // querySelectorAll so every button on the page is bound, not just the
    // first — a page can render more than one (see home.tmpl quick-start).
    if (navigator.clipboard) {
        document.querySelectorAll('[data-copy-target]').forEach(function (btn) {
            btn.addEventListener('click', function () {
                const el = document.getElementById(this.dataset.copyTarget);
                if (!el) return;
                navigator.clipboard.writeText(el.textContent.trim()).then(() => {
                    const icon = this.querySelector('.copy-icon');
                    const label = this.querySelector('.copy-text');
                    const copiedLabel = this.getAttribute('data-copied-label') || t('copied');
                    if (icon || label) {
                        const restoreIcon = icon ? icon.textContent : null;
                        const restoreLabel = label ? label.textContent : null;
                        if (icon) icon.textContent = '✓';
                        if (label) label.textContent = copiedLabel;
                        this.classList.add('copied');
                        setTimeout(() => {
                            if (icon) icon.textContent = restoreIcon;
                            if (label) label.textContent = restoreLabel;
                            this.classList.remove('copied');
                        }, 2000);
                    } else {
                        const original = this.textContent;
                        this.textContent = copiedLabel;
                        setTimeout(() => { this.textContent = original; }, 2000);
                    }
                });
            });
        });
    }

    // Tab key inserts spaces in the textarea instead of moving focus.
    const content = document.getElementById('content');
    if (content) {
        content.addEventListener('keydown', function (e) {
            if (e.key === 'Tab') {
                e.preventDefault();
                const start = this.selectionStart;
                const end = this.selectionEnd;
                this.value = this.value.substring(0, start) + '    ' + this.value.substring(end);
                this.selectionStart = this.selectionEnd = start + 4;
            }
        });
        content.focus();
    }

    // Clear local token copies — keeps UI preferences (theme, lang) intact.
    // Per AI.md "API Token Storage in PWA": the owner_token cookie is
    // HttpOnly and expires via its own Max-Age; this only clears the
    // optional localStorage convenience copy and any cached
    // private/token-scoped IndexedDB data.
    async function revokeLocalToken() {
        localStorage.removeItem(TOKEN_KEY);

        if (window.indexedDB && indexedDB.databases) {
            const databases = await indexedDB.databases();
            for (const db of databases) {
                if (db.name && (db.name.includes('private') || db.name.includes('token'))) {
                    indexedDB.deleteDatabase(db.name);
                }
            }
        }

        window.location.reload();
    }

    const forgetTokenBtn = document.querySelector('[data-action="forget-token"]');
    if (forgetTokenBtn) {
        forgetTokenBtn.addEventListener('click', revokeLocalToken);
    }
})();

// ─── Create form: link-mode content hint (progressive enhancement) ────────────

// There is no is_link checkbox — the server auto-detects a link paste from
// content shape alone (entire trimmed content is exactly one http:// or
// https:// URL, nothing else). This listener only mirrors that same rule
// client-side to swap the content label and hide the (meaningless) language
// field as a visual hint; it changes nothing about validation or submission,
// which the server always re-checks authoritatively.
document.addEventListener('DOMContentLoaded', () => {
    const content = document.getElementById('content');
    const contentLabel = document.getElementById('content-label');
    const languageGroup = document.getElementById('language-group');
    if (!content || !contentLabel) return;

    const textLabel = contentLabel.textContent;
    const linkLabel = t('content_link_label') || textLabel;
    const singleURLPattern = /^https?:\/\/\S+$/;

    const updateLinkHint = () => {
        const trimmed = content.value.trim();
        const isLink = singleURLPattern.test(trimmed);
        contentLabel.textContent = isLink ? linkLabel : textLabel;
        if (languageGroup) {
            languageGroup.style.display = isLink ? 'none' : '';
        }
    };

    content.addEventListener('input', updateLinkHint);
    updateLinkHint();
});

// ─── Remove page enhancements (merged from remove.js) ─────────────────────────

// Pastebin — Remove page enhancements
// Progressive enhancement only — the form works as a plain POST without
// this. Pre-fill the delete-token field from the owner token saved at
// creation time (canonical localStorage key: pastebin_owner_token_rU3uW5Ze,
// shared with the owner_token cookie name, per IDEA.md and AI.md:11801).
// If the page is showing an error, the saved token was invalid — clear it
// so the user is not silently retrying with a bad token on the next attempt.
// The error state is passed via the data-token-error attribute (server-
// rendered, no inline JS needed to read it).
(function () {
    const TOKEN_KEY = 'pastebin_owner_token_rU3uW5Ze';
    const tokenField = document.getElementById('token');
    if (!tokenField) return;

    if (tokenField.dataset.tokenError === 'true') {
        // Server returned an error: the stored token was rejected. Rotate it.
        localStorage.removeItem(TOKEN_KEY);
        return;
    }

    if (!tokenField.value) {
        const saved = localStorage.getItem(TOKEN_KEY) || '';
        if (saved) {
            tokenField.value = saved;
        }
    }
})();

// ─── Offline page reload button ───────────────────────────────────────────────
// Progressive enhancement only. Replaces the inline onclick handler on the
// offline page's "Try Again" button (no inline handlers per PART 16).
(function () {
    const btn = document.querySelector('[data-action="reload"]');
    if (btn) {
        btn.addEventListener('click', function () {
            window.location.reload();
        });
    }
})();

// ─── Swagger UI viewer ─────────────────────────────────────────────────────
// Presence-gated: no-ops on every page except /server/docs/swagger. Moved out
// of src/swagger/swagger.go's HTML string builder — the CSP ships
// script-src 'self' only (no inline scripts) and PART 16 requires all JS to
// live in this single file.
(function () {
    const app = document.getElementById('app');
    if (!app || !app.classList.contains('swagger-ui')) {
        return;
    }
    const specURL = app.dataset.specUrl;

    // Theming is server-rendered (class="theme-{mode}" on <html>, from the
    // project-wide `theme` cookie) and the toggle is a no-JS POST form to
    // /theme — see src/swagger/swagger.go renderUI. No client-side theme
    // state is kept here.

    function esc(s) {
        return String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
    }

    function statusClass(code) {
        if (code >= 200 && code < 300) return 'status-2xx';
        if (code >= 400 && code < 500) return 'status-4xx';
        return 'status-5xx';
    }

    function schemaSnippet(schema) {
        if (!schema) return '';
        try {
            return JSON.stringify(schema, null, 2);
        } catch (e) {
            return '';
        }
    }

    function renderParam(p) {
        return '<tr>' +
            '<td><span class="param-name">' + esc(p.name) + '</span> <span class="param-in">(' + esc(p.in) + ')</span></td>' +
            '<td>' + (p.required ? '<span class="param-req">required</span>' : '<span class="param-optional">optional</span>') + '</td>' +
            '<td>' + esc(p.description || '') + '</td>' +
            '<td><code class="param-type">' + esc((p.schema && p.schema.type) || '') + '</code></td>' +
            '</tr>';
    }

    function renderResponse(code, resp) {
        const sc = statusClass(parseInt(code, 10));
        let schema = '';
        if (resp.content) {
            const ct = Object.keys(resp.content)[0];
            if (ct && resp.content[ct] && resp.content[ct].schema) {
                schema = '<div class="body-schema">' + esc(schemaSnippet(resp.content[ct].schema)) + '</div>';
            }
        }
        return '<div class="response-block">' +
            '<span class="status-code ' + sc + '">' + esc(code) + '</span>' +
            '<div><div>' + esc(resp.description || '') + '</div>' + schema + '</div>' +
            '</div>';
    }

    function renderOp(method, path, op, idx) {
        const id = 'op-' + idx;
        const params = (op.parameters || []).map(renderParam).join('');
        const paramSection = params ? '<div class="section-title">Parameters</div><table class="param-table"><thead><tr><th>Name</th><th>Required</th><th>Description</th><th>Type</th></tr></thead><tbody>' + params + '</tbody></table>' : '';

        let bodySection = '';
        if (op.requestBody && op.requestBody.content) {
            const ct = Object.keys(op.requestBody.content)[0];
            const schema = ct && op.requestBody.content[ct] && op.requestBody.content[ct].schema ? op.requestBody.content[ct].schema : null;
            bodySection = '<div class="section-title">Request Body</div>';
            if (op.requestBody.description) {
                bodySection += '<p class="body-desc">' + esc(op.requestBody.description) + '</p>';
            }
            if (schema) {
                bodySection += '<div class="body-schema">' + esc(schemaSnippet(schema)) + '</div>';
            }
        }

        const responses = op.responses || {};
        const respSection = '<div class="section-title">Responses</div>' +
            Object.keys(responses).sort().map(function (c) { return renderResponse(c, responses[c]); }).join('');

        return '<div class="opblock" id="' + id + '">' +
            '<div class="opblock-summary" data-action="toggle-opblock" data-target="' + id + '">' +
            '<span class="method method-' + method.toLowerCase() + '">' + esc(method) + '</span>' +
            '<span class="opblock-path">' + esc(path) + '</span>' +
            '<span class="opblock-summary-desc">' + esc(op.summary || '') + '</span>' +
            '</div>' +
            '<div class="opblock-body">' +
            (op.description ? '<p class="op-desc">' + esc(op.description) + '</p>' : '') +
            paramSection + bodySection + respSection +
            '</div></div>';
    }

    // Event delegation for opblock expand/collapse — avoids per-row
    // addEventListener calls and inline onclick attributes (PART 16).
    app.addEventListener('click', function (e) {
        const summary = e.target.closest('[data-action="toggle-opblock"]');
        if (!summary) return;
        const block = document.getElementById(summary.dataset.target);
        if (block) {
            block.classList.toggle('open');
        }
    });

    function render(spec) {
        const tags = {};
        const paths = spec.paths || {};
        Object.keys(paths).forEach(function (path) {
            const item = paths[path];
            Object.keys(item).forEach(function (method) {
                const op = item[method];
                const tag = (op.tags && op.tags[0]) || 'other';
                if (!tags[tag]) tags[tag] = [];
                tags[tag].push({ method: method.toUpperCase(), path: path, op: op });
            });
        });

        const info = spec.info || {};
        const servers = (spec.servers || []).map(function (s) {
            return '<code>' + esc(s.url) + '</code>' + (s.description ? ' <span class="server-desc">— ' + esc(s.description) + '</span>' : '');
        }).join(', ');

        let html = '<div class="info-block"><strong>' + esc(info.title || '') + '</strong>' +
            (info.description ? '<p>' + esc(info.description) + '</p>' : '') + '</div>';
        if (servers) html += '<div class="servers"><strong>Server:</strong> ' + servers + '</div>';

        let idx = 0;
        Object.keys(tags).sort().forEach(function (tag) {
            html += '<div class="tag-section"><div class="tag-label">' + esc(tag) + '</div>';
            tags[tag].forEach(function (item) {
                html += renderOp(item.method, item.path, item.op, idx++);
            });
            html += '</div>';
        });

        html += '<footer>OpenAPI 3.0.3 · <a href="' + esc(specURL) + '">JSON spec</a></footer>';
        app.innerHTML = html;
    }

    fetch(specURL)
        .then(function (r) { return r.json(); })
        .then(render)
        .catch(function (e) {
            app.innerHTML = '<p class="error-message">Failed to load spec: ' + esc(String(e)) + '</p>';
        });
})();

// ─── GraphiQL viewer ────────────────────────────────────────────────────────
// Presence-gated: no-ops on every page except /server/docs/graphql. Moved out
// of src/graphql/graphql.go's HTML string builder for the same CSP/PART 16
// reasons as the Swagger UI viewer above.
(function () {
    const container = document.getElementById('graphiql');
    if (!container) {
        return;
    }
    const queryField = document.getElementById('query');
    const varsField = document.getElementById('vars');
    const result = document.getElementById('result');
    const apiURL = container.dataset.apiUrl || '/api/graphql';

    // Theming is server-rendered (class="theme-{mode}" on <html>, from the
    // project-wide `theme` cookie) and the toggle is a no-JS POST form to
    // /theme — see src/graphql/graphql.go renderUI. No client-side theme
    // state is kept here.

    function showResult(data, isError) {
        result.textContent = JSON.stringify(data, null, 2);
        result.className = 'result-window' + (isError ? ' error' : '');
    }

    function runQuery() {
        const query = queryField.value;
        const varsRaw = varsField.value.trim();
        let variables = {};
        if (varsRaw) {
            try {
                variables = JSON.parse(varsRaw);
            } catch (e) {
                showResult({ errors: [{ message: 'Invalid variables JSON: ' + e.message }] }, true);
                return;
            }
        }
        result.textContent = 'Running…';
        result.className = 'result-window';
        fetch(apiURL, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ query: query, variables: variables }),
        })
            .then(function (r) { return r.json(); })
            .then(function (data) { showResult(data, !!data.errors); })
            .catch(function (e) { showResult({ errors: [{ message: String(e) }] }, true); });
    }

    const runBtn = document.querySelector('[data-action="run-graphql-query"]');
    if (runBtn) {
        runBtn.addEventListener('click', runQuery);
    }

    document.addEventListener('keydown', function (e) {
        if ((e.ctrlKey || e.metaKey) && e.key === 'Enter') {
            e.preventDefault();
            runQuery();
        }
    });
})();

// ─── Form validation (AI.md PART 16, Form Validation) ──────────────────────
// Progressive enhancement over native HTML5 validation (required/pattern/
// type=). Every field's own required/pattern/type attributes remain the
// single source of truth for validity — this only adds inline, accessible
// error messaging: validate on blur (not while typing), clear the error the
// moment the field becomes valid again, mirror the native "invalid" event
// fired on submit, and focus the first invalid field on submit.
function initFormValidation() {
    function showFieldError(el, error) {
        el.setAttribute('aria-invalid', 'true');
        error.textContent = el.validationMessage;
        error.hidden = false;
    }

    function clearFieldError(el, error) {
        el.setAttribute('aria-invalid', 'false');
        error.textContent = '';
        error.hidden = true;
    }

    var fields = document.querySelectorAll('.form-group input, .form-group select, .form-group textarea');
    fields.forEach(function (el) {
        var error = document.getElementById(el.id + '-error');
        if (!error) {
            return;
        }

        // Validate on blur — never while the user is still typing.
        el.addEventListener('blur', function () {
            if (el.checkValidity()) {
                clearFieldError(el, error);
            } else {
                showFieldError(el, error);
            }
        });

        // Clear on fix — only re-check once an error has already been shown;
        // don't validate a field's first pass while the user is typing.
        el.addEventListener('input', function () {
            if (el.getAttribute('aria-invalid') === 'true' && el.checkValidity()) {
                clearFieldError(el, error);
            }
        });

        // Native "invalid" event fires on submit attempt.
        el.addEventListener('invalid', function () {
            showFieldError(el, error);
        });
    });

    // Focus the first invalid field on submit (native validation already
    // blocks submission — this just improves where focus lands).
    document.querySelectorAll('form').forEach(function (form) {
        form.addEventListener('submit', function () {
            if (!form.checkValidity()) {
                var firstInvalid = form.querySelector(':invalid');
                if (firstInvalid) {
                    firstInvalid.focus();
                }
            }
        });
    });
}

document.addEventListener('DOMContentLoaded', initFormValidation);
