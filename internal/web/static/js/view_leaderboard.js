// Leaderboard view (SPEC §13.6 view-leaderboard): a full-width ranked model
// table from GET /api/models?trend=1 — rank, .pill.pill-model tag, vendor,
// proportional green share track, % share, tokens (fmt.big), cost, messages,
// a 7-day trend sparkline column (trend7[].tokens) and first/last activity.
// Sort toggles: tokens (default desc) / cost / messages. Vendor filter chips
// from the rows' distinct vendor values -> XU.setState({vendor}) (flows into
// ?vendor= server-side); clicking the active chip clears it. A client-side
// search box filters by model/vendor. Row click expands the §13.4 in-line
// subtable: exact input/output/cache-read/cache-create tokens (fmt.num,
// semantic color swatches), missingPricing -> 'unpriced' badge, and a bigger
// weekly trend sparkline.
//
// Dark palette: canvas #0a0a0a, nested #111111, hairlines #1e1e1e,
// muted #7c7c6f, light gray #e0e0e0 ink, blue #4a9eff accent. NO
// box-shadow — depth via surface steps + gradient washes. Cards use 8px
// radius + 24px padding + #3a3a3a hairline dividers. Every section opens
// with a curly-bracket { Model leaderboard } eyebrow (16–19px Inter 400
// cream). Uses XU.swr for stale-while-revalidate instant load.
//
// Idioms match the other GSAP views: IIFE, alive guard, offState
// unsubscribe, scoped <style data-xu-view> block, seq-based race guards, and
// chart cleanups drained on every redraw AND on unmount. An export() hook is
// registered alongside mount/unmount so the shell's #export button can dump
// the current (filtered) table to CSV.
(function () {
  'use strict';

  if (typeof XU === 'undefined' || typeof XU.registerView !== 'function') {
    return; // Partial deploy without app.js — do not throw on load.
  }

  // Semantic token-bucket colors (dark palette).
  // Each bucket gets a distinct taxonomy color; blue leads as the brand.
  var SEM = {
    input: '#4a9eff',
    output: '#9d95ff',
    cacheRead: '#00bae2',
    cacheCreate: '#ff8709',
    other: '#3a3a3a'
  };

  // Dark palette — single blue accent (#4a9eff), light gray ink.
  var GREEN = '#4a9eff';

  var CSS = [
    // Dark palette design tokens. canvas #0a0a0a, nested #111111,
    // hairline #1e1e1e, muted #7c7c6f, light gray #e0e0e0 ink, blue #4a9eff accent.
    '.xul-root{--cg:#0a0a0a;--cd:#111111;--ce:#111111;--cb:#1e1e1e;--cs:#1e1e1e;--cm:#7c7c6f;--ca:#7c7c6f;--bone:#e0e0e0;--ind:#4a9eff;--font:Inter,-apple-system,"Segoe UI",Roboto,sans-serif;--mono:"JetBrains Mono",ui-monospace,Consolas,monospace;color:#e0e0e0;font-family:var(--font)}',
    '.xul-root *{box-sizing:border-box}',
    '.xul-root .page-head{margin-bottom:18px}',
    // Section eyebrow — curly-bracket annotation, cream Inter 400 (GSAP §13.0).
    '.xul-root .eyebrow{font-family:Inter,-apple-system,"Segoe UI",Roboto,sans-serif;font-size:16px;font-weight:400;color:#e0e0e0}',
    '.xul-root .eyebrow .green{color:var(--ind);font-weight:600}',
    '.xul-root .title{font-size:44px;font-weight:600;margin:4px 0 2px;line-height:1.0;color:var(--bone)}',
    '.xul-root .sub{font-size:13px;color:var(--cm)}',
    '.xul-root .mini-stats{display:grid;grid-template-columns:repeat(4,1fr);gap:12px;margin-bottom:16px}',
    '.xul-root .mini-stat{background:var(--cd);border:1px solid var(--cb);border-radius:8px;padding:14px 16px}',
    '.xul-root .mini-stat .ms-label{font-family:var(--mono);font-size:10px;text-transform:uppercase;letter-spacing:.08em;color:var(--cm);margin-bottom:6px}',
    '.xul-root .mini-stat .ms-value{font-size:22px;font-weight:600;font-variant-numeric:tabular-nums;color:var(--bone)}',
    '.xul-root .xul-toolbar{display:flex;align-items:center;gap:12px;margin-bottom:14px;flex-wrap:wrap}',
    '.xul-root .xul-chips{display:flex;align-items:center;gap:6px;flex-wrap:wrap}',
    '.xul-root .xul-chips-label{font-family:var(--mono);font-size:10px;text-transform:uppercase;letter-spacing:.08em;color:var(--cm);margin-right:2px}',
    // Outlined cream-pill chips (GSAP §13.0): transparent fill, cream border.
    '.xul-root .xul-chip{appearance:none;font-family:var(--mono);font-size:12px;color:var(--bone);background:none;border:1px solid var(--bone);border-radius:100px;padding:6px 14px;cursor:pointer}',
    '.xul-root .xul-chip:hover{color:var(--ind);border-color:var(--ind)}',
    '.xul-root .xul-chip[aria-pressed="true"]{color:var(--cg);background:var(--ind);border-color:var(--ind)}',
    '.xul-root .search{margin-left:auto;background:var(--cg);border:1px solid var(--cb);border-radius:8px;color:var(--bone);padding:7px 12px;font-size:13px;font-family:inherit;outline:none;width:240px}',
    '.xul-root .search::placeholder{color:var(--cm)}',
    '.xul-root .search:focus{border-color:var(--ind)}',
    // Card: nested surface + hairline, NO box-shadow (GSAP §13.0).
    '.xul-root .card{background:var(--cd);border:1px solid var(--cb);border-radius:8px;padding:24px;display:flex;flex-direction:column}',
    '.xul-root .card-head{display:flex;align-items:center;justify-content:space-between;padding:14px 0;border-bottom:1px solid var(--cb)}',
    '.xul-root .card-title{font-size:13px;font-weight:600;color:var(--bone)}',
    '.xul-root .card-sub{font-family:var(--mono);font-size:11px;color:var(--cm)}',
    '.xul-root .card-sub .sum{color:var(--ind);font-weight:600}',
    '.xul-root .card-body{position:relative;min-height:120px}',
    '.xul-root .xul-scroll{overflow-x:auto}',
    '.xul-root .tbl{width:100%;border-collapse:collapse;font-size:13px}',
    '.xul-root .tbl th{font-family:var(--mono);font-size:10px;text-transform:uppercase;letter-spacing:.08em;color:var(--cm);font-weight:500;padding:0 16px;height:48px;text-align:left;border-bottom:1px solid var(--cb);white-space:nowrap;position:sticky;top:0;z-index:1;background:var(--cd)}',
    '.xul-root .tbl th[data-sort]{cursor:pointer}',
    '.xul-root .tbl th[data-sort]:hover{color:var(--bone)}',
    '.xul-root .tbl th.active{color:var(--ind)}',
    '.xul-root .tbl td{padding:0 16px;height:48px;border-bottom:1px solid var(--cb);vertical-align:middle;white-space:nowrap;color:var(--bone);font-variant-numeric:tabular-nums}',
    '.xul-root .tbl tbody tr.xul-row{cursor:pointer}',
    '.xul-root .tbl tbody tr.xul-row:hover td{background:rgba(224,224,224,.04)}',
    '.xul-root .tbl tbody tr.xul-row.open td{background:rgba(224,224,224,.04)}',
    '.xul-root .tbl .num{text-align:right}',
    '.xul-root .rank{color:var(--cm);font-family:var(--mono);min-width:28px}',
    '.xul-root .rank-1{color:var(--ind);font-weight:700}',
    '.xul-root .ell{display:inline-block;max-width:320px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;vertical-align:middle}',
    '.xul-root .pill{font-family:var(--mono);font-size:11px;border:1px solid var(--cb);border-radius:6px;padding:2px 8px;background:transparent}',
    '.xul-root .pill-model{color:var(--ind);border-color:rgba(74,158,255,.4)}',
    '.xul-root .pill-cli{color:var(--ca);border-color:var(--cb)}',
    '.xul-root .pill i{font-style:normal;opacity:.7;margin-right:3px}',
    '.xul-root .tag{font-family:var(--mono);font-size:11px;color:var(--ca);border:1px solid var(--cb);border-radius:6px;padding:2px 7px}',
    '.xul-root .share-cell{display:flex;align-items:center;gap:10px;min-width:150px}',
    '.xul-root .share-track{flex:1;height:6px;border-radius:3px;background:var(--cg);overflow:hidden}',
    '.xul-root .share-track i{display:block;height:100%;background:var(--ind);border-radius:3px}',
    '.xul-root .share-pct{font-family:var(--mono);font-size:12px;color:var(--ca);min-width:44px;text-align:right}',
    '.xul-root .active-now{color:var(--ind);font-family:var(--mono);font-size:11px;font-weight:600}',
    '.xul-root .xul-spark{display:inline-block;vertical-align:middle;width:84px;height:20px}',
    '.xul-root .xul-spark-big{display:block;height:44px;margin:4px 0 8px}',
    '.xul-root .caret{display:inline-flex;padding:2px;color:var(--cm);transition:transform .15s ease}',
    '.xul-root tr.open .caret{transform:rotate(90deg);color:var(--bone)}',
    '.xul-root tr.xul-sub td{background:rgba(224,224,224,.04);padding:0 16px 14px;white-space:normal}',
    '.xul-root .xul-subwell{border:1px solid var(--cb);border-radius:8px;background:var(--cg);padding:12px 14px}',
    '.xul-root .xul-buckets{display:flex;gap:18px;flex-wrap:wrap;font-size:12px;color:var(--ca);margin:6px 0 10px}',
    '.xul-root .xul-buckets .sw{display:inline-block;width:8px;height:8px;border-radius:2px;margin-right:5px;vertical-align:0}',
    '.xul-root .xul-buckets b{color:var(--bone);font-weight:600;font-variant-numeric:tabular-nums;margin-left:6px}',
    '.xul-root .xul-note{font-size:11px;color:var(--cm);font-family:var(--mono)}',
    '.xul-root .empty{position:absolute;inset:0;display:flex;flex-direction:column;align-items:center;justify-content:center;gap:6px;z-index:5;background:var(--cd);color:var(--cm);text-align:center;padding:24px}',
    '.xul-root .empty .empty-eyebrow{font-family:var(--mono);font-size:11px;text-transform:uppercase;letter-spacing:.08em;color:var(--cm)}',
    '.xul-root .reset-filter{appearance:none;background:none;border:1px solid var(--bone);border-radius:100px;color:var(--bone);font-family:var(--mono);font-size:12px;padding:7px 16px;cursor:pointer;margin-top:6px}',
    '.xul-root .reset-filter:hover{border-color:var(--ind);color:var(--ind)}',
    '@media(max-width:900px){.xul-root .mini-stats{grid-template-columns:repeat(2,1fr)}.xul-root .ell{max-width:160px}.xul-root .search{width:100%}}'
  ].join('\n');

  // ---- state (one instance lives at a time: the router unmounts first) -------
  var alive = false;
  var seq = 0;            // response race guard; stale loads are dropped
  var root = null;
  var offState = null;
  var reloadTimer = null;
  var styleNode = null;
  var cleanups = [];      // chart cleanups, drained on redraw and unmount
  var sparkCleanups = {}; // model key -> [cleanup] so collapse frees observers
  var rows = [];          // latest /api/models payload (server-filtered)
  var sortKey = 'tokens'; // 'cost' | 'tokens' | 'messages' — always descending
  var expanded = {};      // model key -> true while its subtable is open
  var searchTerm = '';    // client-side model/vendor filter

  // ---- helpers -----------------------------------------------------------------

  function esc(s) {
    return String(s == null ? '' : s)
      .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;').replace(/'/g, '&#39;');
  }

  function fNum(n) { return XU.fmt.num(n); }
  function fCost(n) { return XU.fmt.cost(n); }
  function fmtDT(iso) { return iso ? XU.fmt.datetime(iso) : '\u2014'; }

  // fBig prefers the GSAP compact fmt.big (987k/12.4M/1.83B).
  function fBig(n) {
    if (XU.fmt && typeof XU.fmt.big === 'function') { return XU.fmt.big(n); }
    return XU.fmt.tokens(n);
  }

  function pctTxt(f) {
    var p = (Number(f) || 0) * 100;
    return p >= 10 ? Math.round(p) + '%' : p.toFixed(1) + '%';
  }

  function totalTokens(r) {
    var t = Number(r.totalTokens) || 0;
    if (t > 0) { return t; }
    return (Number(r.inputTokens) || 0) + (Number(r.outputTokens) || 0) +
      (Number(r.cacheCreationTokens) || 0) + (Number(r.cacheReadTokens) || 0) +
      (Number(r.extraTotalTokens) || 0);
  }

  function sortValue(r) {
    if (sortKey === 'tokens') { return totalTokens(r); }
    if (sortKey === 'messages') { return Number(r.messages) || 0; }
    return Number(r.cost) || 0;
  }

  function sortedRows() {
    return rows.slice().sort(function (a, b) { return sortValue(b) - sortValue(a); });
  }

  function matchesSearch(r) {
    if (!searchTerm) { return true; }
    var q = searchTerm;
    return (r.key && r.key.toLowerCase().indexOf(q) >= 0) ||
      (r.vendor && r.vendor.toLowerCase().indexOf(q) >= 0);
  }

  function visibleRows() { return sortedRows().filter(matchesSearch); }

  function findRow(key) {
    for (var i = 0; i < rows.length; i++) {
      if (rows[i].key === key) { return rows[i]; }
    }
    return null;
  }

  function cssEscape(s) {
    return String(s).replace(/["\\]/g, '\\$&');
  }

  // modelsQs builds the /api/models query: global state params via XU.q()
  // plus trend=1, and vendor appended when the running XU.q() does not emit
  // it yet (mid-wave compatibility).
  function modelsQs() {
    var qs = typeof XU.q === 'function' ? (XU.q() || '') : '';
    var cur = (XU.state && XU.state.vendor) || '';
    var extra = [];
    if (cur && !/[?&]vendor=/.test(qs)) {
      extra.push('vendor=' + encodeURIComponent(cur));
    }
    extra.push('trend=1');
    return qs + (qs ? '&' : '?') + extra.join('&');
  }

  function ensureStyle() {
    if (styleNode) { return; }
    styleNode = document.createElement('style');
    styleNode.setAttribute('data-xu-view', 'leaderboard');
    styleNode.textContent = CSS;
    document.head.appendChild(styleNode);
  }

  function removeStyle() {
    if (styleNode && styleNode.parentNode) { styleNode.parentNode.removeChild(styleNode); }
    styleNode = null;
  }

  function drainCleanups() {
    cleanups.forEach(function (fn) {
      try { fn(); } catch (e) { /* cleanup must never throw */ }
    });
    cleanups = [];
    Object.keys(sparkCleanups).forEach(function (k) { disposeSparks(k); });
  }

  function disposeSparks(key) {
    (sparkCleanups[key] || []).forEach(function (fn) {
      try { fn(); } catch (e) { /* ignore */ }
    });
    delete sparkCleanups[key];
  }

  function pushSpark(key, clean) {
    if (typeof clean !== 'function') { return; }
    if (key) {
      if (!sparkCleanups[key]) { sparkCleanups[key] = []; }
      sparkCleanups[key].push(clean);
    } else {
      cleanups.push(clean);
    }
  }

  var CARET = '<svg viewBox="0 0 16 16" width="14" height="14" fill="none" aria-hidden="true">' +
    '<path d="M6 4l4 4-4 4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>';

  // ---- rendering ----------------------------------------------------------------

  function renderLoading() {
    drainCleanups();
    if (!root) { return; }
    var host = root.querySelector('[data-table]');
    if (host) {
      host.innerHTML = '<div style="padding:12px 16px">' +
        '<div class="skeleton" style="height:14px;width:92%;margin-bottom:10px"></div>' +
        '<div class="skeleton" style="height:14px;width:84%;margin-bottom:10px"></div>' +
        '<div class="skeleton" style="height:14px;width:88%;margin-bottom:10px"></div>' +
        '<div class="skeleton" style="height:14px;width:76%;margin-bottom:10px"></div>' +
        '<div class="skeleton" style="height:14px;width:64%"></div>' +
        '</div>';
    }
  }

  function renderPageHead() {
    var host = root && root.querySelector('[data-pagehead]');
    if (!host) { return; }
    var tot = 0;
    rows.forEach(function (r) { tot += totalTokens(r); });
    host.innerHTML =
      '<div class="eyebrow">{ Model leaderboard }</div>' +
      '<h1 class="title">' + esc(fBig(tot)) + '</h1>' +
      '<div class="sub">' + esc(fNum(rows.length)) + ' models \u00b7 ranked by ' + esc(sortKey) + '</div>';
  }

  function renderMiniStats() {
    var host = root && root.querySelector('[data-ministats]');
    if (!host) { return; }
    var tot = 0, cost = 0, free = 0;
    rows.forEach(function (r) {
      var t = totalTokens(r);
      tot += t;
      cost += Number(r.cost) || 0;
      if (r.missingPricing) { free += t; }
    });
    var share = tot > 0 ? (free / tot) * 100 : 0;
    function cell(label, value) {
      return '<div class="mini-stat"><div class="ms-label">' + esc(label) +
        '</div><div class="ms-value">' + value + '</div></div>';
    }
    host.innerHTML =
      cell('Models', esc(fNum(rows.length))) +
      cell('Tokens', esc(fBig(tot))) +
      cell('Cost', esc(fCost(cost))) +
      cell('Free share', esc(share >= 10 ? Math.round(share) + '%' : share.toFixed(1) + '%'));
  }

  function renderChips() {
    var host = root && root.querySelector('[data-chips]');
    if (!host) { return; }
    var cur = (XU.state && XU.state.vendor) || '';
    var seen = {};
    var keys = [];
    rows.forEach(function (r) {
      var v = r.vendor || 'Other';
      if (!seen[v]) { seen[v] = true; keys.push(v); }
    });
    var html = '<span class="xul-chips-label">Vendor</span>' +
      '<button type="button" class="xul-chip" data-vendor="" aria-pressed="' + String(!cur) + '">All</button>';
    keys.forEach(function (k) {
      html += '<button type="button" class="xul-chip" data-vendor="' + esc(k) +
        '" aria-pressed="' + String(k === cur) + '">' + esc(k) + '</button>';
    });
    host.innerHTML = html;
  }

  function sparkHost(key, big) {
    return '<span class="' + (big ? 'xul-spark-big' : 'xul-spark') +
      '" data-spark="' + esc(key) + '"><span class="xul-note">7d\u2026</span></span>';
  }

  function activeNowHtml(iso) {
    if (!iso) { return '<span class="xul-note">\u2014</span>'; }
    var d = new Date(iso);
    if (isNaN(d.getTime())) { return esc(fmtDT(iso)); }
    var diff = Date.now() - d.getTime();
    if (diff < 5 * 60 * 1000) { return '<span class="active-now">\u25cf active now</span>'; }
    return esc(fmtDT(iso));
  }

  function thSort(key, label) {
    var cls = 'num' + (sortKey === key ? ' active' : '');
    return '<th class="' + cls + '" data-sort="' + key + '">' + esc(label) + '</th>';
  }

  function renderTable() {
    var host = root && root.querySelector('[data-table]');
    if (!host) { return; }
    host.innerHTML = '';
    var vis = visibleRows();
    if (!rows.length) {
      host.innerHTML = '<div class="empty"><div class="empty-eyebrow">No data</div>' +
        '<p>No model activity in the selected range.</p></div>';
      return;
    }
    if (!vis.length) {
      host.innerHTML = '<div class="empty"><div class="empty-eyebrow">No matches</div>' +
        '<p>No models match your filters.</p>' +
        '<button type="button" class="reset-filter" data-reset>Reset filters</button></div>';
      return;
    }
    var html = '<div class="xul-scroll"><table class="tbl"><thead><tr>' +
      '<th class="num">#</th>' +
      '<th>Model</th>' +
      '<th>Vendor</th>' +
      '<th style="width:170px">Share</th>' +
      thSort('tokens', 'Tokens') +
      thSort('cost', 'Cost') +
      thSort('messages', 'Msgs') +
      '<th>7d trend</th>' +
      '<th>Last used</th>' +
      '<th style="width:28px"></th>' +
      '</tr></thead><tbody>';
    vis.forEach(function (r, i) {
      var rankCls = i === 0 ? 'rank rank-1' : 'rank';
      var share = Math.max(Math.min(Number(r.share) || 0, 1), 0);
      var isOpen = !!expanded[r.key];
      html +=
        '<tr class="xul-row' + (isOpen ? ' open' : '') + '" data-model="' + esc(r.key) + '"' +
        ' aria-expanded="' + (isOpen ? 'true' : 'false') + '">' +
        '<td class="num ' + rankCls + '">' + (i + 1) + '</td>' +
        '<td><span class="pill pill-model" title="' + esc(r.key) + '">' + esc(r.key) + '</span>' +
        (r.missingPricing ? '<span class="tag" style="color:#4a9eff;border-color:rgba(74,158,255,.4);margin-left:6px">unpriced</span>' : '') +
        '</td>' +
        '<td><span class="tag">' + esc(r.vendor || 'Other') + '</span></td>' +
        '<td><span class="share-cell"><span class="share-track"><i style="width:' +
        (share * 100).toFixed(1) + '%"></i></span>' +
        '<span class="share-pct">' + esc(pctTxt(r.share)) + '</span></span></td>' +
        '<td class="num" title="' + esc(fNum(totalTokens(r))) + ' tokens">' + esc(fBig(totalTokens(r))) + '</td>' +
        '<td class="num">' + esc(fCost(r.cost || 0)) + '</td>' +
        '<td class="num">' + fNum(r.messages || 0) + '</td>' +
        '<td>' + sparkHost(r.key, false) + '</td>' +
        '<td>' + activeNowHtml(r.lastActivity) + '</td>' +
        '<td><span class="caret">' + CARET + '</span></td>' +
        '</tr>';
      if (isOpen) { html += subRowHtml(r); }
    });
    html += '</tbody></table></div>';
    host.innerHTML = html;
    vis.forEach(function (r) {
      drawSpark(r.key, !!expanded[r.key]);
    });
  }

  // subRowHtml builds the §13.4 expansion: exact token buckets labeled with
  // semantic swatches, an unpriced badge, and a bigger weekly trend sparkline.
  function subRowHtml(r) {
    var buckets = [
      ['Input', r.inputTokens, SEM.input],
      ['Output', r.outputTokens, SEM.output],
      ['Cache read', r.cacheReadTokens, SEM.cacheRead],
      ['Cache created', r.cacheCreationTokens, SEM.cacheCreate],
      ['Other', r.extraTotalTokens, SEM.other]
    ];
    var chips = buckets.map(function (b) {
      return '<span><span class="sw" style="background:' + b[2] + '"></span>' + esc(b[0]) +
        '<b>' + esc(fNum(Number(b[1]) || 0)) + '</b></span>';
    }).join('');
    return '<tr class="xul-sub"><td colspan="10">' +
      '<div class="xul-subwell">' +
      '<div class="xul-buckets">' + chips + '</div>' +
      '<div class="xul-note">Exact tokens \u00b7 weekly trend</div>' +
      sparkHost(r.key, true) +
      '</div></td></tr>';
  }

  // ---- sparklines -----------------------------------------------------------------

  // drawSpark fills every spark host for one model key (row column, plus the
  // big subtable host when expanded). values come from trend7[].tokens.
  function drawSpark(key, includeBig) {
    if (!alive || !root) { return; }
    var row = findRow(key);
    if (!row) { return; }
    var pts = Array.isArray(row.trend7) ? row.trend7 : [];
    var values = pts.map(function (p) { return Number(p.tokens) || 0; });
    var any = values.some(function (v) { return v > 0; });
    var hosts = root.querySelectorAll('[data-spark="' + cssEscape(key) + '"]');
    for (var i = 0; i < hosts.length; i++) {
      var host = hosts[i];
      var isBig = host.classList.contains('xul-spark-big');
      if (isBig && !includeBig) { continue; }
      disposeSparks(key + (isBig ? ':big' : ''));
      host.innerHTML = '';
      if (!any) {
        host.innerHTML = '<span class="xul-note">\u2013</span>';
        continue;
      }
      if (typeof XUChart === 'object' && XUChart && typeof XUChart.sparkline === 'function') {
        pushSpark(key + (isBig ? ':big' : ''),
          XUChart.sparkline(host, {
            values: values,
            color: GREEN,
            width: isBig ? 220 : 84,
            height: isBig ? 44 : 20
          }));
      } else {
        host.appendChild(staticSpark(values, GREEN, isBig ? 220 : 84, isBig ? 44 : 20));
      }
    }
  }

  // staticSpark is a dependency-free fallback sparkline (single path, no axes)
  // for mid-wave deploys whose charts.js predates XUChart.sparkline.
  function staticSpark(values, color, W, H) {
    var pad = 1.5;
    var min = Math.min.apply(null, values);
    var max = Math.max.apply(null, values);
    var x = function (i) {
      return values.length > 1 ? pad + (i * (W - pad * 2)) / (values.length - 1) : W / 2;
    };
    var y = function (v) {
      return max > min ? pad + (H - pad * 2) * (1 - (v - min) / (max - min)) : H / 2;
    };
    var d = '';
    values.forEach(function (v, i) {
      d += (i ? 'L' : 'M') + x(i).toFixed(1) + ',' + y(v).toFixed(1);
    });
    var NS = 'http://www.w3.org/2000/svg';
    var svg = document.createElementNS(NS, 'svg');
    svg.setAttribute('width', W);
    svg.setAttribute('height', H);
    svg.setAttribute('viewBox', '0 0 ' + W + ' ' + H);
    svg.setAttribute('aria-hidden', 'true');
    var path = document.createElementNS(NS, 'path');
    path.setAttribute('d', d);
    path.setAttribute('fill', 'none');
    path.setAttribute('stroke', color);
    path.setAttribute('stroke-width', '1.5');
    path.setAttribute('stroke-linejoin', 'round');
    path.setAttribute('stroke-linecap', 'round');
    svg.appendChild(path);
    return svg;
  }

  // ---- csv --------------------------------------------------------------------

  // exportCSV dumps the current (filtered, sorted) table to a client-side
  // Blob. Registered as the view's export() hook for the shell #export button.
  function exportCSV() {
    var vis = visibleRows();
    var header = ['Rank', 'Model', 'Vendor', 'Share', 'Tokens', 'Cost', 'Messages', 'Sessions', 'First seen', 'Last seen'];
    var data = vis.map(function (r, i) {
      return [i + 1, r.key, r.vendor || '', pctTxt(r.share), totalTokens(r),
        fCost(r.cost || 0), r.messages || 0, r.sessions || 0,
        fmtDT(r.firstActivity), fmtDT(r.lastActivity)];
    });
    if (typeof XU.downloadCSV === 'function') {
      XU.downloadCSV('leaderboard.csv', header, data);
    }
  }

  // ---- events ---------------------------------------------------------------------

  function evTarget(ev, sel) {
    var t = ev.target;
    return t && t.closest ? t.closest(sel) : null;
  }

  function onClick(ev) {
    var n;
    if ((n = evTarget(ev, '[data-vendor]'))) {
      var k = n.getAttribute('data-vendor');
      var cur = (XU.state && XU.state.vendor) || '';
      XU.setState({ vendor: k === cur ? '' : k });
      return;
    }
    if ((n = evTarget(ev, '[data-sort]'))) { setSort(n.getAttribute('data-sort')); return; }
    if ((n = evTarget(ev, '[data-reset]'))) { resetFilters(); return; }
    if (evTarget(ev, 'a, button')) { return; }
    var tr = evTarget(ev, 'tr.xul-row[data-model]');
    if (tr) { toggleRow(tr.getAttribute('data-model')); }
  }

  function onSearch(ev) {
    searchTerm = ev.target.value.trim().toLowerCase();
    renderTable();
  }

  function resetFilters() {
    searchTerm = '';
    var s = root && root.querySelector('[data-search]');
    if (s) { s.value = ''; }
    XU.setState({ vendor: '' });
    renderTable();
  }

  function setSort(k) {
    if (k !== 'cost' && k !== 'tokens' && k !== 'messages') { return; }
    if (k === sortKey) { return; }
    sortKey = k;
    renderPageHead();
    renderTable();
  }

  function toggleRow(key) {
    if (expanded[key]) {
      delete expanded[key];
      disposeSparks(key);
      disposeSparks(key + ':big');
      var tr = root.querySelector('tr.xul-row[data-model="' + cssEscape(key) + '"]');
      if (tr) {
        tr.classList.remove('open');
        tr.setAttribute('aria-expanded', 'false');
        var sub = tr.nextElementSibling;
        if (sub && sub.classList.contains('xul-sub')) { sub.parentNode.removeChild(sub); }
      }
      return;
    }
    expanded[key] = true;
    var tr2 = root.querySelector('tr.xul-row[data-model="' + cssEscape(key) + '"]');
    if (!tr2) { return; }
    var row = findRow(key);
    if (!row) { return; }
    tr2.classList.add('open');
    tr2.setAttribute('aria-expanded', 'true');
    tr2.insertAdjacentHTML('afterend', subRowHtml(row));
    drawSpark(key, true);
  }

  // ---- data -----------------------------------------------------------------------

  function scheduleLoad(delayMs) {
    if (reloadTimer) { clearTimeout(reloadTimer); }
    reloadTimer = setTimeout(load, delayMs || 0);
  }

  function load() {
    reloadTimer = null;
    if (!alive) { return; }
    var my = ++seq;
    var key = 'leaderboard:' + (typeof XU.q === 'function' ? (XU.q() || '') : '');
    var loadFn = function () { return XU.fetch('/api/models' + modelsQs()); };
    var renderFn = function (data) {
      if (!alive || my !== seq) { return; }
      rows = Array.isArray(data) ? data : [];
      var present = {};
      rows.forEach(function (r) { present[r.key] = true; });
      Object.keys(expanded).forEach(function (k) {
        if (!present[k]) { delete expanded[k]; }
      });
      drainCleanups();
      renderPageHead();
      renderMiniStats();
      renderChips();
      renderTable();
    };
    var errFn = function (err) {
      if (!alive || my !== seq) { return; }
      drainCleanups();
      rows = [];
      renderPageHead();
      renderMiniStats();
      renderChips();
      var host = root && root.querySelector('[data-table]');
      if (host) {
        host.innerHTML = '<div class="empty"><div class="empty-eyebrow">Error</div>' +
          '<p>' + esc((err && err.message) || 'Failed to load models.') + '</p></div>';
      }
    };
    // Instant-load (SPEC §13.0.1): paint from cache now, refresh silently.
    // Falls back to a direct fetch when XU.swr is unavailable (mid-wave).
    if (typeof XU.swr === 'function') {
      XU.swr(key, loadFn, renderFn);
    } else {
      renderLoading();
      loadFn().then(function (d) { renderFn(d); }).catch(errFn);
    }
  }

  // ---- lifecycle --------------------------------------------------------------------

  function mount(el) {
    alive = true;
    ensureStyle();
    el.innerHTML = '';
    root = document.createElement('div');
    root.className = 'xul-root';
    root.innerHTML =
      '<div class="page-head" data-pagehead></div>' +
      '<div class="mini-stats" data-ministats></div>' +
      '<div class="xul-toolbar">' +
      '<div class="xul-chips" data-chips><span class="xul-chips-label">Vendor</span></div>' +
      '<input class="search" data-search type="search" placeholder="Search models\u2026" aria-label="Search models">' +
      '</div>' +
      '<div class="card">' +
      '<div class="card-head"><span class="card-title">Models</span><span class="card-sub" data-cardsub></span></div>' +
      '<div class="card-body"><div class="xul-scroll" data-table>' +
      '<div style="padding:12px 16px">' +
      '<div class="skeleton" style="height:14px;width:92%;margin-bottom:10px"></div>' +
      '<div class="skeleton" style="height:14px;width:84%;margin-bottom:10px"></div>' +
      '<div class="skeleton" style="height:14px;width:88%;margin-bottom:10px"></div>' +
      '</div></div></div></div>';
    el.appendChild(root);

    root.addEventListener('click', onClick);
    var search = root.querySelector('[data-search]');
    if (search) { search.addEventListener('input', onSearch); }
    if (typeof XU.onState === 'function') {
      if (typeof offState === 'function') { offState(); }
      offState = XU.onState(function () {
        if (alive) { scheduleLoad(120); }
      });
    }
    load();
  }

  function unmount() {
    alive = false;
    seq++; // retire any in-flight response
    if (reloadTimer) { clearTimeout(reloadTimer); reloadTimer = null; }
    if (typeof offState === 'function') {
      try { offState(); } catch (e) { /* older XU without unsubscribe */ }
    }
    offState = null;
    drainCleanups();
    if (root) {
      root.removeEventListener('click', onClick);
      root.innerHTML = '';
      root = null;
    }
    removeStyle();
    rows = [];
    expanded = {};
    searchTerm = '';
  }

  XU.registerView('leaderboard', { mount: mount, unmount: unmount, export: exportCSV });
})();
