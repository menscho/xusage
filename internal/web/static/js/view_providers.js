// Providers view: model VENDOR families (Anthropic, OpenAI, DeepSeek, …)
// from GET /api/providers — a cost-share donut, a dense vendor table with
// proportional share bars and token-split mini-gauges, vendor filter chips
// synced to XU.state.vendor, and expandable per-model subtables (exact token
// buckets + pills + a lazy 7-day trend sparkline from /api/models?trend=1).
//
// Payload (SPEC §13.5): VendorRow[] sorted cost desc —
//   {key,label,inputTokens,outputTokens,cacheCreationTokens,cacheReadTokens,
//    extraTotalTokens?,totalTokens,cost,messages,sessions,share,models:[
//    ModelBreakdown…]}   share is a 0..1 fraction of total cost.
(function () {
  'use strict';

  // Semantic token-bucket colors — GSAP category hues (SPEC §13.0).
  var C_INPUT = '#9d95ff';
  var C_OUTPUT = '#00bae2';
  var C_CACHE_READ = '#ff8709';
  var C_CACHE_CREATE = '#fec5fb';
  var C_OTHER = '#7c7c6f';

  var PALETTE_FALLBACK = ['#0ae448', '#ff8709', '#fec5fb', '#9d95ff', '#00bae2',
                          '#7c7c6f', '#42433d', '#191919'];

  var alive = false;
  var root = null;
  var offState = null;
  var reloadTimer = null;
  var seq = 0;            // response race guard; stale loads are dropped
  var rows = [];          // VendorRow[] currently displayed
  var chipKeys = [];      // unfiltered vendor keys for the chip bar
  var expanded = {};      // vendor key -> true (expanded subtable)
  var trendCache = {};    // vendor key -> {modelName: trend7[]} per range
  var trendInflight = {}; // vendor key -> promise (dedupe expand spam)
  var chartCleanups = []; // donut/sparkline cleanups drained on redraw/unmount
  var sparkCleanups = {}; // vendor key -> [cleanup] so collapse frees observers

  var CSS_TEXT = [
    '.vu-prov-eyebrow{font-size:17px;font-weight:400;color:var(--text,#e0e0e0);letter-spacing:.01em;line-height:1.1;margin:0 0 14px}',
    '.vu-prov-toolbar{display:flex;align-items:center;justify-content:space-between;gap:12px;flex-wrap:wrap;margin-bottom:12px}',
    '.vu-prov-title{font-size:11px;font-weight:800;letter-spacing:.08em;text-transform:uppercase;color:var(--muted,#808080)}',
    '.vu-prov-chips{display:flex;gap:6px;flex-wrap:wrap}',
    '.vu-prov-chip{display:inline-flex;align-items:center;gap:6px;background:none;border:1px solid var(--border,#1e1e1e);border-radius:100px;color:var(--text,#e0e0e0);padding:4px 14px;font-size:12px;font-weight:600;cursor:pointer;font-family:inherit}',
    '.vu-prov-chip:hover{opacity:.8}',
    '.vu-prov-chip.active{color:var(--accent,#4a9eff);border-color:var(--accent,#4a9eff)}',
    '.vu-prov-chip .sw{width:8px;height:8px;border-radius:2px;flex:none}',
    '.vu-prov-xu-export button{appearance:none;background:none;border:1px solid var(--text,#e0e0e0);' +
        'border-radius:100px;color:var(--text,#e0e0e0);padding:5px 14px;font-size:12px;' +
        'font-weight:600;cursor:pointer;font-family:inherit;line-height:1.2}',
    '.vu-prov-xu-export button:hover{opacity:.8}',
    '.vu-prov-xu-export button:focus-visible{outline:1px solid var(--accent,#4a9eff);outline-offset:1px}',
    '.vu-prov-grid{display:grid;grid-template-columns:1fr 320px;gap:16px;align-items:start}',
    '.vu-prov-cardhead{display:flex;align-items:baseline;justify-content:space-between;gap:12px;flex-wrap:wrap;padding:0 0 12px}',
    '.vu-prov-legend{display:flex;gap:12px;flex-wrap:wrap;font-size:11px;color:var(--muted,#808080);font-family:var(--mono,ui-monospace,Consolas,monospace)}',
    '.vu-prov-legend .sw{display:inline-block;width:8px;height:8px;border-radius:2px;margin-right:5px;vertical-align:0}',
    '.vu-prov-donutfoot{padding:0 0 12px;font-size:11px;color:var(--muted,#808080);font-family:var(--mono,ui-monospace,Consolas,monospace)}',
    '.vu-prov-scroll{overflow-x:auto;padding:0}',
    '.vu-prov table{width:100%;border-collapse:collapse;font-size:12px;font-variant-numeric:tabular-nums}',
    '.vu-prov th{position:sticky;top:0;z-index:1;background:var(--panel,#151515);text-align:left;font-size:11px;font-weight:800;letter-spacing:.08em;text-transform:uppercase;color:var(--accent,#4a9eff);padding:8px 10px;border-bottom:2px solid var(--border,#1e1e1e);white-space:nowrap}',
    '.vu-prov td{padding:8px 10px;border-bottom:1px solid var(--border,#1e1e1e);vertical-align:middle;white-space:nowrap;font-family:var(--mono,ui-monospace,Consolas,monospace)}',
    '.vu-prov tbody tr.row{cursor:pointer}',
    '.vu-prov tbody tr.row:hover td{background:var(--panel,#151515)}',
    '.vu-prov tbody tr.row.expanded td{background:var(--panel,#151515)}',
    '.vu-prov td.num,.vu-prov th.num{text-align:right}',
    '.vu-prov .vendor{display:inline-flex;align-items:center;gap:8px;font-weight:700;color:var(--text,#e0e0e0);font-family:var(--font,Inter,sans-serif)}',
    '.vu-prov .vendor .sw{width:9px;height:9px;border-radius:2px;flex:none}',
    '.vu-prov .sharebar{position:relative;display:block;min-width:120px;height:6px;border-radius:3px;background:var(--border,#1e1e1e);overflow:hidden}',
    '.vu-prov .sharebar span{position:absolute;left:0;top:0;bottom:0;border-radius:3px}',
    '.vu-prov .sharewrap{display:flex;align-items:center;gap:8px}',
    '.vu-prov .sharepct{font-variant-numeric:tabular-nums;color:var(--muted,#808080);min-width:44px;text-align:right}',
    '.vu-prov .gauge{display:block;min-width:110px;height:12px;border-radius:6px;background:var(--bg,#0a0a0a);overflow:hidden}',
    '.vu-prov .gauge span{display:block;float:left;height:100%}',
    '.vu-prov .chev{display:inline-flex;padding:2px;color:var(--muted,#808080);transition:transform .15s ease}',
    '.vu-prov tr.expanded .chev{transform:rotate(90deg);color:var(--accent,#4a9eff)}',
    '.vu-prov tr.subrow td{background:var(--panel,#151515);padding:0 10px 10px;white-space:normal}',
    '.vu-prov .subtable{width:100%;border-collapse:collapse;font-size:12px;margin-top:10px}',
    '.vu-prov .subtable th{position:static;background:none;font-size:10.5px;padding:6px 8px;border-bottom:1px solid var(--border,#1e1e1e)}',
    '.vu-prov .subtable td{padding:6px 8px;border-bottom:1px solid var(--border,#1e1e1e);font-variant-numeric:tabular-nums}',
    '.vu-prov .subtable td:first-child{white-space:normal}',
    '.vu-prov .subtable tr:hover td{background:var(--bg,#0a0a0a)}',
    '.vu-prov .sparkhost{display:inline-block;vertical-align:middle;min-width:84px;min-height:16px}',
    '.vu-prov .muted{color:var(--muted,#808080)}',
    '.vu-prov .faint{color:var(--muted,#808080)}',
    '.vu-prov .pill{display:inline-flex;align-items:center;max-width:260px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;vertical-align:middle}',
    '@media (max-width:1100px){.vu-prov-grid{grid-template-columns:1fr}}',
  ].join('\n');

  // ---- small utilities ------------------------------------------------------

  function ensureStyles() {
    if (document.querySelector('style[data-vu-providers]')) { return; }
    var st = document.createElement('style');
    st.setAttribute('data-vu-providers', '');
    st.textContent = CSS_TEXT;
    document.head.appendChild(st);
  }

  function esc(s) {
    return String(s == null ? '' : s)
      .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;');
  }

  function palette() {
    var c = window.XU && window.XU.colors && window.XU.colors.categorical;
    return c && c.length ? c : PALETTE_FALLBACK;
  }

  function fTokens(n) { var x = window.XU; return x && x.fmt ? x.fmt.tokens(n) : String(n); }
  function fCost(n) { var x = window.XU; return x && x.fmt ? x.fmt.cost(n) : '$' + (Number(n) || 0).toFixed(2); }
  function fNum(n) { var x = window.XU; return x && x.fmt ? x.fmt.num(n) : String(n); }
  function fPct(n) { var x = window.XU; return x && x.fmt ? x.fmt.pct(n) : Math.round(n) + '%'; }

  // fBig prefers the redesign's compact fmt.big (987k/12.4M/1.83B) and falls
  // back to fmt.tokens, which shares the same notation.
  function fBig(n) {
    var x = window.XU;
    if (x && x.fmt && typeof x.fmt.big === 'function') { return x.fmt.big(n); }
    return fTokens(n);
  }

  // pillModel renders the model attribution tag (.pill.pill-model per the
  // design system: tinted accent chip). Built locally so rendering never
  // depends on XU.pill's null-CLI handling.
  function pillModel(name) {
    return '<span class="pill pill-model" title="' + esc(name) + '">' + esc(shortName(name, 28)) + '</span>';
  }

  function shortName(name, max) {
    name = String(name);
    if (name.length <= max) { return name; }
    var head = Math.ceil((max - 3) * 0.7);
    var tail = (max - 3) - head;
    return name.slice(0, head) + '...' + name.slice(-tail);
  }

  function vendorColor(key) {
    var i = chipKeys.indexOf(key);
    if (i < 0) { i = rows.map(function (r) { return r.key; }).indexOf(key); }
    var pal = palette();
    return pal[(i >= 0 ? i : 0) % pal.length];
  }

  function disposeCharts() {
    chartCleanups.forEach(function (fn) {
      try { fn(); } catch (e) { /* cleanup must never throw */ }
    });
    chartCleanups = [];
    Object.keys(sparkCleanups).forEach(function (k) { disposeSparks(k); });
  }

  function disposeSparks(key) {
    (sparkCleanups[key] || []).forEach(function (fn) {
      try { fn(); } catch (e) { /* ignore */ }
    });
    delete sparkCleanups[key];
  }

  // ---- rendering ---------------------------------------------------------------

  function renderChips() {
    var host = root.querySelector('[data-chips]');
    if (!host) { return; }
    var current = (window.XU && window.XU.state && window.XU.state.vendor) || '';
    var html = chipHtml('', 'All vendors', '', current);
    chipKeys.forEach(function (key) {
      html += chipHtml(key, key, vendorColor(key), current);
    });
    host.innerHTML = html;
  }

  function chipHtml(key, label, color, current) {
    var active = current === key ? ' active' : '';
    var sw = color ? '<span class="sw" style="background:' + color + '"></span>' : '';
    return '<button type="button" class="vu-prov-chip' + active + '" data-chip="' + esc(key) +
      '" title="Filter every view to ' + esc(label) + '">' + sw + esc(label) + '</button>';
  }

  function renderDonut() {
    var host = root.querySelector('[data-donut]');
    var foot = root.querySelector('[data-donutfoot]');
    if (!host) { return; }
    host.innerHTML = '';
    if (!rows.length) {
      host.innerHTML = '';
      if (foot) { foot.textContent = ''; }
      return;
    }
    var totalCost = 0;
    var slices = rows.slice(0, 8).map(function (r, i) {
      totalCost += r.cost || 0;
      return { name: r.key, value: r.cost || 0, color: palette()[i % palette().length] };
    }).filter(function (s) { return s.value > 0; });
    if (window.XUChart && typeof window.XUChart.donut === 'function' && slices.length) {
      var box = document.createElement('div');
      host.appendChild(box);
      chartCleanups.push(window.XUChart.donut(box, {
        slices: slices,
        center: { label: 'Total cost', value: fCost(totalCost) },
        yFormat: fCost,
      }));
    }
    if (foot) {
      var top = rows[0];
      foot.textContent = rows.length + ' vendor' + (rows.length === 1 ? '' : 's') +
        ' \u00B7 top: ' + top.key + ' ' + fPct((top.share || 0) * 100);
    }
  }

  // gaugeHtml renders the fixed-color input/output/cacheRead/cacheCreate
  // split as one stacked bar; each segment carries its exact count.
  function gaugeHtml(r) {
    var segs = [
      { label: 'Input', v: r.inputTokens || 0, c: C_INPUT },
      { label: 'Output', v: r.outputTokens || 0, c: C_OUTPUT },
      { label: 'Cache read', v: r.cacheReadTokens || 0, c: C_CACHE_READ },
      { label: 'Cache created', v: r.cacheCreationTokens || 0, c: C_CACHE_CREATE },
      { label: 'Other', v: r.extraTotalTokens || 0, c: C_OTHER },
    ].filter(function (s) { return s.v > 0; });
    var total = segs.reduce(function (a, s) { return a + s.v; }, 0);
    if (!total) { return '<span class="faint">\u2013</span>'; }
    return '<span class="gauge">' + segs.map(function (s) {
      return '<span style="width:' + (s.v / total * 100).toFixed(2) + '%;background:' + s.c +
        '" title="' + s.label + ': ' + esc(fNum(s.v)) + ' (' + esc(fPct(s.v / total * 100)) + ')"></span>';
    }).join('') + '</span>';
  }

  function legendHtml() {
    var items = [
      ['Input', C_INPUT], ['Output', C_OUTPUT],
      ['Cache read', C_CACHE_READ], ['Cache created', C_CACHE_CREATE],
    ];
    return items.map(function (it) {
      return '<span><span class="sw" style="background:' + it[1] + '"></span>' + it[0] + '</span>';
    }).join('');
  }

  function renderTable() {
    var host = root.querySelector('[data-table]');
    if (!host) { return; }
    host.innerHTML = '';
    if (!rows.length) {
      host.innerHTML = '<div class="empty">No provider activity in the selected range.</div>';
      return;
    }
    var html =
      '<table>' +
      '<thead><tr>' +
      '<th>Vendor</th>' +
      '<th style="width:180px">Share</th>' +
      '<th class="num">Tokens</th>' +
      '<th style="width:130px">Token split</th>' +
      '<th class="num">Cost</th>' +
      '<th class="num">Models</th>' +
      '<th class="num">Sessions</th>' +
      '<th class="num">Messages</th>' +
      '<th style="width:36px"></th>' +
      '</tr></thead><tbody>';
    rows.forEach(function (r) {
      var color = vendorColor(r.key);
      var isOpen = !!expanded[r.key];
      html +=
        '<tr class="row' + (isOpen ? ' expanded' : '') + '" data-vendor="' + esc(r.key) + '"' +
        ' aria-expanded="' + (isOpen ? 'true' : 'false') + '">' +
        '<td><span class="vendor"><span class="sw" style="background:' + color + '"></span>' +
        esc(r.label || r.key) + '</span></td>' +
        '<td><span class="sharewrap"><span class="sharebar"><span style="width:' +
        (Math.max(Math.min(r.share || 0, 1), 0) * 100).toFixed(2) + '%;background:' + color +
        '"></span></span><span class="sharepct">' + esc(fPct((r.share || 0) * 100)) + '</span></span></td>' +
        '<td class="num" title="' + esc(fNum(r.totalTokens || 0)) + ' tokens">' + esc(fBig(r.totalTokens || 0)) + '</td>' +
        '<td>' + gaugeHtml(r) + '</td>' +
        '<td class="num">' + esc(fCost(r.cost || 0)) + '</td>' +
        '<td class="num">' + fNum((r.models || []).length) + '</td>' +
        '<td class="num">' + fNum(r.sessions || 0) + '</td>' +
        '<td class="num">' + fNum(r.messages || 0) + '</td>' +
        '<td><span class="chev"><svg viewBox="0 0 16 16" width="14" height="14" fill="none" aria-hidden="true">' +
        '<path d="M6 4l4 4-4 4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>' +
        '</svg></span></td>' +
        '</tr>';
      if (isOpen) { html += subRowHtml(r); }
    });
    html += '</tbody></table>';
    host.innerHTML = html;
    // A vendor filter that leaves exactly one row reads best pre-expanded.
    if (rows.length === 1 && window.XU && window.XU.state && window.XU.state.vendor &&
        !expanded[rows[0].key]) {
      toggleRow(rows[0].key);
    }
  }

  // subRowHtml builds the expanded detail: one line per model with exact
  // token buckets, cost, pricing flag and a trend sparkline host.
  function subRowHtml(r) {
    var models = (r.models || []).slice(0, 50);
    var body = models.map(function (m) {
      var flag = m.missingPricing
        ? ' <span class="badge" title="No pricing entry found for this model">unpriced</span>' : '';
      return '<tr>' +
        '<td>' + pillModel(m.model) + flag + '</td>' +
        '<td class="num">' + fNum(m.inputTokens || 0) + '</td>' +
        '<td class="num">' + fNum(m.outputTokens || 0) + '</td>' +
        '<td class="num">' + fNum(m.cacheReadTokens || 0) + '</td>' +
        '<td class="num">' + fNum(m.cacheCreationTokens || 0) + '</td>' +
        '<td class="num">' + fNum(m.extraTotalTokens || 0) + '</td>' +
        '<td class="num">' + esc(fCost(m.cost || 0)) + '</td>' +
        '<td><span class="sparkhost" data-trend="' + esc(m.model) + '">' +
        '<span class="faint">7d\u2026</span></span></td>' +
        '</tr>';
    }).join('');
    if (!models.length) {
      body = '<tr><td colspan="8"><span class="faint">No model detail.</span></td></tr>';
    }
    return '<tr class="subrow"><td colspan="9">' +
      '<table class="subtable">' +
      '<thead><tr>' +
      '<th>Model</th>' +
      '<th class="num">Input</th>' +
      '<th class="num">Output</th>' +
      '<th class="num">Cache read</th>' +
      '<th class="num">Cache created</th>' +
      '<th class="num">Other</th>' +
      '<th class="num">Cost</th>' +
      '<th>7-day trend</th>' +
      '</tr></thead><tbody>' + body + '</tbody></table>' +
      '</td></tr>';
  }

  // ---- expand + lazy trend ------------------------------------------------------

  function toggleRow(key) {
    if (expanded[key]) {
      delete expanded[key];
      disposeSparks(key);
    } else {
      expanded[key] = true;
      fetchTrend(key);
    }
    var tr = root.querySelector('tr.row[data-vendor="' + cssEscape(key) + '"]');
    if (!tr) { return; }
    var open = !!expanded[key];
    tr.setAttribute('aria-expanded', open ? 'true' : 'false');
    tr.classList.toggle('expanded', open);
    var sub = tr.nextElementSibling;
    var isSub = sub && sub.classList.contains('subrow');
    if (open && !isSub) {
      var row = null;
      rows.forEach(function (r) { if (r.key === key) { row = r; } });
      if (row) {
        tr.insertAdjacentHTML('afterend', subRowHtml(row));
        fillTrendHosts(key);
      }
    } else if (!open && isSub) {
      sub.parentNode.removeChild(sub);
    }
  }

  // cssEscape quotes a string for use inside an attribute selector.
  function cssEscape(s) {
    return String(s).replace(/["\\]/g, '\\$&');
  }

  // fetchTrend loads /api/models?trend=1 for one vendor (global filters
  // honored via XU.q) and caches the per-model trend7 arrays for the
  // current range; the cache is cleared whenever the view reloads.
  function fetchTrend(key) {
    if (trendCache[key] || trendInflight[key]) { return; }
    var xu = window.XU;
    if (!xu || typeof xu.fetch !== 'function') { return; }
    var qs = typeof xu.q === 'function' ? (xu.q({ vendor: key }) || '') : '';
    var url = '/api/models?trend=1' + (qs ? (qs.charAt(0) === '?' ? '&' + qs.slice(1) : '&' + qs) : '');
    var mySeq = seq;
    trendInflight[key] = xu.fetch(url).then(function (list) {
      delete trendInflight[key];
      if (!alive || mySeq !== seq) { return; }
      var byModel = {};
      (Array.isArray(list) ? list : []).forEach(function (d) {
        if (d && d.key) { byModel[d.key] = Array.isArray(d.trend7) ? d.trend7 : []; }
      });
      trendCache[key] = byModel;
      fillTrendHosts(key);
    }).catch(function () {
      delete trendInflight[key];
      if (!alive || mySeq !== seq) { return; }
      trendCache[key] = {}; // failed: render empty sparks, don't retry-spam
      fillTrendHosts(key);
    });
  }

  function fillTrendHosts(key) {
    if (!alive || !root) { return; }
    var byModel = trendCache[key] || null;
    var hosts = root.querySelectorAll('tr.subrow [data-trend]');
    for (var i = 0; i < hosts.length; i++) {
      var host = hosts[i];
      var tr = host.closest ? host.closest('tr.row[data-vendor]') : null;
      if (!tr || tr.getAttribute('data-vendor') !== key) { continue; }
      var model = host.getAttribute('data-trend');
      var values = byModel ? (byModel[model] || []).map(function (p) { return p.tokens || 0; }) : null;
      drawSpark(host, values);
    }
  }

  function drawSpark(host, values) {
    if (host.getAttribute('data-drawn')) { return; }
    if (!values) { return; } // still loading: keep the placeholder visible
    host.innerHTML = '';
    host.setAttribute('data-drawn', '1');
    var color = C_OUTPUT;
    var any = values.some(function (v) { return v > 0; });
    if (!any) {
      host.innerHTML = '<span class="faint">\u2013</span>';
      return;
    }
    var xuChart = window.XUChart;
    if (xuChart && typeof xuChart.sparkline === 'function') {
      var box = document.createElement('span');
      host.appendChild(box);
      var clean = xuChart.sparkline(box, { values: values, color: color, width: 84, height: 16 });
      if (typeof clean === 'function') {
        var tr = host.closest ? host.closest('tr.row[data-vendor]') : null;
        var vendorKey = tr ? tr.getAttribute('data-vendor') : '';
        if (vendorKey) {
          if (!sparkCleanups[vendorKey]) { sparkCleanups[vendorKey] = []; }
          sparkCleanups[vendorKey].push(clean);
        } else {
          chartCleanups.push(clean);
        }
      }
      return;
    }
    host.appendChild(staticSpark(values, color)); // charts.js not landed yet
  }

  // staticSpark is a dependency-free fallback sparkline (single path, no axes).
  function staticSpark(values, color) {
    var W = 84, H = 16, pad = 1;
    var max = Math.max.apply(null, values.concat([0]));
    var step = values.length > 1 ? (W - pad * 2) / (values.length - 1) : 0;
    var d = '';
    values.forEach(function (v, i) {
      var x = pad + step * i;
      var y = max > 0 ? pad + (H - pad * 2) * (1 - v / max) : H - pad;
      d += (i ? 'L' : 'M') + x.toFixed(1) + ',' + y.toFixed(1);
    });
    var NS = 'http://www.w3.org/2000/svg';
    var svg = document.createElementNS(NS, 'svg');
    svg.setAttribute('width', W);
    svg.setAttribute('height', H);
    svg.setAttribute('viewBox', '0 0 ' + W + ' ' + H);
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

  // ---- data ----------------------------------------------------------------------

  function scheduleLoad(delayMs) {
    if (reloadTimer) { clearTimeout(reloadTimer); }
    reloadTimer = setTimeout(load, delayMs || 0);
  }

  function load() {
    reloadTimer = null;
    if (!alive) { return; }
    var xu = window.XU;
    if (!xu || typeof xu.fetch !== 'function') { return; }
    var my = ++seq;
    var qs = xu.q() || '';
    var cacheKey = 'providers:' + qs;

    // Main fetch honors the global query (including a vendor filter, which
    // the backend applies entry-wise). When one is active we also fetch the
    // unfiltered list once so the chip bar keeps showing every vendor.
    var loadFn = function () { return xu.fetch('/api/providers' + qs); };
    var renderFn = function (list) {
      if (!alive || my !== seq) { return; }
      rows = Array.isArray(list) ? list : [];
      var chipsReq = (xu.state && xu.state.vendor)
        ? xu.fetch('/api/providers' + (xu.q({ vendor: '' }) || ''))
        : Promise.resolve(list);
      chipsReq.then(function (all) {
        if (!alive || my !== seq) { return; }
        chipKeys = (Array.isArray(all) ? all : rows).map(function (r) { return r.key; });
        afterLoad();
      }).catch(function () {
        if (!alive || my !== seq) { return; }
        chipKeys = rows.map(function (r) { return r.key; });
        afterLoad();
      });
    };
    var failFn = function (err) {
      if (!alive || my !== seq) { return; }
      var msg = err && err.message ? err.message : 'Failed to load providers.';
      rows = [];
      chipKeys = [];
      disposeCharts();
      renderChips();
      var donutHost = root.querySelector('[data-donut]');
      if (donutHost) { donutHost.innerHTML = ''; }
      var tableHost = root.querySelector('[data-table]');
      if (tableHost) { tableHost.innerHTML = '<div class="empty">' + esc(msg) + '</div>'; }
      var foot = root.querySelector('[data-donutfoot]');
      if (foot) { foot.textContent = ''; }
    };
    if (typeof xu.swr === 'function') {
      xu.swr(cacheKey, loadFn, renderFn);
    } else {
      loadFn().then(renderFn).catch(failFn);
    }
  }

  function afterLoad() {
    trendCache = {};       // trends are range/mode/tz dependent
    trendInflight = {};
    disposeCharts();       // drain the previous donut/sparkline observers
    var present = {};
    rows.forEach(function (r) { present[r.key] = true; });
    Object.keys(expanded).forEach(function (k) {
      if (!present[k]) { delete expanded[k]; }
    });
    renderChips();
    renderDonut();
    renderTable();
    // renderTable pre-expands a single filtered row (which fetches trends);
    // otherwise re-prime trends for rows that were already open.
    rows.forEach(function (r) {
      if (expanded[r.key]) { fetchTrend(r.key); }
    });
  }

  // ---- wiring ----------------------------------------------------------------------

  function onClick(ev) {
    var target = ev.target;
    if (!target || !target.closest) { return; }
    var chip = target.closest('[data-chip]');
    if (chip) {
      var key = chip.getAttribute('data-chip');
      var current = (window.XU && window.XU.state && window.XU.state.vendor) || '';
      window.XU.setState({ vendor: current === key ? '' : key });
      return;
    }
    if (target.closest('a, button:not([data-chip]):not([data-xu-export]), .subrow')) { return; }
    var exportBtn = target.closest('[data-xu-export]');
    if (exportBtn) { exportCSV(); return; }
    var tr = target.closest('tr.row[data-vendor]');
    if (tr) { toggleRow(tr.getAttribute('data-vendor')); }
  }

  // exportCSV downloads the vendor rows (cost-desc) as a CSV matching the
  // table: Vendor, Share, Tokens, Cost, Models, Sessions, Messages.
  function exportCSV() {
    if (!rows.length) {
      if (typeof window.XU.toast === 'function') { window.XU.toast('No rows to export.'); }
      return;
    }
    var header = ['Vendor', 'Key', 'Share', 'Tokens', 'Input', 'Output',
      'Cache read', 'Cache created', 'Other', 'Cost', 'Models', 'Sessions', 'Messages'];
    var out = rows.map(function (r) {
      return [r.label || r.key, r.key, (r.share || 0).toFixed(4), r.totalTokens || 0,
        r.inputTokens || 0, r.outputTokens || 0, r.cacheReadTokens || 0,
        r.cacheCreationTokens || 0, r.extraTotalTokens || 0, (r.cost || 0).toFixed(4),
        (r.models || []).length, r.sessions || 0, r.messages || 0];
    });
    window.XU.downloadCSV('xusage-providers.csv', header, out);
  }

  function mount(el) {
    ensureStyles();
    alive = true;
    root = el;
    el.classList.add('vu-prov');
    el.innerHTML =
      '<div class="vu-prov-eyebrow">{ Providers }</div>' +
      '<div class="vu-prov-toolbar">' +
      '<span class="vu-prov-title">Model vendor families</span>' +
      '<span class="vu-prov-chips" data-chips></span>' +
      '<span class="vu-prov-xu-export" title="Download the vendor table as CSV">' +
      '<button type="button" data-xu-export="providers">Export CSV</button></span>' +
      '</div>' +
      '<div class="vu-prov-grid">' +
      '<div class="card">' +
      '<div class="vu-prov-cardhead"><span class="vu-prov-title">Cost share</span>' +
      '<span class="vu-prov-legend">' + legendHtml() + '</span></div>' +
      '<div class="vu-prov-scroll" data-table><div style="padding:8px 0">' +
      '<div class="skeleton" style="height:14px;width:92%;margin-bottom:10px"></div>' +
      '<div class="skeleton" style="height:14px;width:84%;margin-bottom:10px"></div>' +
      '<div class="skeleton" style="height:14px;width:88%;margin-bottom:10px"></div>' +
      '<div class="skeleton" style="height:14px;width:70%"></div>' +
      '</div></div>' +
      '</div>' +
      '<div class="card">' +
      '<div class="vu-prov-cardhead"><span class="vu-prov-title">By cost</span></div>' +
      '<div data-donut><div class="skeleton" style="height:220px;margin:0;border-radius:8px"></div></div>' +
      '<div class="vu-prov-donutfoot" data-donutfoot></div>' +
      '</div>' +
      '</div>';

    el.addEventListener('click', onClick);

    if (window.XU && typeof window.XU.onState === 'function') {
      if (typeof offState === 'function') { offState(); }
      offState = window.XU.onState(function () {
        if (alive) { scheduleLoad(120); }
      });
    }
    load();
  }

  function unmount() {
    alive = false;
    if (typeof offState === 'function') { offState(); offState = null; }
    if (reloadTimer) { clearTimeout(reloadTimer); reloadTimer = null; }
    disposeCharts();
    if (root) {
      root.removeEventListener('click', onClick);
      root.classList.remove('vu-prov');
      root.innerHTML = '';
      root = null;
    }
    rows = [];
    chipKeys = [];
    expanded = {};
    trendCache = {};
    trendInflight = {};
  }

  if (window.XU && typeof window.XU.registerView === 'function') {
    window.XU.registerView('providers', { mount: mount, unmount: unmount });
  }
})();
