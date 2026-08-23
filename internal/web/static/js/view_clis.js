// xusage dashboard — CLIs view (the calling-agents dimension).
//
// The backend "provider" field names the coding agent that produced an entry
// (claude/codex/opencode/kimi/…). This view shows, per SPEC §13.1/§13.4:
//   1. one card per detected CLI from /api/pulse (letter-mark avatar, name,
//      fileCount, detected dot), joined with usage from /api/summary;
//   2. an aggregate table from /api/summary.byProvider (cost desc, share
//      bars, tokens, messages, sessions); clicking a row applies the CLI as
//      the global provider filter and jumps to the Series view.
// Built on the frozen XU API (§9.1). Bridges to the redesign additions
// (XU.pill, XU.fmt.big, XU.onPulse) degrade gracefully when the app-core
// rewrite is not loaded yet: fmt.tokens stands in for fmt.big, a local chip
// stands in for XU.pill, and a one-shot /api/pulse fetch stands in for the
// pulse subscription.
(function () {
  'use strict';

  if (typeof XU === 'undefined') {
    return; // Partial deploy without app.js — do not throw on load.
  }

  var VIEW = 'clis';
  var FIELD = 'provider'; // XU.state key + query parameter driven by this view
  var FIELD_LABEL = 'provider';
  var PULSE_ENDPOINT = '/api/pulse';
  var SUMMARY_ENDPOINT = '/api/summary';
  // Only the injected <style> tag carries an id. The mount element's id must
  // stay untouched: the redesigned router resolves #view by id after mount,
  // so view content lives in a class-scoped wrapper (see [views-attrib] NOTE).
  var VIEW_CLASS = 'xu-view-clis';
  var STYLE_ID = 'xu-style-view-clis';

  // View-scoped styling for pieces the shared stylesheet does not guarantee
  // (card grid, letter marks, share bars, fallback pill chip). Scoped under
  // the wrapper class and removed on unmount so nothing leaks globally. The
  // fallback chip styles key on .xu-pillfb, which only the local fallback
  // emits — once XU.pill exists, the design system owns .pill styling.
  // Colors follow the §13.0 GSAP tokens; shared vars are referenced
  // with fresh-value fallbacks so the view renders even pre-shell.
  var P = '.' + VIEW_CLASS;
  var MONO = 'var(--mono,ui-monospace,Consolas,monospace)';
  var CSS = [
    P + ' .xu-eyebrow{font-size:17px;font-weight:400;color:var(--text,#fffce1);' +
        'letter-spacing:.01em;line-height:1.1;margin:0 0 14px}',
    P + ' .xu-toolbar{display:flex;align-items:center;gap:10px;margin:0 0 12px}',
    P + ' .xu-label{font-size:11px;font-weight:800;letter-spacing:.08em;' +
        'text-transform:uppercase;color:var(--muted,#7c7c6f)}',
    P + ' .xu-grow{flex:1 1 auto}',
    P + ' .xu-count{font-size:12px;color:var(--muted,#7c7c6f);font-family:' + MONO + ';' +
        'font-variant-numeric:tabular-nums}',
    P + ' .xu-xu-export{appearance:none;background:none;border:1px solid var(--text,#fffce1);' +
        'border-radius:100px;color:var(--text,#fffce1);padding:5px 14px;font-size:12px;' +
        'font-weight:600;cursor:pointer;font-family:inherit;line-height:1.2}',
    P + ' .xu-xu-export:hover{opacity:.8}',
    P + ' .xu-xu-export:focus-visible{outline:1px solid var(--accent,#0ae448);outline-offset:1px}',
    P + ' .xu-chip{display:inline-flex;align-items:center;gap:6px;padding:4px 12px;' +
        'border:1px solid var(--border,#1e1e1e);border-radius:100px;background:none;' +
        'color:var(--text,#fffce1);font-size:12px;font-weight:600}',
    P + ' .xu-chip .xu-cv{max-width:220px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}',
    P + ' .xu-chip button{appearance:none;border:0;background:none;color:inherit;cursor:pointer;' +
        'font-size:12px;line-height:1;padding:2px 6px;border-radius:100px}',
    P + ' .xu-chip button:hover{opacity:.8}',
    P + ' .xu-grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(230px,1fr));' +
        'gap:8px;margin:0 0 16px}',
    P + ' .xu-cli{display:flex;align-items:center;gap:10px;padding:10px 12px}',
    P + ' .xu-cli.xu-click{cursor:pointer}',
    P + ' .xu-cli.xu-click:hover{border-color:var(--border,#1e1e1e)}',
    P + ' .xu-cli.xu-click:focus-visible{outline:1px solid var(--accent,#0ae448);outline-offset:-2px}',
    P + ' .xu-mark{flex:0 0 auto;width:28px;height:28px;border-radius:6px;' +
        'background:var(--panel,#111111);border:1px solid var(--border,#1e1e1e);' +
        'display:flex;align-items:center;justify-content:center;' +
        'font-size:13px;font-weight:800;text-transform:uppercase}',
    P + ' .xu-cmeta{min-width:0}',
    P + ' .xu-cnamerow{display:flex;align-items:center;gap:6px}',
    P + ' .xu-cname{font-size:13px;font-weight:700;color:var(--accent,#0ae448);overflow:hidden;' +
        'text-overflow:ellipsis;white-space:nowrap}',
    P + ' .xu-cnamerow .dot{margin-left:auto}',
    P + ' .xu-cfiles{font-size:12px;color:var(--muted,#7c7c6f);font-family:' + MONO + ';' +
        'font-variant-numeric:tabular-nums}',
    P + ' .xu-cusage{margin-left:auto;text-align:right;flex:0 0 auto}',
    P + ' .xu-ccost{font-size:13px;color:var(--text,#fffce1);font-family:' + MONO + ';' +
        'font-variant-numeric:tabular-nums}',
    P + ' .xu-cshare{font-size:12px;color:var(--muted,#7c7c6f);font-family:' + MONO + ';' +
        'font-variant-numeric:tabular-nums}',
    P + ' table.table{width:100%;border-collapse:collapse}',
    P + ' th{white-space:nowrap}',
    P + ' th.xu-r,td.xu-r{text-align:right;font-variant-numeric:tabular-nums}',
    P + ' tbody tr.xu-row{cursor:pointer}',
    P + ' tbody tr.xu-row:focus-visible{outline:1px solid var(--accent,#0ae448);outline-offset:-2px}',
    P + ' .xu-agentcell{display:flex;align-items:center;gap:8px;min-width:0}',
    P + ' .xu-sharecell{display:flex;align-items:center;gap:8px;justify-content:flex-end}',
    P + ' .xu-bar{width:72px;height:3px;border-radius:2px;background:var(--panel,#111111);overflow:hidden}',
    P + ' .xu-bar i{display:block;height:100%;background:rgba(10,224,72,0.30)}',
    P + ' .xu-pillfb{display:inline-flex;align-items:center;padding:3px 8px;border-radius:100px;' +
        'font-size:11px;line-height:14px;font-family:' + MONO + ';font-weight:700;' +
        'background:var(--bg,#0a0a0a);border:1px solid var(--border,#1e1e1e);' +
        'color:var(--accent,#0ae448)}'
  ].join('\n');

  // Per-mount state; exactly one instance lives at a time (router unmounts first).
  var S = {
    alive: false, tick: 0, unsub: null, offPulse: null,
    pulse: null, pulseLoading: false, pulseError: '',
    rows: [], qs: null, loading: false, error: '',
    el: null, root: null, count: null, chips: null, cards: null, card: null
  };

  XU.registerView(VIEW, { mount: mount, unmount: unmount });

  // ---------------------------------------------------------------- helpers

  function elIn(parent, tag, cls, text) {
    var n = document.createElement(tag);
    if (cls) { n.className = cls; }
    if (text != null) { n.textContent = text; }
    if (parent) { parent.appendChild(n); }
    return n;
  }

  function injectStyle() {
    if (document.getElementById(STYLE_ID)) { return; }
    var s = document.createElement('style');
    s.id = STYLE_ID;
    s.setAttribute('data-xu-view', VIEW);
    s.textContent = CSS;
    document.head.appendChild(s);
  }

  function removeStyle() {
    var s = document.getElementById(STYLE_ID);
    if (s && s.parentNode) { s.parentNode.removeChild(s); }
  }

  // big(n) prefers the redesign's compact fmt.big; falls back to fmt.tokens
  // (same notation) while the app-core rewrite is not loaded.
  function big(n) {
    return (XU.fmt && typeof XU.fmt.big === 'function')
      ? XU.fmt.big(n)
      : XU.fmt.tokens(n);
  }

  function esc(s) {
    return String(s == null ? '' : s)
      .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;');
  }

  // pillHTML renders the §13.4 CLI attribution tag. XU.pill (app-core) is
  // authoritative once loaded; the fallback emits the same .pill.pill-cli
  // classes plus .xu-pillfb so only the fallback carries local styling.
  function pillHTML(name) {
    if (typeof XU.pill === 'function') {
      return XU.pill(name);
    }
    return '<span class="pill pill-cli xu-pillfb">' + esc(name) + '</span>';
  }

  // colorFor gives each CLI a stable categorical accent for its letter mark.
  // Not part of the frozen §9.1 surface — guarded.
  function accentFor(name) {
    return (typeof XU.colorFor === 'function')
      ? XU.colorFor(name)
      : (XU.colors && XU.colors.accent) || '#0ae448';
  }

  // normalize maps a byProvider row to the view shape and precomputes totals.
  function normalize(raw) {
    var r = raw || {};
    var tokens = Number(r.totalTokens) || 0;
    return {
      key: String(r.provider || ''),
      cost: Number(r.cost) || 0,
      tokens: tokens,
      messages: Number(r.messages) || 0,
      sessions: Number(r.sessions) || 0,
      search: String(r.provider || '').toLowerCase()
    };
  }

  // ------------------------------------------------------------ data wiring

  function targetQs() {
    var extra = {};
    extra[FIELD] = (XU.state && XU.state[FIELD]) || '';
    return XU.q(extra) || '';
  }

  function syncFromState() {
    renderChips();
    var t = targetQs();
    if (t !== S.qs) { load(t); } else { renderTable(); }
  }

  function load(qs) {
    S.qs = qs;
    S.loading = true;
    S.error = '';
    var ticket = ++S.tick;
    renderChips();
    renderTable();
    renderCards(); // usage join may change with the new query
    var cacheKey = VIEW + ':' + qs;
    var loadFn = function () { return XU.fetch(SUMMARY_ENDPOINT + qs); };
    var renderFn = function (data) {
      if (!S.alive || ticket !== S.tick) { return; }
      // /api/summary returns the Summary envelope; byProvider carries the
      // per-CLI rows (bare-array responses are accepted for robustness).
      var list = Array.isArray(data)
        ? data
        : (data && Array.isArray(data.byProvider)) ? data.byProvider : [];
      S.rows = list.map(normalize)
        .filter(function (r) { return r.tokens > 0 || r.sessions > 0; })
        .sort(function (a, b) { // server is cost-desc already; keep it explicit
          return (b.cost - a.cost) || (a.key < b.key ? -1 : (a.key > b.key ? 1 : 0));
        });
      S.loading = false;
      renderTable();
      renderCards();
    };
    var failFn = function (err) {
      if (!S.alive || ticket !== S.tick) { return; }
      S.loading = false;
      S.error = (err && (err.message || err.statusText)) ||
        ('HTTP ' + ((err && err.status) || '?'));
      renderTable();
    };
    if (typeof XU.swr === 'function') {
      XU.swr(cacheKey, loadFn, renderFn);
    } else {
      loadFn().then(renderFn).catch(failFn);
    }
  }

  // fetchPulse is the fallback path for the CLI cards; when app-core's pulse
  // poller is live, onPulse pushes updates and this one-shot just seeds them.
  function fetchPulse() {
    S.pulseLoading = true;
    S.pulseError = '';
    renderCards();
    XU.fetch(PULSE_ENDPOINT).then(function (p) {
      if (!S.alive) { return; }
      S.pulse = p || {};
      S.pulseLoading = false;
      renderCards();
    }).catch(function (err) {
      if (!S.alive) { return; }
      S.pulseLoading = false;
      S.pulseError = (err && (err.message || err.statusText)) ||
        ('HTTP ' + ((err && err.status) || '?'));
      renderCards();
    });
  }

  function onExternalState() {
    if (!S.alive) { return; }
    renderChips();
    var t = targetQs();
    if (t !== S.qs) { load(t); } // range/tz/mode/filter changed -> refetch
  }

  function onPulseEvent(p) {
    if (!S.alive) { return; }
    S.pulse = p || {};
    S.pulseLoading = false;
    S.pulseError = '';
    renderCards();
  }

  // -------------------------------------------------------------- lifecycle

  function mount(el) {
    injectStyle();
    S.alive = true;
    S.tick++;
    S.pulse = null;
    S.pulseLoading = false;
    S.pulseError = '';
    S.rows = [];
    S.qs = null;
    S.loading = false;
    S.error = '';

    el.innerHTML = '';
    S.el = el;
    S.root = elIn(el, 'div', VIEW_CLASS);

    elIn(S.root, 'div', 'xu-eyebrow', '{ CLIs }');

    var bar = elIn(S.root, 'div', 'xu-toolbar');
    elIn(bar, 'span', 'xu-label', 'Calling agents');
    elIn(bar, 'span', 'xu-grow');
    S.count = elIn(bar, 'span', 'xu-count');
    S.chips = elIn(bar, 'span', 'xu-chips');
    var ex = elIn(bar, 'button', 'xu-xu-export', 'Export CSV');
    ex.type = 'button';
    ex.title = 'Download the calling-agent table as a CSV file';
    ex.setAttribute('aria-label', 'Export calling agents as CSV');
    ex.addEventListener('click', exportCSV);

    S.cards = elIn(S.root, 'div', 'xu-cards');
    S.card = elIn(S.root, 'div', 'card');

    if (typeof XU.onPulse === 'function') {
      S.offPulse = XU.onPulse(onPulseEvent);
    }
    S.unsub = XU.onState(onExternalState);
    fetchPulse();
    syncFromState();
  }

  function unmount() {
    S.alive = false;
    S.tick++;
    if (typeof S.offPulse === 'function') {
      try { S.offPulse(); } catch (e) { /* poller not wired yet */ }
    }
    S.offPulse = null;
    if (typeof S.unsub === 'function') {
      try { S.unsub(); } catch (e) { /* older XU without unsubscribe */ }
    }
    S.unsub = null;
    if (S.el) { S.el.innerHTML = ''; }
    removeStyle();
    S.el = null; S.root = null; S.count = null; S.chips = null;
    S.cards = null; S.card = null;
    S.pulse = null;
    S.rows = [];
  }

  // ---------------------------------------------------------------- toolbar

  function renderChips() {
    if (!S.chips) { return; }
    S.chips.innerHTML = '';
    var val = (XU.state && XU.state[FIELD]) || '';
    if (!val) { return; }
    var chip = elIn(S.chips, 'span', 'xu-chip');
    var cv = elIn(chip, 'span', 'xu-cv', FIELD_LABEL + ': ' + val);
    cv.title = val;
    var btn = elIn(chip, 'button', null, '\u2715');
    btn.type = 'button';
    btn.setAttribute('aria-label', 'Clear ' + FIELD_LABEL + ' filter');
    btn.addEventListener('click', clearField);
  }

  function clearField() {
    var patch = {};
    patch[FIELD] = '';
    XU.setState(patch);
    syncFromState(); // covers XU implementations that do not notify listeners
  }

  // exportCSV downloads the currently listed calling agents (cost-desc) as a
  // CSV matching the table columns: Agent, Share, Tokens, Msgs, Sessions, Cost.
  function exportCSV() {
    if (!S.rows.length) {
      if (typeof XU.toast === 'function') { XU.toast('No rows to export.'); }
      return;
    }
    var total = totalCost();
    var header = ['Agent', 'Share', 'Tokens', 'Msgs', 'Sessions', 'Cost'];
    var out = S.rows.map(function (r) {
      var share = total > 0 ? XU.fmt.pct((r.cost / total) * 100) : '0%';
      return [r.key, share, r.tokens, r.messages, r.sessions, r.cost.toFixed(4)];
    });
    XU.downloadCSV('xusage-clis.csv', header, out);
  }

  // -------------------------------------------------------------- rendering

  function skeletonBox(height) {
    var d = elIn(null, 'div', 'skeleton');
    d.style.height = height + 'px';
    d.style.borderRadius = '8px';
    return d;
  }

  function noticeBox(text) {
    return elIn(null, 'div', 'empty', text);
  }

  // byKey maps lowercased provider keys to usage rows so pulse CLI badges can
  // join against the (possibly range-filtered) summary.
  function byKey() {
    var map = Object.create(null);
    S.rows.forEach(function (r) { map[r.key.toLowerCase()] = r; });
    return map;
  }

  function totalCost() {
    var t = 0;
    S.rows.forEach(function (r) { t += r.cost; });
    return t;
  }

  function updateCount(text) {
    if (S.count) { S.count.textContent = text; }
  }

  function renderCards() {
    if (!S.cards) { return; }
    var box = S.cards;
    box.innerHTML = '';
    if (S.pulseLoading && !S.pulse) {
      box.appendChild(skeletonBox(96));
      return;
    }
    var clis = (S.pulse && Array.isArray(S.pulse.clis)) ? S.pulse.clis : [];
    // Only CLIs that actually have data files on disk are meaningful here.
    clis = clis.filter(function (c) { return c && c.fileCount > 0; });
    if (!clis.length) {
      box.appendChild(noticeBox(S.pulseError
        ? 'Failed to load detected CLIs \u2014 ' + S.pulseError
        : 'No coding agents detected.'));
      return;
    }
    var usage = byKey();
    // Only show CLIs that have non-zero usage.
    clis = clis.filter(function (c) {
      var name = String((c && c.name) || '');
      var row = usage[name.toLowerCase()];
      return row && (row.tokens > 0 || row.sessions > 0);
    });
    if (!clis.length) {
      box.appendChild(noticeBox(S.pulseError
        ? 'Failed to load detected CLIs \u2014 ' + S.pulseError
        : 'No CLI usage recorded.'));
      return;
    }
    var grid = elIn(box, 'div', 'xu-grid');
    clis.forEach(function (c) {
      var name = String((c && c.name) || '');
      var row = usage[name.toLowerCase()];
      var card = elIn(grid, 'div', 'card xu-cli' + (row ? ' xu-click' : ''));
      card.title = name + (c && c.detected ? ' \u00b7 detected' : ' \u00b7 not detected');

      var mark = elIn(card, 'span', 'xu-mark', name ? name.charAt(0) : '?');
      mark.style.color = accentFor(name);

      var meta = elIn(card, 'div', 'xu-cmeta');
      var namerow = elIn(meta, 'div', 'xu-cnamerow');
      elIn(namerow, 'span', 'xu-cname', name || '(unknown)');
      // Detection dot: green streaming dot when the CLI has data on disk.
      elIn(namerow, 'span', 'dot' + (c && c.detected ? ' dot-live' : ''));
      elIn(meta, 'div', 'xu-cfiles',
        XU.fmt.num((c && c.fileCount) || 0) + ' file' + ((c && c.fileCount) === 1 ? '' : 's'));

      var use = elIn(card, 'div', 'xu-cusage');
      if (row) {
        var cost = elIn(use, 'div', 'xu-ccost', XU.fmt.cost(row.cost));
        cost.title = '$' + row.cost.toFixed(4) + ' USD';
        elIn(use, 'div', 'xu-cshare', shareLabel(row));
      } else {
        elIn(use, 'div', 'xu-cshare', 'no usage');
      }

      if (row) {
        card.tabIndex = 0;
        card.setAttribute('role', 'button');
        card.addEventListener('click', function () { drill(row.key); });
        card.addEventListener('keydown', function (ev) {
          if (ev.key === 'Enter' || ev.key === ' ') {
            ev.preventDefault();
            drill(row.key);
          }
        });
      }
    });
  }

  // shareLabel formats a row's cost share of the currently listed rows.
  function shareLabel(r) {
    var total = totalCost();
    if (total <= 0) { return '0%'; }
    return XU.fmt.pct((r.cost / total) * 100);
  }

  function renderTable() {
    if (!S.card) { return; }
    var card = S.card;
    card.innerHTML = '';
    if (S.loading) {
      updateCount('');
      card.appendChild(skeletonBox(180));
      return;
    }
    if (S.error) {
      updateCount('');
      card.appendChild(noticeBox('Failed to load CLI usage \u2014 ' + S.error));
      return;
    }
    if (!S.rows.length) {
      var filtered = (XU.state && XU.state[FIELD]) || '';
      updateCount('');
      card.appendChild(noticeBox(filtered
        ? 'No usage recorded for ' + FIELD_LABEL + ' "' + filtered + '" in this range.'
        : 'No CLI usage recorded yet.'));
      return;
    }
    updateCount(S.rows.length + ' agent' + (S.rows.length === 1 ? '' : 's') +
      ' \u00b7 ' + XU.fmt.cost(totalCost()));
    card.appendChild(buildTable());
  }

  function buildTable() {
    var maxCost = 0;
    S.rows.forEach(function (r) { if (r.cost > maxCost) { maxCost = r.cost; } });

    var table = elIn(null, 'table', 'table');
    var thead = elIn(table, 'thead');
    var hr = elIn(thead, 'tr');
    ['Agent', 'Share', 'Tokens', 'Msgs', 'Sessions', 'Cost'].forEach(function (label, i) {
      elIn(hr, 'th', i >= 1 ? 'xu-r' : '', label);
    });

    var tbody = elIn(table, 'tbody');
    var frag = document.createDocumentFragment();
    S.rows.forEach(function (r) { frag.appendChild(mainRow(r, maxCost)); });
    tbody.appendChild(frag);
    return table;
  }

  function numTd(tr, text, title) {
    var td = elIn(tr, 'td', 'xu-r', text);
    if (title) { td.title = title; }
    return td;
  }

  function mainRow(r, maxCost) {
    var tr = document.createElement('tr');
    tr.className = 'xu-row';
    tr.tabIndex = 0;

    var agent = elIn(tr, 'td');
    var cell = elIn(agent, 'div', 'xu-agentcell');
    var pill = elIn(cell, 'span');
    pill.innerHTML = pillHTML(r.key);

    var share = elIn(tr, 'td', 'xu-r');
    var sc = elIn(share, 'div', 'xu-sharecell');
    var bar = elIn(sc, 'span', 'xu-bar');
    var fill = elIn(bar, 'i');
    var pct = shareLabel(r);
    if (maxCost > 0 && r.cost > 0) {
      fill.style.width = Math.max(2, Math.round((100 * r.cost) / maxCost)) + '%';
    }
    elIn(sc, 'span', null, pct);

    numTd(tr, big(r.tokens), XU.fmt.num(r.tokens) + ' tokens');
    numTd(tr, XU.fmt.num(r.messages));
    numTd(tr, XU.fmt.num(r.sessions));
    numTd(tr, XU.fmt.cost(r.cost), '$' + r.cost.toFixed(4) + ' USD');

    tr.addEventListener('click', function () { drill(r.key); });
    tr.addEventListener('keydown', function (ev) {
      if (ev.key === 'Enter' || ev.key === ' ') {
        ev.preventDefault();
        drill(r.key);
      }
    });
    return tr;
  }

  // Apply this CLI as the global provider filter and open the Series view.
  function drill(key) {
    var patch = {};
    patch[FIELD] = key;
    XU.setState(patch);
    location.hash = '#/series';
  }
})();
