// Series view (SPEC §13.0.2): the consolidated Time Series surface. Hourly /
// Daily / Weekly / Monthly now live behind one granularity segment driving
// GET /api/{hourly,daily,weekly,monthly}. Layout:
//   page-head (eyebrow / title / sub) + range label
//   series-toolbar: #seg-gran (hourly|daily|weekly|monthly) + heatmap toggle
//     (heatmap only meaningful for hourly — day x 24 via XUChart.heatmap)
//   mini-stats strip: total / peak / avg / active buckets
//   Tokens card: svg.chart via XUChart.bars (tokens/mix/cost modes)
//   Buckets table (sticky header, tabular numerics): top hours for hourly,
//     newest-first days for daily, weeks for weekly, months for monthly; every
//     row expands an inline subtable (exact buckets + per-model breakdown with
//     pill attribution + lazy 7-day trend sparkline)
//   CSV export through the mounted.exportCsv hook
// Uses XU.swr for instant load (cacheKey = 'series:' + gran + ':' + XU.q()).

(function () {
  'use strict';

  if (!window.XU || typeof XU.registerView !== 'function') { return; }

  var VIEW = 'series';
  var MONTH_NAMES = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun',
                     'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];

  // GSAP categorical fallback (SPEC §13.0 taxonomy, green-first); XU.colors.categorical wins at draw time.
  var PALETTE_FALLBACK = ['#0ae448', '#ff8709', '#fec5fb', '#9d95ff', '#00bae2', '#7c7c6f', '#42433d', '#191919'];
  var OTHER_COLOR = '#7c7c6f';
  var COST_COLOR = '#0ae448';
  var SEMANTIC = [
    { name: 'Input', field: 'inputTokens', color: '#0ae448' },
    { name: 'Output', field: 'outputTokens', color: '#fffce1' },
    { name: 'Cache read', field: 'cacheReadTokens', color: '#7c7c6f' },
    { name: 'Cache creation', field: 'cacheCreationTokens', color: '#42433d' },
    { name: 'Unsplit total', field: 'extraTotalTokens', color: '#42433d' }
  ];

  // Per-mount state.
  var S = {
    alive: false, tick: 0, root: null, offState: null, reloadTimer: null,
    gran: 'daily',                 // 'hourly' | 'daily' | 'weekly' | 'monthly'
    heatmap: false,                 // hourly-only toggle
    rows: [], chartMode: 'tokens',  // 'tokens' | 'mix' | 'cost'
    topModels: [], trendCache: null,
    chartCleanups: [], tableCleanups: [], qs: null, loading: false, error: ''
  };

  // ---- scoped Carbon styling (injected once) ----------------------------------

  var STYLE_ID = 'xu-view-series-style';
  var P = '.xus-root';
  var CSS = [
    P + ' .xus-head{display:flex;align-items:center;justify-content:space-between;gap:12px;flex-wrap:wrap;margin-bottom:12px}',
    P + ' .xus-eyebrow{font-family:var(--font,"Inter",-apple-system,"Segoe UI",Roboto,sans-serif);font-size:16px;font-weight:400;color:#fffce1;margin-bottom:6px;letter-spacing:.01em}',
    P + ' .xus-title{font-size:11px;font-weight:500;letter-spacing:.08em;text-transform:uppercase;color:#0ae448;font-family:var(--mono,"JetBrains Mono",ui-monospace,Consolas,monospace)}',
    P + ' .xus-seg{display:inline-flex;border:1px solid #42433d;border-radius:8px;overflow:hidden;background:#0e100f}',
    P + ' .xus-seg button{background:none;border:0;color:#fffce1;padding:4px 11px;font-size:11px;font-weight:600;cursor:pointer;font-family:inherit}',
    P + ' .xus-seg button[aria-pressed="true"]{background:#0ae448;color:#0e100f}',
    P + ' .xus-range{font-size:12px;color:#7c7c6f;font-family:var(--mono,"JetBrains Mono",ui-monospace,Consolas,monospace);font-variant-numeric:tabular-nums}',
    P + ' .xus-actions{display:flex;align-items:center;gap:8px}',
    P + ' .xus-legend{display:flex;flex-wrap:wrap;gap:4px 14px;margin:0 0 8px;font-size:11px;color:#fffce1}',
    P + ' .xus-legend-item{display:inline-flex;align-items:center;gap:5px;white-space:nowrap}',
    P + ' .xus-swatch{width:8px;height:8px;border-radius:2px;display:inline-block;flex:none}',
    P + ' .xus-note{font-size:11px;color:#7c7c6f;margin-top:6px;font-family:var(--mono,"JetBrains Mono",ui-monospace,Consolas,monospace)}',
    P + ' .xus-scroll{max-height:520px;overflow:auto}',
    P + ' .xus-table thead th{position:sticky;top:0;z-index:2;background:#191919}',
    P + ' .xus-table td.num,.xus-table th.num{text-align:right;font-variant-numeric:tabular-nums}',
    P + ' .xus-chevron{background:none;border:0;padding:0 4px;color:#fffce1;cursor:pointer;font-size:11px;line-height:1;font-family:inherit}',
    P + ' .xus-chevron:hover{color:#0ae448}',
    P + ' .xus-sub>td{background:#191919;padding:8px 12px 12px 30px}',
    P + ' .xus-facts{display:flex;flex-wrap:wrap;gap:4px 16px;margin:0 0 10px;font-size:11px;color:#7c7c6f}',
    P + ' .xus-facts b{font-weight:600;color:#fffce1;font-variant-numeric:tabular-nums;margin-left:4px}',
    P + ' .xus-subtable{width:100%;border-collapse:collapse;font-size:12px}',
    P + ' .xus-subtable th{color:#7c7c6f;font-weight:500;text-transform:uppercase;font-size:10px;letter-spacing:.06em;padding:2px 8px}',
    P + ' .xus-subtable td{padding:2px 8px;border-top:1px solid #42433d;font-variant-numeric:tabular-nums}',
    P + ' .xus-subtable td.num,.xus-subtable th.num{text-align:right}',
    P + ' .xus-sparkcell{width:72px}',
    P + ' .xus-flag{display:inline-block;margin-left:6px;padding:0 4px;border:1px solid rgba(10,228,72,.45);border-radius:4px;font-size:10px;color:#0ae448;vertical-align:1px}',
    P + ' .xus-more{color:#7c7c6f;font-size:11px}',
    P + ' .xus-heattip{font-size:11px;color:#7c7c6f;margin-bottom:8px;font-family:var(--mono,"JetBrains Mono",ui-monospace,Consolas,monospace)}'
  ].join('\n');

  function ensureStyle() {
    if (document.getElementById(STYLE_ID)) { return; }
    var s = document.createElement('style');
    s.id = STYLE_ID;
    s.textContent = CSS;
    document.head.appendChild(s);
  }
  function removeStyle() {
    var s = document.getElementById(STYLE_ID);
    if (s && s.parentNode) { s.parentNode.removeChild(s); }
  }

  // ---- helpers ----------------------------------------------------------------

  function esc(s) {
    return String(s == null ? '' : s)
      .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;').replace(/'/g, '&#39;');
  }
  function palette() {
    var c = XU.colors && XU.colors.categorical;
    return (c && c.length ? c : PALETTE_FALLBACK);
  }
  function fBig(n) { return (XU.fmt && typeof XU.fmt.big === 'function') ? XU.fmt.big(n) : XU.fmt.tokens(n); }
  function fTokens(n) { return XU.fmt.tokens(n); }
  function fCost(n) { return XU.fmt.cost(n); }
  function fNum(n) { return XU.fmt.num(n); }
  function fPct(n) { return XU.fmt.pct(n); }
  function fDate(k) { return XU.fmt.date(k); }
  function fmtDT(iso) { return iso ? XU.fmt.datetime(iso) : '\u2014'; }

  function modelTokens(m) {
    return (m.inputTokens || 0) + (m.outputTokens || 0) + (m.cacheCreationTokens || 0) +
      (m.cacheReadTokens || 0) + (m.extraTotalTokens || 0);
  }
  function rowTokens(r) {
    return (r.inputTokens || 0) + (r.outputTokens || 0) + (r.cacheCreationTokens || 0) +
      (r.cacheReadTokens || 0) + (r.extraTotalTokens || 0);
  }
  function shortModel(name) {
    name = String(name);
    if (name.length <= 26) { return name; }
    return name.slice(0, 16) + '...' + name.slice(-8);
  }
  function pillModel(name) {
    if (typeof XU.pill === 'function') { try { return XU.pill(null, name); } catch (e) {} }
    return '<span class="badge" title="' + esc(name) + '">' + esc(shortModel(name)) + '</span>';
  }

  // Granularity → endpoint + label + date-parse for the cost-line x axis.
  function endpointFor(gran) {
    return '/api/' + (gran === 'hourly' ? 'hourly' : gran === 'weekly' ? 'weekly' : gran === 'monthly' ? 'monthly' : 'daily');
  }

  // dispLabel formats a row key per granularity.
  function dispLabel(r) {
    if (r.label) { return r.label; }
    var k = String(r.key || '');
    if (S.gran === 'hourly') {
      // "YYYY-MM-DD HH"
      if (k.length >= 13) { return k.slice(5, 10) + ' ' + k.slice(11, 13) + 'h'; }
      return k;
    }
    if (S.gran === 'monthly') {
      var y = +k.slice(0, 4), m = +k.slice(5, 7);
      if (y && m && m >= 1 && m <= 12) { return MONTH_NAMES[m - 1] + ' ' + y; }
      return k;
    }
    if (S.gran === 'weekly') {
      var start = new Date(+k.slice(0, 4), +k.slice(5, 7) - 1, +k.slice(8, 10));
      if (isNaN(start.getTime())) { return fDate(k); }
      var end = new Date(start.getTime() + 6 * 86400000);
      function md(d) { return (d.getMonth() + 1) + '/' + d.getDate(); }
      return md(start) + ' \u2013 ' + md(end);
    }
    return fDate(k);
  }

  // xValue parses a row key to a ms timestamp for the cost line.
  function xValue(r) {
    var k = String(r.key || '');
    if (S.gran === 'monthly') { return new Date(+k.slice(0, 4), +k.slice(5, 7) - 1, 1).getTime(); }
    if (S.gran === 'weekly') { return new Date(+k.slice(0, 4), +k.slice(5, 7) - 1, +k.slice(8, 10)).getTime(); }
    if (S.gran === 'hourly') {
      return new Date(+k.slice(0, 4), +k.slice(5, 7) - 1, +k.slice(8, 10), +k.slice(11, 13)).getTime();
    }
    return new Date(+k.slice(0, 4), +k.slice(5, 7) - 1, +k.slice(8, 10)).getTime();
  }

  function collectModels(rows) {
    var totals = {};
    rows.forEach(function (r) {
      if (!r) { return; }
      (r.models || []).forEach(function (m) { totals[m.model] = (totals[m.model] || 0) + modelTokens(m); });
    });
    return Object.keys(totals).sort(function (a, b) {
      return (totals[b] - totals[a]) || (a < b ? -1 : a > b ? 1 : 0);
    });
  }

  function drainCleanups() {
    S.chartCleanups.forEach(function (fn) { try { fn(); } catch (e) {} });
    S.chartCleanups = [];
    S.tableCleanups.forEach(function (fn) { try { fn(); } catch (e) {} });
    S.tableCleanups = [];
  }

  function disposeList(list) {
    list.forEach(function (fn) { try { fn(); } catch (e) {} });
    list.length = 0;
  }

  function evTarget(ev, sel) {
    var t = ev.target;
    return t && t.closest ? t.closest(sel) : null;
  }

  // ---- hourly heatmap -----------------------------------------------------------

  var HOUR_LABELS = [];
  for (var h = 0; h < 24; h++) { HOUR_LABELS.push((h < 10 ? '0' : '') + h); }
  var RAMP_LOW = [14, 16, 15];
  var RAMP_HIGH = [10, 228, 72];
  function rampColor(t) {
    t = Math.max(0, Math.min(1, t || 0));
    var c = RAMP_LOW.map(function (f, i) { return Math.round(f + (RAMP_HIGH[i] - f) * t); });
    return 'rgb(' + c.join(',') + ')';
  }
  function splitKey(key) {
    var parts = String(key || '').split(' ');
    var day = parts[0], hour = parseInt(parts[1], 10);
    if (!/^\d{4}-\d{2}-\d{2}$/.test(day) || !(hour >= 0 && hour <= 23)) { return null; }
    return { day: day, hour: hour };
  }
  function zeros24() { var a = []; for (var i = 0; i < 24; i++) { a.push(0); } return a; }

  function buildHeatMatrix(rows, metric) {
    var indexOf = {}, days = [], cells = [], max = 0, peak = null;
    rows.forEach(function (r) {
      var kh = splitKey(r.key);
      if (!kh) { return; }
      if (indexOf[kh.day] === undefined) { indexOf[kh.day] = days.length; days.push(kh.day); cells.push(zeros24()); }
      var v = metric === 'tokens' ? rowTokens(r) : (r.cost || 0);
      cells[indexOf[kh.day]][kh.hour] = v;
      if (v > max) { max = v; peak = { day: kh.day, hour: kh.hour, value: v }; }
    });
    return { days: days, cells: cells, max: max, peak: peak };
  }

  function buildProfile(rows, metric) {
    var sum = zeros24(), n = zeros24();
    rows.forEach(function (r) {
      var kh = splitKey(r.key);
      if (!kh) { return; }
      sum[kh.hour] += metric === 'tokens' ? rowTokens(r) : (r.cost || 0);
      n[kh.hour] += 1;
    });
    var pts = [];
    for (var h = 0; h < 24; h++) { if (n[h] > 0) { pts.push({ x: h, y: sum[h] / n[h] }); } }
    return pts;
  }

  function drawHeatmap(host, matrix, metric) {
    if (!host || typeof XUChart === 'undefined') { return; }
    var fmt = metric === 'tokens' ? fNum : fCost;
    var cleanup = XUChart.heatmap(host, {
      rowLabels: matrix.days, colLabels: HOUR_LABELS, cells: matrix.cells,
      yFormat: fmt, colorScale: rampColor
    });
    if (typeof cleanup === 'function') { S.chartCleanups.push(cleanup); }
  }
  function drawProfile(host, points, metric) {
    if (!host || typeof XUChart === 'undefined' || !points.length) { return; }
    var fmt = metric === 'tokens' ? fNum : fCost;
    var cleanup = XUChart.line(host, {
      series: [{ name: 'avg ' + metric, color: '#0ae448', points: points }],
      xType: 'index', yFormat: fmt, height: 240
    });
    if (typeof cleanup === 'function') { S.chartCleanups.push(cleanup); }
  }

  // ---- bars / line chart (daily/weekly/monthly) ---------------------------------

  function modelTip(r, name, m) {
    return dispLabel(r) + ' \u00b7 ' + esc(shortModel(name)) +
      ' \u2014 in ' + fNum(m.inputTokens) + ' \u00b7 out ' + fNum(m.outputTokens) +
      ' \u00b7 cache-read ' + fNum(m.cacheReadTokens) +
      ' \u00b7 cache-create ' + fNum(m.cacheCreationTokens) +
      ' \u00b7 total ' + fNum(modelTokens(m));
  }

  function drawBars(host, legend) {
    var pal = palette();
    var colorOf = {}, topSet = {};
    S.topModels.forEach(function (m, i) { colorOf[m] = pal[i % pal.length]; topSet[m] = true; });
    var groups = S.rows.map(function (r) {
      var byModel = {};
      (r.models || []).forEach(function (m) { byModel[m.model] = m; });
      var values = [], other = 0;
      S.topModels.forEach(function (name) {
        var m = byModel[name];
        if (!m) { return; }
        var t = modelTokens(m);
        if (t > 0) { values.push({ name: name, value: t, color: colorOf[name], tip: modelTip(r, name, m) }); }
      });
      Object.keys(byModel).forEach(function (name) { if (!topSet[name]) { other += modelTokens(byModel[name]); } });
      if (other > 0) { values.push({ name: 'other', value: other, color: OTHER_COLOR, tip: dispLabel(r) + ' \u00b7 other \u2014 ' + fNum(other) + ' tokens' }); }
      return { label: dispLabel(r), values: values };
    });
    var chartEl = document.createElement('div');
    host.appendChild(chartEl);
    S.chartCleanups.push(XUChart.bars(chartEl, { groups: groups, stacked: true, yFormat: fTokens, height: 300 }));
    if (legend) {
      legend.innerHTML = S.topModels.map(function (name) {
        return '<span class="xus-legend-item" title="' + esc(name) + '"><span class="xus-swatch" style="background:' + colorOf[name] + '"></span>' + esc(shortModel(name)) + '</span>';
      }).join('') +
        '<span class="xus-legend-item"><span class="xus-swatch" style="background:' + OTHER_COLOR + '"></span>other</span>';
    }
  }

  function drawMixBars(host, legend) {
    var groups = S.rows.map(function (r) {
      var values = [];
      SEMANTIC.forEach(function (seg) {
        var v = r[seg.field] || 0;
        if (v > 0) { values.push({ name: seg.name, value: v, color: seg.color, tip: dispLabel(r) + ' \u00b7 ' + seg.name + ' \u2014 ' + fNum(v) + ' tokens' }); }
      });
      return { label: dispLabel(r), values: values };
    });
    var chartEl = document.createElement('div');
    host.appendChild(chartEl);
    S.chartCleanups.push(XUChart.bars(chartEl, { groups: groups, stacked: true, yFormat: fTokens, height: 300 }));
    if (legend) {
      legend.innerHTML = SEMANTIC.map(function (seg) {
        return '<span class="xus-legend-item"><span class="xus-swatch" style="background:' + seg.color + '"></span>' + esc(seg.name) + '</span>';
      }).join('');
    }
  }

  function drawCostLine(host) {
    var points = S.rows.map(function (r) {
      var x = xValue(r);
      return { x: isNaN(x) ? null : x, y: r.cost || 0 };
    }).filter(function (pt) { return pt.x !== null; });
    var chartEl = document.createElement('div');
    host.appendChild(chartEl);
    S.chartCleanups.push(XUChart.line(chartEl, {
      series: [{ name: 'Cost', color: COST_COLOR, points: points }],
      xType: 'time', yFormat: fCost, height: 300
    }));
  }

  function drawChart() {
    var host = S.root.querySelector('[data-chart]');
    var legend = S.root.querySelector('[data-legend]');
    if (!host) { return; }
    host.innerHTML = '';
    if (legend) { legend.innerHTML = ''; }
    disposeList(S.chartCleanups);
    if (!S.rows.length) {
      host.innerHTML = '<div class="empty">No ' + S.gran + ' usage in the selected range.</div>';
      return;
    }
    if (S.gran === 'hourly') {
      if (S.heatmap) {
        var matrix = buildHeatMatrix(S.rows, S.chartMode === 'cost' ? 'cost' : 'tokens');
        drawHeatmap(host, matrix, S.chartMode === 'cost' ? 'cost' : 'tokens');
      } else {
        drawProfile(host, buildProfile(S.rows, S.chartMode === 'cost' ? 'cost' : 'tokens'), S.chartMode === 'cost' ? 'cost' : 'tokens');
      }
      return;
    }
    if (S.chartMode === 'cost') { drawCostLine(host); }
    else if (S.chartMode === 'mix') { drawMixBars(host, legend); }
    else { drawBars(host, legend); }
  }

  // ---- mini-stats ---------------------------------------------------------------

  function renderMiniStats() {
    var host = S.root.querySelector('[data-ministats]');
    if (!host) { return; }
    var total = 0, peak = 0, active = 0;
    S.rows.forEach(function (r) {
      var t = rowTokens(r);
      total += t;
      if (t > peak) { peak = t; }
      if (t > 0) { active++; }
    });
    var avg = S.rows.length ? Math.round(total / S.rows.length) : 0;
    host.innerHTML =
      miniStat('Total', fBig(total)) +
      miniStat('Peak', fBig(peak)) +
      miniStat('Avg', fBig(avg)) +
      miniStat('Buckets', fNum(active));
  }
  function miniStat(label, val) {
    return '<div class="mini-stat"><div class="mini-stat-val">' + val + '</div><div class="mini-stat-label">' + label + '</div></div>';
  }

  // ---- 7-day trend sparklines (lazy) ------------------------------------------

  function fetchTrends() {
    var gen = S.tick;
    XU.fetch('/api/models?trend=1').then(function (rows) {
      if (!S.alive || gen !== S.tick) { return; }
      var map = {};
      (Array.isArray(rows) ? rows : []).forEach(function (r) {
        if (r && r.key && Array.isArray(r.trend7)) { map[r.key] = r.trend7.map(function (t) { return (t && t.tokens) || 0; }); }
      });
      S.trendCache = map;
      paintSparks();
    }).catch(function () {});
  }
  function miniSparkSVG(values, color, w, h) {
    var vs = (values || []).map(Number);
    if (!vs.length) { return ''; }
    var max = 0, i;
    for (i = 0; i < vs.length; i++) { if (vs[i] > max) { max = vs[i]; } }
    var pad = 1, pts = [];
    for (i = 0; i < vs.length; i++) {
      var x = pad + (vs.length === 1 ? (w - 2 * pad) / 2 : (w - 2 * pad) * i / (vs.length - 1));
      var y = max > 0 ? pad + (h - 2 * pad) * (1 - vs[i] / max) : h - pad;
      pts.push(x.toFixed(1) + ',' + y.toFixed(1));
    }
    return '<svg width="' + w + '" height="' + h + '" viewBox="0 0 ' + w + ' ' + h + '" aria-hidden="true">' +
      '<polyline points="' + pts.join(' ') + '" fill="none" stroke="' + color + '" stroke-width="1.25" stroke-linejoin="round" stroke-linecap="round"/></svg>';
  }
  function sparkInto(cell, values) {
    cell.setAttribute('data-sparked', '1');
    cell.innerHTML = '';
    var color = palette()[0];
    if (window.XUChart && typeof XUChart.sparkline === 'function') {
      try {
        var c = XUChart.sparkline(cell, { values: values, color: color, width: 60, height: 14 });
        if (typeof c === 'function') { S.tableCleanups.push(c); }
        return;
      } catch (e) {}
    }
    cell.innerHTML = miniSparkSVG(values, color, 60, 14);
  }
  function paintSparks() {
    if (!S.root || !S.trendCache) { return; }
    var cells = S.root.querySelectorAll('[data-spark]');
    for (var i = 0; i < cells.length; i++) {
      var cell = cells[i];
      if (cell.getAttribute('data-sparked')) { continue; }
      var vals = S.trendCache[cell.getAttribute('data-spark')];
      if (vals) { sparkInto(cell, vals); }
    }
  }

  // ---- table -------------------------------------------------------------------

  function modelsCell(r) {
    var models = (r.models || []).slice().sort(function (a, b) { return modelTokens(b) - modelTokens(a); });
    if (!models.length) { return '<span style="color:#7c7c6f">-</span>'; }
    var parts = models.slice(0, 3).map(function (m) { return pillModel(m.model); });
    if (models.length > 3) { parts.push('<span class="xus-more">+' + (models.length - 3) + '</span>'); }
    return parts.join(' ');
  }
  function factItem(label, value) { return '<span>' + label + '<b>' + value + '</b></span>'; }
  function factsStrip(r) {
    var html = '';
    SEMANTIC.forEach(function (seg) { html += factItem(seg.name, fNum(r[seg.field] || 0)); });
    html += factItem('Cost', esc(fCost(r.cost))) + factItem('Messages', fNum(r.messages)) + factItem('Sessions', fNum(r.sessions));
    return '<div class="xus-facts">' + html + '</div>';
  }
  function subTable(r) {
    var html = factsStrip(r);
    var models = (r.models || []).slice().sort(function (a, b) { return (b.cost - a.cost) || (modelTokens(b) - modelTokens(a)); });
    if (!models.length) { return html + '<div style="font-size:11px;color:#7c7c6f">No model attribution recorded.</div>'; }
    var total = rowTokens(r) || 1;
    var body = models.map(function (m) {
      var share = modelTokens(m) / total * 100;
      var flag = m.missingPricing ? '<span class="xus-flag" title="No pricing entry found for this model">no pricing</span>' : '';
      return '<tr>' +
        '<td style="text-align:left">' + pillModel(m.model) + flag + '</td>' +
        tdNum(fNum(m.inputTokens)) + tdNum(fNum(m.outputTokens)) +
        tdNum(fNum(m.cacheReadTokens)) + tdNum(fNum(m.cacheCreationTokens)) +
        tdNum(fNum(modelTokens(m))) + tdNum(esc(fCost(m.cost))) +
        tdNum('<span style="color:#fffce1">' + esc(fPct(share)) + '</span>') +
        '<td class="xus-sparkcell" data-spark="' + esc(m.model) + '" title="7-day token trend"><span style="color:#7c7c6f">\u2026</span></td>' +
        '</tr>';
    }).join('');
    return html + '<table class="xus-subtable"><thead><tr>' +
      '<th style="text-align:left">Model</th><th class="num">Input</th><th class="num">Output</th>' +
      '<th class="num">Cache read</th><th class="num">Cache create</th><th class="num">Tokens</th>' +
      '<th class="num">Cost</th><th class="num">Share</th><th>7-day</th></tr></thead><tbody>' + body + '</tbody></table>';
  }
  function tdNum(html) { return '<td class="num">' + html + '</td>'; }

  function tableHead() {
    if (S.gran === 'hourly') {
      return '<tr><th style="text-align:left">Hour</th><th class="num">Tokens</th><th class="num">Cost</th><th class="num">Msgs</th><th style="text-align:left">Models</th></tr>';
    }
    return '<tr><th style="text-align:left">' + (S.gran === 'weekly' ? 'Week' : S.gran === 'monthly' ? 'Month' : 'Date') + '</th>' +
      '<th class="num">Input</th><th class="num">Output</th><th class="num">Cache R</th><th class="num">Cache C</th>' +
      '<th class="num">Tokens</th><th class="num">Cost</th><th class="num">Msgs</th><th class="num">Sess</th>' +
      '<th style="text-align:left">Models</th></tr>';
  }

  function drawTable() {
    var host = S.root.querySelector('[data-table]');
    if (!host) { return; }
    disposeList(S.tableCleanups);
    if (!S.rows.length) {
      host.innerHTML = '<div class="card"><div class="empty">No ' + S.gran + ' usage in the selected range.</div></div>';
      return;
    }
    // daily newest-first; others ascending.
    var rows = S.gran === 'daily' ? S.rows.slice().reverse() : S.rows;
    var head = tableHead();
    var body = rows.map(function (r, i) {
      if (!r) { return ''; }
      var label = S.gran === 'hourly' ? esc(String(r.key || '')) : esc(dispLabel(r));
      if (S.gran === 'hourly') {
        return '<tr>' +
          '<td style="white-space:nowrap"><button type="button" class="xus-chevron" data-toggle="' + i + '" aria-expanded="false" title="Exact breakdown">&#9656;</button> ' + label + '</td>' +
          tdNum('<span style="font-weight:600">' + esc(fTokens(rowTokens(r))) + '</span>') +
          tdNum(esc(fCost(r.cost))) + tdNum(fNum(r.messages)) +
          '<td style="text-align:left">' + modelsCell(r) + '</td>' +
          '</tr>' +
          '<tr class="xus-sub" data-sub="' + i + '" style="display:none"><td colspan="5">' + subTable(r) + '</td></tr>';
      }
      return '<tr>' +
        '<td style="white-space:nowrap"><button type="button" class="xus-chevron" data-toggle="' + i + '" aria-expanded="false" title="Exact breakdown">&#9656;</button> ' + label + '</td>' +
        tdNum(fNum(r.inputTokens)) + tdNum(fNum(r.outputTokens)) +
        tdNum(fNum(r.cacheReadTokens)) + tdNum(fNum(r.cacheCreationTokens)) +
        tdNum('<span style="font-weight:600">' + esc(fTokens(rowTokens(r))) + '</span>') +
        tdNum(esc(fCost(r.cost))) + tdNum(fNum(r.messages)) + tdNum(fNum(r.sessions)) +
        '<td style="text-align:left">' + modelsCell(r) + '</td>' +
        '</tr>' +
        '<tr class="xus-sub" data-sub="' + i + '" style="display:none"><td colspan="10">' + subTable(r) + '</td></tr>';
    }).join('');
    host.innerHTML = '<div class="card"><div class="xus-scroll">' +
      '<table class="table xus-table"><thead>' + head + '</thead><tbody data-tbody>' + body + '</tbody></table></div></div>';
    var tbody = host.querySelector('[data-tbody]');
    if (tbody) { tbody.addEventListener('click', onTableClick); }
  }

  function onTableClick(ev) {
    var btn = evTarget(ev, 'button[data-toggle]');
    if (!btn) { return; }
    var sub = S.root.querySelector('tr[data-sub="' + btn.getAttribute('data-toggle') + '"]');
    if (!sub) { return; }
    var open = sub.style.display !== 'none';
    sub.style.display = open ? 'none' : '';
    btn.setAttribute('aria-expanded', open ? 'false' : 'true');
    btn.innerHTML = open ? '&#9656;' : '&#9662;';
    if (!open) { paintSparks(); }
  }

  // ---- frame -------------------------------------------------------------------

  function frameHTML() {
    return '<div class="xus-eyebrow">{ Time series }</div>' +
      '<div class="xus-head">' +
      '<span class="xus-title">Time Series &mdash; ' + S.gran + ' aggregation</span>' +
      '<span class="xus-range" data-range-label></span>' +
      '</div>' +
      '<div class="xus-head" style="margin-top:0">' +
      '<span class="xus-seg" role="group" aria-label="Granularity">' +
      '<button type="button" data-gran="hourly" aria-pressed="' + (S.gran === 'hourly') + '">Hourly</button>' +
      '<button type="button" data-gran="daily" aria-pressed="' + (S.gran === 'daily') + '">Daily</button>' +
      '<button type="button" data-gran="weekly" aria-pressed="' + (S.gran === 'weekly') + '">Weekly</button>' +
      '<button type="button" data-gran="monthly" aria-pressed="' + (S.gran === 'monthly') + '">Monthly</button>' +
      '</span>' +
      '<span class="xus-actions">' +
      '<span class="xus-seg" role="group" aria-label="Chart metric" data-modes>' +
      '<button type="button" data-mode="tokens" aria-pressed="' + (S.chartMode === 'tokens') + '">Tokens</button>' +
      '<button type="button" data-mode="mix" aria-pressed="' + (S.chartMode === 'mix') + '">Mix</button>' +
      '<button type="button" data-mode="cost" aria-pressed="' + (S.chartMode === 'cost') + '">Cost</button>' +
      '</span>' +
      '<button type="button" class="btn ghost" data-csv>CSV</button>' +
      '</span></div>' +
      '<div class="mini-stats" data-ministats></div>' +
      '<div class="card">' +
      '<div class="xus-legend" data-legend></div>' +
      '<div data-chart><div class="skeleton" style="height:300px;border-radius:8px"></div></div>' +
      '<div class="xus-note" data-note></div>' +
      '</div>' +
      '<div data-table style="margin-top:16px"><div class="card"><div class="skeleton" style="height:220px;border-radius:8px"></div></div></div>';
  }

  function updateGranSel() {
    if (!S.root) { return; }
    var seg = S.root.querySelector('[data-gran]') ? S.root.querySelector('[data-gran]').parentNode : null;
    seg = S.root.querySelector('.xus-seg[aria-label="Granularity"]');
    if (seg) {
      Array.prototype.forEach.call(seg.querySelectorAll('button'), function (b) {
        b.setAttribute('aria-pressed', String(b.getAttribute('data-gran') === S.gran));
      });
    }
    var modeSeg = S.root.querySelector('[data-modes]');
    if (modeSeg) {
      Array.prototype.forEach.call(modeSeg.querySelectorAll('button'), function (b) {
        b.setAttribute('aria-pressed', String(b.getAttribute('data-mode') === S.chartMode));
      });
    }
  }

  // ---- CSV export ---------------------------------------------------------------

  function csvField(s) { s = String(s); return /[",\r\n]/.test(s) ? '"' + s.replace(/"/g, '""') + '"' : s; }
  function csvNum(v) { v = Number(v); return isFinite(v) ? String(parseFloat(v.toFixed(8))) : '0'; }
  function csvHeader() {
    if (S.gran === 'hourly') { return ['hour', 'input_tokens', 'output_tokens', 'cache_creation_tokens', 'cache_read_tokens', 'extra_total_tokens', 'total_tokens', 'messages', 'cost_usd', 'models']; }
    var key = S.gran === 'weekly' ? 'week_start' : S.gran === 'monthly' ? 'month' : 'date';
    return [key, 'input_tokens', 'output_tokens', 'cache_creation_tokens', 'cache_read_tokens', 'extra_total_tokens', 'total_tokens', 'messages', 'sessions', 'cost_usd', 'models'];
  }
  function buildCsv(rows) {
    var lines = [csvHeader().join(',')];
    (rows || []).forEach(function (r) {
      if (!r) { return; }
      lines.push([csvField(r.key), r.inputTokens || 0, r.outputTokens || 0, r.cacheCreationTokens || 0, r.cacheReadTokens || 0,
        r.extraTotalTokens || 0, rowTokens(r), r.messages || 0, r.sessions || 0, csvNum(r.cost),
        csvField((r.models || []).map(function (m) { return m.model; }).join(';'))].join(','));
    });
    return lines.join('\n') + '\n';
  }
  function downloadCsv() {
    var blob = new Blob([buildCsv(S.rows)], { type: 'text/csv;charset=utf-8' });
    var url = URL.createObjectURL(blob);
    var a = document.createElement('a');
    a.href = url; a.download = S.gran + '.csv';
    document.body.appendChild(a); a.click(); a.remove();
    setTimeout(function () { URL.revokeObjectURL(url); }, 1000);
  }

  // ---- data ---------------------------------------------------------------------

  function scheduleLoad(delayMs) {
    var gen = S.tick;
    if (S.reloadTimer) { clearTimeout(S.reloadTimer); }
    S.reloadTimer = setTimeout(function () {
      S.reloadTimer = null;
      if (!S.alive || gen !== S.tick) { return; }
      load();
    }, delayMs || 0);
  }

  function load() {
    S.reloadTimer = null;
    if (!S.alive) { return; }
    var my = ++S.tick;
    S.loading = true;
    S.error = '';
    var qs = (typeof XU.q === 'function' ? (XU.q() || '') : '');
    if (qs && qs.charAt(0) !== '?') { qs = '?' + qs; }
    var url = endpointFor(S.gran) + qs;

    var render = function (data, fromCache) {
      S.rows = Array.isArray(data) ? data : [];
      S.topModels = S.gran === 'hourly' ? [] : collectModels(S.rows).slice(0, 8);
      S.trendCache = null;
      renderMiniStats();
      drawChart();
      drawTable();
      if (S.gran !== 'hourly') { fetchTrends(); }
      // range label
      var rl = S.root && S.root.querySelector('[data-range-label]');
      if (rl && S.rows.length) {
        rl.textContent = dispLabel(S.rows[0]) + ' \u2013 ' + dispLabel(S.rows[S.rows.length - 1]) + ' \u00b7 ' + S.rows.length + ' buckets';
      }
    };

    // Instant load via SWR (§13.0.1): cacheKey = 'series:' + gran + ':' + q().
    var cacheKey = VIEW + ':' + S.gran + ':' + (XU.q ? XU.q() : '');
    if (typeof XU.swr === 'function') {
      XU.swr(cacheKey, function () { return XU.fetch(url); }, render);
      return;
    }
    XU.fetch(url).then(function (data) {
      if (!S.alive || my !== S.tick) { return; }
      S.loading = false;
      render(data, false);
    }).catch(function (err) {
      if (!S.alive || my !== S.tick) { return; }
      S.loading = false;
      S.error = (err && err.message) ? err.message : 'Failed to load.';
      var chart = S.root.querySelector('[data-chart]');
      if (chart) { chart.innerHTML = '<div class="empty">' + esc(S.error) + '</div>'; }
    });
  }

  function onToolbarClick(ev) {
    var n;
    if ((n = evTarget(ev, '[data-gran]'))) {
      var g = n.getAttribute('data-gran');
      if (g !== S.gran) { S.gran = g; S.heatmap = false; updateGranSel(); load(); }
      return;
    }
    if ((n = evTarget(ev, '[data-mode]'))) {
      var md = n.getAttribute('data-mode');
      if (md !== S.chartMode) { S.chartMode = md; updateGranSel(); drawChart(); }
      return;
    }
    if (evTarget(ev, '[data-csv]')) { downloadCsv(); }
  }

  // ---- lifecycle ----------------------------------------------------------------

  function mount(el) {
    ensureStyle();
    S.alive = true;
    S.tick++;
    S.rows = []; S.topModels = []; S.trendCache = null;
    S.loading = false; S.error = ''; S.heatmap = false;
    el.innerHTML = '';
    S.root = document.createElement('div');
    S.root.className = 'xus-root';
    S.root.innerHTML = frameHTML();
    el.appendChild(S.root);

    // heatmap toggle lives in the note area for hourly
    updateGranSel();
    S.root.addEventListener('click', onToolbarClick);

    S.offState = XU.onState(function () { if (S.alive) { scheduleLoad(120); } });
    load();
  }

  function unmount() {
    S.alive = false;
    S.tick++;
    if (S.reloadTimer) { clearTimeout(S.reloadTimer); S.reloadTimer = null; }
    if (typeof S.offState === 'function') { try { S.offState(); } catch (e) {} }
    S.offState = null;
    drainCleanups();
    if (S.root) {
      S.root.removeEventListener('click', onToolbarClick);
      S.root.innerHTML = '';
      S.root = null;
    }
    removeStyle();
  }

  var api = { mount: mount, unmount: unmount, exportCsv: downloadCsv };
  XU.registerView(VIEW, api);

  // Debug/testing hook.
  window.XUSeries = { collectModels: collectModels, rowTokens: rowTokens, buildCsv: buildCsv, dispLabel: dispLabel };
})();
