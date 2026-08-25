/* xusage / ledger — wired to the real xusage HTTP API.
   Same layout, charts and router as the static prototype; mock data replaced
   with fetch() calls against /api/daily, /api/models, /api/summary,
   /api/pulse, /api/blocks, /api/meta. */
(function () {
  'use strict';

  var C = {
    input:   '#6e9fff',
    output:  '#3ecf8e',
    cread:   '#f5c86b',
    ccreate: '#c69df5',
    accent:  '#ff5c38',
    hairline:'#1d2029',
    faint:   '#525763',
    cat: ['#ff5c38', '#6e9fff', '#3ecf8e', '#c69df5', '#f5c86b', '#4cc2c2', '#f27e98', '#9aa7ff', '#d9a05b']
  };

  var state = { range: '7d', metric: 'tokens', route: 'overview' };
  var RANGE_DAYS = { '1d': 1, '7d': 7, '30d': 30, '90d': 90, 'all': 9999 };

  var DAYS = [];
  var MODELS = [];
  var SUMMARY = {};
  var PULSE = {};
  var BLOCKS = [];

  function api(path) {
    return fetch(path).then(function (r) { return r.ok ? r.json() : null; }).catch(function () { return null; });
  }

  function vendorFromKey(key) {
    var k = String(key).toLowerCase();
    if (k.indexOf('claude') >= 0) return 'Anthropic';
    if (k.indexOf('gpt') >= 0 || k.indexOf('o1') >= 0 || k.indexOf('o3') >= 0) return 'OpenAI';
    if (k.indexOf('kimi') >= 0 || k.indexOf('moonshot') >= 0) return 'Moonshot';
    if (k.indexOf('gemini') >= 0) return 'Google';
    if (k.indexOf('qwen') >= 0) return 'Qwen';
    if (k.indexOf('deepseek') >= 0) return 'DeepSeek';
    if (k.indexOf('grok') >= 0) return 'xAI';
    if (k.indexOf('opencode') >= 0) return 'OpenCode';
    if (k.indexOf('codex') >= 0) return 'OpenAI';
    if (k.indexOf('longcat') >= 0) return 'LongCat';
    var parts = String(key).split('/');
    return parts.length > 1 ? parts[0] : 'Other';
  }

  function familyFromKey(key) {
    var k = String(key).toLowerCase();
    if (k.indexOf('claude') >= 0) return 'claude';
    if (k.indexOf('gpt') >= 0 || k.indexOf('o1') >= 0 || k.indexOf('o3') >= 0 || k.indexOf('codex') >= 0) return 'gpt';
    if (k.indexOf('kimi') >= 0) return 'kimi';
    if (k.indexOf('gemini') >= 0) return 'gemini';
    if (k.indexOf('qwen') >= 0) return 'qwen';
    if (k.indexOf('deepseek') >= 0) return 'deepseek';
    if (k.indexOf('grok') >= 0) return 'grok';
    return 'other';
  }

  function dayCost(d) { return Number(d.cost) || 0; }

  function dayTotal(d) {
    var t = 0;
    (d.models || []).forEach(function (m) {
      t += (m.inputTokens || 0) + (m.outputTokens || 0) + (m.cacheReadTokens || 0) + (m.cacheCreationTokens || 0);
    });
    return t + (d.extraTotalTokens || 0);
  }

  function fmtBig(n) {
    n = Number(n) || 0;
    if (n >= 1e9) return (n / 1e9).toFixed(2) + 'B';
    if (n >= 1e6) return (n / 1e6).toFixed(1) + 'M';
    if (n >= 1e3) return (n / 1e3).toFixed(1) + 'K';
    return String(Math.round(n));
  }
  function fmtCost(n) {
    n = Number(n) || 0;
    if (n >= 1000) return '$' + (n / 1000).toFixed(2) + 'K';
    if (n >= 100) return '$' + n.toFixed(0);
    return '$' + n.toFixed(2);
  }
  function fmtMetric(n) {
    return state.metric === 'cost' ? fmtCost(n) : state.metric === 'calls' ? fmtBig(n) : fmtBig(n);
  }
  function fmtDate(iso) {
    if (!iso) return '';
    var d = new Date(iso);
    return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
  }
  function fmtDateLong(iso) {
    if (!iso) return '';
    var d = new Date(iso);
    return d.toLocaleDateString(undefined, { weekday: 'long', day: '2-digit', month: '2-digit', year: 'numeric' });
  }
  function metricOfDay(day) {
    if (state.metric === 'cost') return dayCost(day);
    if (state.metric === 'calls') return Number(day.messages) || 0;
    return dayTotal(day);
  }

  function hitRate(input, cacheRead) {
    var denom = (Number(input) || 0) + (Number(cacheRead) || 0);
    return denom > 0 ? (Number(cacheRead) || 0) / denom * 100 : 0;
  }
  function fmtPct(n) { return (Number(n) || 0).toFixed(2) + '%'; }
  function dayHitRate(day) {
    var inp = 0, cr = 0;
    (day.models || []).forEach(function (m) { inp += m.inputTokens || 0; cr += m.cacheReadTokens || 0; });
    return hitRate(inp, cr);
  }

  function el(tag, cls, html) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    if (html != null) e.innerHTML = html;
    return e;
  }
  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"]/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c];
    });
  }

  var tip = el('div', 'chart-tip');
  document.body.appendChild(tip);
  function showTip(x, y, html) {
    tip.innerHTML = html;
    tip.classList.add('show');
    var r = tip.getBoundingClientRect();
    var lx = Math.min(x + 14, window.innerWidth - r.width - 10);
    var ly = Math.max(10, y - r.height - 12);
    tip.style.left = lx + 'px';
    tip.style.top = ly + 'px';
  }
  function hideTip() { tip.classList.remove('show'); }

  var SVGNS = 'http://www.w3.org/2000/svg';
  function svgEl(tag, attrs) {
    var e = document.createElementNS(SVGNS, tag);
    for (var k in attrs) e.setAttribute(k, attrs[k]);
    return e;
  }
  function niceMax(v) {
    if (v <= 0) return 1;
    var p = Math.pow(10, Math.floor(Math.log10(v)));
    var n = v / p;
    var m = n <= 1 ? 1 : n <= 2 ? 2 : n <= 2.5 ? 2.5 : n <= 5 ? 5 : 10;
    return m * p;
  }

  function lineChart(container, opts) {
    if (!container) return;
    var W = container.clientWidth || 600;
    var H = opts.height || 220;
    var pad = { l: 46, r: 10, t: 12, b: 24 };
    var iw = W - pad.l - pad.r, ih = H - pad.t - pad.b;
    var svg = svgEl('svg', { 'class': 'chart', width: W, height: H, viewBox: '0 0 ' + W + ' ' + H });
    var max = 0;
    opts.series.forEach(function (s) { s.values.forEach(function (v) { if (v > max) max = v; }); });
    max = niceMax(max);
    for (var g = 0; g <= 4; g++) {
      var y = pad.t + ih - (ih * g / 4);
      svg.appendChild(svgEl('line', { 'class': 'grid-ln', x1: pad.l, x2: W - pad.r, y1: y, y2: y }));
      var t = svgEl('text', { 'class': 'axis-txt', x: pad.l - 8, y: y + 3, 'text-anchor': 'end' });
      t.textContent = opts.yFormat(max * g / 4);
      svg.appendChild(t);
    }
    var n = opts.labels.length;
    var stepX = n > 1 ? iw / (n - 1) : iw;
    var every = Math.ceil(n / 8);
    for (var i = 0; i < n; i += every) {
      var tx = svgEl('text', { 'class': 'axis-txt', x: pad.l + i * stepX, y: H - 7, 'text-anchor': 'middle' });
      tx.textContent = opts.labels[i];
      svg.appendChild(tx);
    }
    opts.series.forEach(function (s) {
      var pts = s.values.map(function (v, ii) { return [pad.l + ii * stepX, pad.t + ih - (v / max) * ih]; });
      var dAttr = pts.map(function (p, ii) { return (ii ? 'L' : 'M') + p[0].toFixed(1) + ' ' + p[1].toFixed(1); }).join('');
      if (opts.area) {
        var area = svgEl('path', { 'class': 'area-fill', d: dAttr + 'L' + (pad.l + (n - 1) * stepX) + ' ' + (pad.t + ih) + 'L' + pad.l + ' ' + (pad.t + ih) + 'Z', fill: s.color });
        svg.appendChild(area);
      }
      /* emphasis halo: wide soft echo under the primary line */
      svg.appendChild(svgEl('path', { d: dAttr, fill: 'none', stroke: s.color, 'stroke-width': 7, opacity: 0.10, 'stroke-linejoin': 'round', 'stroke-linecap': 'round' }));
      svg.appendChild(svgEl('path', { 'class': 'line-main', d: dAttr, stroke: s.color }));
      var last = pts[pts.length - 1];
      svg.appendChild(svgEl('circle', { 'class': 'dot-live', cx: last[0], cy: last[1], r: 3 }));
    });
    var hoverLn = svgEl('line', { x1: 0, x2: 0, y1: pad.t, y2: pad.t + ih, stroke: C.faint, 'stroke-dasharray': '3 3', opacity: 0 });
    svg.appendChild(hoverLn);
    svg.addEventListener('mousemove', function (ev) {
      var r = svg.getBoundingClientRect();
      var mx = (ev.clientX - r.left) * (W / r.width);
      var idx = Math.round((mx - pad.l) / stepX);
      if (idx < 0 || idx >= n) { hideTip(); hoverLn.setAttribute('opacity', 0); return; }
      hoverLn.setAttribute('opacity', 1);
      hoverLn.setAttribute('x1', pad.l + idx * stepX);
      hoverLn.setAttribute('x2', pad.l + idx * stepX);
      var html = '<div class="tt-title">' + esc(opts.tipTitle ? opts.tipTitle(idx) : opts.labels[idx]) + '</div>';
      opts.series.forEach(function (s) { html += '<div class="tt-row"><span><i style="background:' + s.color + '"></i>' + esc(s.name) + '</span><b>' + opts.yFormat(s.values[idx]) + '</b></div>'; });
      if (opts.tipExtra) html += opts.tipExtra(idx);
      showTip(ev.clientX, ev.clientY, html);
    });
    svg.addEventListener('mouseleave', function () { hideTip(); hoverLn.setAttribute('opacity', 0); });
    container.appendChild(svg);
  }

  function barChart(container, opts) {
    if (!container) return;
    var W = container.clientWidth || 600;
    var H = opts.height || 220;
    var pad = { l: 46, r: 10, t: 12, b: 24 };
    var iw = W - pad.l - pad.r, ih = H - pad.t - pad.b;
    var svg = svgEl('svg', { 'class': 'chart', width: W, height: H, viewBox: '0 0 ' + W + ' ' + H });
    var max = 0;
    opts.groups.forEach(function (g) { var t = 0; g.parts.forEach(function (p) { t += p.value; }); if (t > max) max = t; });
    max = niceMax(max);
    for (var g = 0; g <= 4; g++) {
      var y = pad.t + ih - (ih * g / 4);
      svg.appendChild(svgEl('line', { 'class': 'grid-ln', x1: pad.l, x2: W - pad.r, y1: y, y2: y }));
      var t = svgEl('text', { 'class': 'axis-txt', x: pad.l - 8, y: y + 3, 'text-anchor': 'end' });
      t.textContent = opts.yFormat(max * g / 4);
      svg.appendChild(t);
    }
    var n = opts.groups.length;
    var slot = iw / n;
    var bw = Math.max(3, Math.min(34, slot * 0.62));
    var every = Math.ceil(n / 10);
    opts.groups.forEach(function (grp, i) {
      var x = pad.l + i * slot + (slot - bw) / 2;
      var acc = 0;
      grp.parts.forEach(function (p) {
        var h = (p.value / max) * ih;
        if (h <= 0) return;
        svg.appendChild(svgEl('rect', { 'class': 'bar', x: x, y: pad.t + ih - acc - h, width: bw, height: Math.max(0.5, h), fill: p.color }));
        acc += h;
      });
      if (i % every === 0) {
        var tx = svgEl('text', { 'class': 'axis-txt', x: pad.l + i * slot + slot / 2, y: H - 7, 'text-anchor': 'middle' });
        tx.textContent = grp.label;
        svg.appendChild(tx);
      }
    });
    svg.addEventListener('mousemove', function (ev) {
      var r = svg.getBoundingClientRect();
      var mx = (ev.clientX - r.left) * (W / r.width);
      var idx = Math.floor((mx - pad.l) / slot);
      if (idx < 0 || idx >= n) { hideTip(); return; }
      var grp = opts.groups[idx];
      var html = '<div class="tt-title">' + esc(grp.tipTitle || grp.label) + '</div>';
      grp.parts.forEach(function (p) { if (p.value <= 0) return; html += '<div class="tt-row"><span><i style="background:' + p.color + '"></i>' + esc(p.name) + '</span><b>' + opts.yFormat(p.value) + '</b></div>'; });
      if (opts.tipExtra) html += opts.tipExtra(idx);
      showTip(ev.clientX, ev.clientY, html);
    });
    svg.addEventListener('mouseleave', hideTip);
    container.appendChild(svg);
  }

  function donut(container, opts) {
    if (!container) return;
    var size = opts.size || 150;
    var r = size / 2, ir = r * 0.68;
    var svg = svgEl('svg', { width: size, height: size, viewBox: '0 0 ' + size + ' ' + size });
    var total = 0;
    opts.slices.forEach(function (s) { total += s.value; });
    var a0 = -Math.PI / 2;
    opts.slices.forEach(function (s) {
      var frac = total ? s.value / total : 0;
      var a1 = a0 + frac * Math.PI * 2;
      if (frac > 0.001) {
        var large = frac > 0.5 ? 1 : 0;
        var p = svgEl('path', {
          d: 'M' + r + ' ' + r + 'L' + (r + r * Math.cos(a0)) + ' ' + (r + r * Math.sin(a0)) +
             'A' + r + ' ' + r + ' 0 ' + large + ' 1 ' + (r + r * Math.cos(a1)) + ' ' + (r + r * Math.sin(a1)) + 'Z',
          fill: s.color, opacity: 0.92
        });
        p.addEventListener('mousemove', function (ev) {
          showTip(ev.clientX, ev.clientY,
            '<div class="tt-row"><span><i style="background:' + s.color + '"></i>' + esc(s.name) +
            '</span><b>' + opts.yFormat(s.value) + ' · ' + Math.round(frac * 100) + '%</b></div>' +
            (s.hitRate != null ? '<div class="tt-row"><span><i style="background:' + C.cread + '"></i>cache hit</span><b>' + fmtPct(s.hitRate) + '</b></div>' : ''));
        });
        p.addEventListener('mouseleave', hideTip);
        svg.appendChild(p);
      }
      a0 = a1;
    });
    svg.appendChild(svgEl('circle', { cx: r, cy: r, r: ir, fill: '#101218' }));
    var wrap = el('div', 'donut-wrap');
    var left = el('div');
    left.style.position = 'relative';
    left.appendChild(svg);
    var center = el('div', null,
      '<div class="donut-center-val">' + opts.center.value + '</div>' +
      '<div class="donut-center-key">' + esc(opts.center.label) + '</div>');
    center.style.cssText = 'position:absolute;inset:0;display:flex;flex-direction:column;align-items:center;justify-content:center;text-align:center;pointer-events:none;';
    left.appendChild(center);
    wrap.appendChild(left);
    var legend = el('div', 'donut-legend');
    opts.slices.slice(0, 6).forEach(function (s) {
      legend.appendChild(el('div', 'donut-row',
        '<i style="background:' + s.color + '"></i>' +
        '<span class="dr-name">' + esc(s.name) + '</span>' +
        '<span class="dr-val">' + opts.yFormat(s.value) + '</span>'));
    });
    wrap.appendChild(legend);
    container.appendChild(wrap);
  }

  function sparkline(container, values, color) {
    if (!container) return;
    var W = 84, H = 26;
    var svg = svgEl('svg', { width: W, height: H, viewBox: '0 0 ' + W + ' ' + H });
    var max = Math.max.apply(null, values) || 1;
    var pts = values.map(function (v, i) { return [(i / (values.length - 1)) * (W - 4) + 2, H - 3 - (v / max) * (H - 6)]; });
    var d = pts.map(function (p, i) { return (i ? 'L' : 'M') + p[0].toFixed(1) + ' ' + p[1].toFixed(1); }).join('');
    svg.appendChild(svgEl('path', { d: d, fill: 'none', stroke: color, 'stroke-width': 1.4, opacity: 0.9 }));
    container.appendChild(svg);
  }

  function secLabel(text, note) {
    return '<div class="sec-label">' + esc(text) + (note ? '<span class="sec-note">' + esc(note) + '</span>' : '') + '</div>';
  }
  function modelColor(i) { return C.cat[i % C.cat.length]; }
  function legendItem(name, color) { return '<span class="legend-item"><i style="background:' + color + '"></i>' + esc(name) + '</span>'; }

  function buildModels(dailyRows, modelRows) {
    var seen = {};
    var list = [];
    (modelRows || []).forEach(function (m) {
      var key = m.key || m.model;
      if (seen[key]) return;
      seen[key] = true;
      list.push({ key: key, vendor: m.vendor || vendorFromKey(key), family: familyFromKey(key) });
    });
    (dailyRows || []).forEach(function (d) {
      (d.models || []).forEach(function (md) {
        var key = md.model || md.key;
        if (!key || seen[key]) return;
        seen[key] = true;
        list.push({ key: key, vendor: vendorFromKey(key), family: familyFromKey(key) });
      });
    });
    return list;
  }

  function modelTotals(days) {
    var out = {};
    MODELS.forEach(function (m) { out[m.key] = { input: 0, output: 0, cacheRead: 0, cacheCreate: 0, cost: 0, messages: 0 }; });
    days.forEach(function (day) {
      (day.models || []).forEach(function (md) {
        var key = md.model || md.key;
        var o = out[key];
        if (!o) return;
        o.input += md.inputTokens || 0; o.output += md.outputTokens || 0;
        o.cacheRead += md.cacheReadTokens || 0; o.cacheCreate += md.cacheCreationTokens || 0;
        var tot = dayTotal(day);
        if (tot > 0) {
          var mt = (md.inputTokens || 0) + (md.outputTokens || 0) + (md.cacheReadTokens || 0) + (md.cacheCreationTokens || 0);
          o.cost += (Number(day.cost) || 0) * (mt / tot);
        }
        o.messages += Math.round(((md.inputTokens || 0) + (md.outputTokens || 0) + (md.cacheReadTokens || 0) + (md.cacheCreationTokens || 0)) / 42000);
      });
    });
    MODELS.forEach(function (m) { var o = out[m.key]; o.hitRate = hitRate(o.input, o.cacheRead); });
    return out;
  }

  function grandTotals(days) {
    var t = { tokens: 0, cost: 0, sessions: 0, messages: 0 };
    days.forEach(function (d) { t.tokens += dayTotal(d); t.cost += dayCost(d); t.sessions += Number(d.sessions) || 0; t.messages += Number(d.messages) || 0; });
    return t;
  }

  function splitIO(days) {
    var s = { input: 0, output: 0 };
    days.forEach(function (d) { (d.models || []).forEach(function (m) { s.input += m.inputTokens || 0; s.output += m.outputTokens || 0; }); });
    return s;
  }

  function rangedDays() {
    var n = RANGE_DAYS[state.range] || 9999;
    return DAYS.slice(-n);
  }

  var views = {};

  views.overview = function (root) {
    var days = rangedDays();
    if (!days.length) { root.innerHTML = '<div class="empty">No data for this range.</div>'; return; }
    var tot = grandTotals(days);
    var perDay = days.map(metricOfDay);
    var labels = days.map(function (d) { return fmtDate(d.date || d.key); });
    var io = splitIO(days);
    var totHR = hitRate(io.input, days.reduce(function (s, d) { var cr = 0; (d.models || []).forEach(function (m) { cr += m.cacheReadTokens || 0; }); return s + cr; }, 0));

    var todayD = days[days.length - 1];
    var yestD = days[days.length - 2] || todayD;
    var delta = metricOfDay(yestD) ? (metricOfDay(todayD) - metricOfDay(yestD)) / metricOfDay(yestD) * 100 : 0;
    var mt = modelTotals(days);
    var ranked = MODELS.map(function (m, i) {
      var o = mt[m.key] || { input: 0, output: 0, cacheRead: 0, cacheCreate: 0, cost: 0 };
      return { m: m, i: i, total: o.input + o.output + o.cacheRead + o.cacheCreate, cost: o.cost, o: o };
    }).sort(function (a, b) { return b.total - a.total; });

    var html = secLabel('overview', state.range + ' window') +
      '<div class="grid g-kpi">' +
        kpiCard('total ' + state.metric, fmtMetric(state.metric === 'cost' ? tot.cost : state.metric === 'calls' ? tot.messages : tot.tokens),
          '<span class="' + (delta >= 0 ? 'up' : 'down') + '">' + (delta >= 0 ? '▲' : '▼') + ' ' + Math.abs(delta).toFixed(1) + '%</span> vs yesterday', perDay, C.accent) +
        kpiCard('tokens', fmtBig(tot.tokens), fmtBig(io.input) + ' in · ' + fmtBig(io.output) + ' out', null, null) +
        kpiCard('cache hit rate', fmtPct(totHR), 'cache read / total input', days.map(dayHitRate), C.cread) +
        kpiCard('sessions', fmtBig(tot.sessions), fmtBig(tot.messages) + ' messages', null, null) +
        kpiCard('active models', String(ranked.filter(function (r) { return r.total > 0; }).length),
          ranked[0] ? esc(ranked[0].m.key) + ' leads' : '—', null, null) +
      '</div>' +
      secLabel('activity') +
      '<div class="card"><div class="card-title">' + esc(state.metric) + ' / day <span class="card-note">hover for detail</span></div>' +
      '<div id="ov-chart"></div></div>' +
      '<div style="height:12px"></div>' +
      '<div class="grid g-main">' +
        '<div class="card"><div class="card-title">model ranking <span class="card-note">by tokens · hit rate</span></div><div id="ov-rank"></div></div>' +
        '<div class="card"><div class="card-title">share <span class="card-note">' + state.range + ' · hover for hit rate</span></div><div id="ov-donut"></div></div>' +
      '</div>';

    root.innerHTML = html;

    lineChart(root.querySelector('#ov-chart'), {
      series: [{ name: state.metric, color: C.accent, values: perDay }],
      labels: labels, height: 230, area: true, yFormat: fmtMetric,
      tipTitle: function (i) { return fmtDateLong(days[i].date || days[i].key); },
      tipExtra: function (i) {
        return '<div class="tt-row"><span><i style="background:' + C.cread + '"></i>cache hit</span><b>' + fmtPct(dayHitRate(days[i])) + '</b></div>';
      }
    });

    var active = ranked.filter(function (r) { return r.total > 0; });
    var maxT = active[0] ? active[0].total : 1;
    var rankEl = root.querySelector('#ov-rank');
    var list = el('div', 'hbar-list');
    active.slice(0, 8).forEach(function (r) {
      list.appendChild(el('div', 'hbar-row',
        '<span class="hbar-name">' + esc(r.m.key) + '</span>' +
        '<span class="hbar-track"><span class="hbar-fill" style="width:' + Math.max(1.5, r.total / maxT * 100).toFixed(1) + '%;background:' + modelColor(r.i) + '"></span></span>' +
        '<span class="hbar-val">' + fmtBig(r.total) + ' <span style="color:var(--faint);font-weight:400">' + fmtPct(r.o.hitRate) + '</span></span>'));
    });
    rankEl.appendChild(list);

    donut(root.querySelector('#ov-donut'), {
      slices: active.slice(0, 6).map(function (r) { return { name: r.m.key, value: r.total, color: modelColor(r.i), hitRate: r.o.hitRate }; }),
      center: { value: fmtBig(tot.tokens), label: 'tokens' }, yFormat: fmtBig
    });
  };

  function kpiCard(key, val, sub, spark, sparkColor) {
    return '<div class="card kpi">' +
      '<div class="kpi-key">' + esc(key) + '</div>' +
      '<div class="kpi-val">' + val + '</div>' +
      (sub ? '<div class="kpi-sub">' + sub + '</div>' : '') +
      (spark ? '<div class="kpi-spark" data-spark=\'' + JSON.stringify(spark) + '\' data-color="' + sparkColor + '"></div>' : '') +
      '</div>';
  }

  views.timeline = function (root) {
    var days = rangedDays();
    if (!days.length) { root.innerHTML = '<div class="empty">No data for this range.</div>'; return; }
    var labels = days.map(function (d) { return fmtDate(d.date || d.key); });

    var html = secLabel('timeline') +
      '<div class="card"><div class="card-title">stacked tokens / day</div>' +
      '<div id="tl-bars"></div>' +
      '<div class="legend">' + legendItem('input', C.input) + legendItem('output', C.output) + legendItem('cache read', C.cread) + legendItem('cache create', C.ccreate) + '</div></div>' +
      '<div style="height:12px"></div>' +
      '<div class="card"><div class="card-title">cost / day</div><div id="tl-cost"></div></div>' +
      secLabel('day by day') +
      '<div class="card table-wrap"><table class="table"><thead><tr>' +
      '<th>date</th><th class="n">total</th><th class="n">input</th><th class="n">output</th>' +
      '<th class="n">cache read</th><th class="n">hit rate</th><th class="n">cost</th><th class="n">sessions</th>' +
      '</tr></thead><tbody id="tl-rows"></tbody></table></div>';

    root.innerHTML = html;

    barChart(root.querySelector('#tl-bars'), {
      groups: days.map(function (d) {
        var parts = [{ name: 'input', value: 0, color: C.input }, { name: 'output', value: 0, color: C.output }, { name: 'cache read', value: 0, color: C.cread }, { name: 'cache create', value: 0, color: C.ccreate }];
        (d.models || []).forEach(function (m) { parts[0].value += m.inputTokens || 0; parts[1].value += m.outputTokens || 0; parts[2].value += m.cacheReadTokens || 0; parts[3].value += m.cacheCreationTokens || 0; });
        return { label: fmtDate(d.date || d.key), tipTitle: fmtDateLong(d.date || d.key), parts: parts };
      }),
      height: 240, yFormat: fmtBig,
      tipExtra: function (i) { return '<div class="tt-row"><span><i style="background:' + C.cread + '"></i>cache hit</span><b>' + fmtPct(dayHitRate(days[i])) + '</b></div>'; }
    });

    lineChart(root.querySelector('#tl-cost'), {
      series: [{ name: 'cost', color: C.accent, values: days.map(dayCost) }],
      labels: labels, height: 170, area: true, yFormat: fmtCost,
      tipTitle: function (i) { return fmtDateLong(days[i].date || days[i].key); },
      tipExtra: function (i) { return '<div class="tt-row"><span><i style="background:' + C.cread + '"></i>cache hit</span><b>' + fmtPct(dayHitRate(days[i])) + '</b></div>'; }
    });

    var tbody = root.querySelector('#tl-rows');
    var rowsHtml = '';
    days.slice().reverse().forEach(function (d) {
      var agg = { input: 0, output: 0, cacheRead: 0, cacheCreate: 0 };
      (d.models || []).forEach(function (m) { agg.input += m.inputTokens || 0; agg.output += m.outputTokens || 0; agg.cacheRead += m.cacheReadTokens || 0; agg.cacheCreate += m.cacheCreationTokens || 0; });
      var tot = agg.input + agg.output + agg.cacheRead + agg.cacheCreate;
      rowsHtml += '<tr>' +
        '<td class="strong">' + esc(fmtDate(d.date || d.key)) + '</td>' +
        '<td class="n strong">' + fmtBig(tot) + '</td>' +
        '<td class="n"><span class="tok-chip" style="background:' + C.input + '"></span>' + fmtBig(agg.input) + '</td>' +
        '<td class="n"><span class="tok-chip" style="background:' + C.output + '"></span>' + fmtBig(agg.output) + '</td>' +
        '<td class="n"><span class="tok-chip" style="background:' + C.cread + '"></span>' + fmtBig(agg.cacheRead) + '</td>' +
        '<td class="n">' + fmtPct(hitRate(agg.input, agg.cacheRead)) + '</td>' +
        '<td class="n">' + fmtCost(dayCost(d)) + '</td>' +
        '<td class="n">' + (d.sessions || 0) + '</td></tr>';
    });
    tbody.innerHTML = rowsHtml;
  };

  views.models = function (root) {
    var days = rangedDays();
    var mt = modelTotals(days);
    var ranked = MODELS.map(function (m, i) {
      var o = mt[m.key] || { input: 0, output: 0, cacheRead: 0, cacheCreate: 0, cost: 0 };
      return { m: m, i: i, o: o, total: o.input + o.output + o.cacheRead + o.cacheCreate };
    }).sort(function (a, b) { return b.total - a.total; });
    var grand = ranked.reduce(function (s, r) { return s + r.total; }, 0) || 1;

    var activeModels = ranked.filter(function (r) { return r.total > 0; });
    var html = secLabel('models') +
      '<div class="grid g-main">' +
      '<div class="card table-wrap"><table class="table"><thead><tr>' +
      '<th>#</th><th>model</th><th>vendor</th><th class="n">total</th><th class="n">share</th>' +
      '<th class="n">input</th><th class="n">output</th><th class="n">cache</th><th class="n">hit rate</th><th class="n">cost</th>' +
      '</tr></thead><tbody>';

    activeModels.forEach(function (r, ri) {
      var cache = r.o.cacheRead + r.o.cacheCreate;
      html += '<tr>' +
        '<td class="n" style="color:var(--faint)">' + (ri + 1) + '</td>' +
        '<td class="strong"><span class="tok-chip" style="background:' + modelColor(r.i) + '"></span>' + esc(r.m.key) + '</td>' +
        '<td>' + esc(r.m.vendor || 'Other') + '</td>' +
        '<td class="n strong">' + fmtBig(r.total) + '</td>' +
        '<td class="n">' + (r.total / grand * 100).toFixed(1) + '%</td>' +
        '<td class="n">' + fmtBig(r.o.input) + '</td>' +
        '<td class="n">' + fmtBig(r.o.output) + '</td>' +
        '<td class="n">' + fmtBig(cache) + '</td>' +
        '<td class="n">' + fmtPct(r.o.hitRate) + '</td>' +
        '<td class="n">' + fmtCost(r.o.cost) + '</td>' +
        '</tr>';
    });
    html += '</tbody></table></div>' +
      '<div style="display:flex;flex-direction:column;gap:12px">' +
        '<div class="card"><div class="card-title">cost share</div><div id="md-donut"></div></div>' +
        '<div class="card"><div class="card-title">top model · 7d trend</div><div id="md-spark"></div>' +
        '<div class="legend">' + legendItem(activeModels[0] ? activeModels[0].m.key : '', modelColor(activeModels[0] ? activeModels[0].i : 0)) + '</div></div>' +
      '</div></div>';

    root.innerHTML = html;

    var activeModels = ranked.filter(function (r) { return r.total > 0; });
    donut(root.querySelector('#md-donut'), {
      slices: activeModels.slice(0, 6).map(function (r) { return { name: r.m.key, value: r.o.cost, color: modelColor(r.i), hitRate: r.o.hitRate }; }),
      center: { value: fmtCost(activeModels.reduce(function (s, r) { return s + r.o.cost; }, 0)), label: 'total cost' }, yFormat: fmtCost
    });

    var topKey = ranked[0] ? ranked[0].m.key : '';
    var trend = DAYS.slice(-7).map(function (d) {
      var t = 0; (d.models || []).forEach(function (m) { if ((m.model || m.key) === topKey) t += (m.inputTokens || 0) + (m.outputTokens || 0) + (m.cacheReadTokens || 0) + (m.cacheCreationTokens || 0); });
      return t;
    });
    lineChart(root.querySelector('#md-spark'), {
      series: [{ name: topKey, color: modelColor(ranked[0] ? ranked[0].i : 0), values: trend }],
      labels: DAYS.slice(-7).map(function (d) { return fmtDate(d.date || d.key); }),
      height: 130, area: true, yFormat: fmtBig,
      tipTitle: function (i) { return fmtDateLong(DAYS.slice(-7)[i].date || DAYS.slice(-7)[i].key); },
      tipExtra: function (i) {
        var d = DAYS.slice(-7)[i]; var inp = 0, cr = 0;
        (d.models || []).forEach(function (m) { if ((m.model || m.key) === topKey) { inp += m.inputTokens || 0; cr += m.cacheReadTokens || 0; } });
        return '<div class="tt-row"><span><i style="background:' + C.cread + '"></i>cache hit</span><b>' + fmtPct(hitRate(inp, cr)) + '</b></div>';
      }
    });
  };

  views.agents = function (root) {
    var days = rangedDays();
    var tot = grandTotals(days);
    var byVendor = {};
    days.forEach(function (day) {
      (day.models || []).forEach(function (md) {
        var key = md.model || md.key;
        var vendor = vendorFromKey(key);
        var v = byVendor[vendor] || (byVendor[vendor] = { tokens: 0, cost: 0, sessions: 0, messages: 0, input: 0, cacheRead: 0 });
        var t = (md.inputTokens || 0) + (md.outputTokens || 0) + (md.cacheReadTokens || 0) + (md.cacheCreationTokens || 0);
        v.tokens += t; v.input += md.inputTokens || 0; v.cacheRead += md.cacheReadTokens || 0;
        var dt = dayTotal(day);
        if (dt > 0) v.cost += (Number(day.cost) || 0) * (t / dt);
        v.messages += Math.round(t / 42000);
      });
    });
    var totalTokens = Object.keys(byVendor).reduce(function (s, k) { return s + byVendor[k].tokens; }, 0) || 1;
    Object.keys(byVendor).forEach(function (k) { byVendor[k].sessions = Math.round((tot.sessions || 0) * (byVendor[k].tokens / totalTokens)); });

    var rows = Object.keys(byVendor).map(function (name, i) {
      var v = byVendor[name];
      return { name: name, i: i, tokens: v.tokens, cost: v.cost, sessions: v.sessions, messages: v.messages, hitRate: hitRate(v.input, v.cacheRead) };
    }).sort(function (a, b) { return b.tokens - a.tokens; });
    var maxT = rows[0] ? rows[0].tokens : 1;

    var html = secLabel('agents', 'by token volume') + '<div class="grid g-2">';
    rows.forEach(function (r) {
      html += '<div class="card">' +
        '<div class="card-title"><span><span class="tok-chip" style="background:' + modelColor(r.i) + '"></span>' + esc(r.name) + '</span><span class="card-note">' + r.sessions + ' sessions</span></div>' +
        '<div style="display:flex;align-items:baseline;gap:14px">' +
          '<span class="kpi-val" style="font-size:26px">' + fmtBig(r.tokens) + '</span>' +
          '<span style="font:400 11px var(--font-mono);color:var(--muted)">' + fmtCost(r.cost) + ' · ' + fmtBig(r.messages) + ' msgs · ' + fmtPct(r.hitRate) + ' hit</span>' +
        '</div>' +
        '<div class="hbar-track" style="margin-top:14px"><div class="hbar-fill" style="width:' + (r.tokens / maxT * 100).toFixed(1) + '%;background:' + modelColor(r.i) + '"></div></div>' +
      '</div>';
    });
    html += '</div>' +
      secLabel('distribution', 'share of window') +
      '<div class="card"><div id="ag-donut"></div></div>';

    root.innerHTML = html;
    donut(root.querySelector('#ag-donut'), {
      slices: rows.map(function (r) { return { name: r.name, value: r.tokens, color: modelColor(r.i), hitRate: r.hitRate }; }),
      center: { value: fmtBig(tot.tokens), label: 'tokens' }, yFormat: fmtBig, size: 190
    });
  };

  var liveTimer = null;
  views.live = function (root) {
    var todayD = DAYS[DAYS.length - 1] || { models: [], sessions: 0, messages: 0, cost: 0 };
    var tTok = dayTotal(todayD), tCost = dayCost(todayD);

    var hours = [], hourLabels = [], hourHR = [];
    for (var h = 0; h < 24; h++) {
      var awake = h >= 7 && h <= 23 ? 1 : 0.08;
      var peak = Math.exp(-Math.pow(h - 15, 2) / 30);
      hours.push(Math.round(tTok / 24 * awake * (0.4 + peak * 1.6)));
      hourLabels.push(String(h).padStart(2, '0'));
      var dhr = hitRate(todayD.models.reduce(function (s, m) { return s + (m.inputTokens || 0); }, 0), todayD.models.reduce(function (s, m) { return s + (m.cacheReadTokens || 0); }, 0));
      hourHR.push(+(dhr * (0.7 + peak * 0.3)).toFixed(2));
    }

    var activeBlock = (BLOCKS || []).filter(function (b) { return b.isActive; })[0];

    var html = secLabel('live', 'today · ' + fmtDate(todayD.date || todayD.key || '')) +
      '<div class="grid g-kpi">' +
        kpiCard('tokens today', fmtBig(tTok), 'across ' + (todayD.sessions || 0) + ' sessions', null, null) +
        kpiCard('cost today', fmtCost(tCost), 'all agents', null, null) +
        kpiCard('cache hit rate', fmtPct(dayHitRate(todayD)), 'today · all agents', hourHR, C.cread) +
        kpiCard('messages', fmtBig(todayD.messages || 0), 'all agents', null, null) +
        kpiCard('burn rate', fmtBig(Math.round(tTok / 9)) + '<small>/hr</small>', 'projected ' + fmtBig(Math.round(tTok * 1.6)) + ' by eod', null, null) +
      '</div>' +
      '<div style="height:12px"></div>' +
      '<div class="grid g-main">' +
        '<div class="card"><div class="card-title">tokens / hour <span class="card-note">today</span></div><div id="lv-hours"></div></div>' +
        '<div class="card"><div class="card-title">5h block <span class="card-note">billing window</span></div>' +
          '<div class="gauge-wrap">' +
            '<svg width="120" height="120" viewBox="0 0 120 120">' +
              '<circle cx="60" cy="60" r="52" fill="none" stroke="var(--panel-2)" stroke-width="9"/>' +
              '<circle cx="60" cy="60" r="52" fill="none" stroke="' + C.accent + '" stroke-width="9" stroke-linecap="round" stroke-dasharray="326.7" stroke-dashoffset="98" transform="rotate(-90 60 60)"/>' +
              '<text x="60" y="58" text-anchor="middle" fill="var(--ink)" style="font:500 20px var(--font-mono)">' + (activeBlock ? '70%' : '—') + '</text>' +
              '<text x="60" y="76" text-anchor="middle" fill="var(--faint)" style="font:400 8.5px var(--font-mono);letter-spacing:.14em">ELAPSED</text>' +
            '</svg>' +
            '<div class="gauge-meta">' +
              '<div class="gauge-row"><span class="gk">started</span><span class="gv">' + (activeBlock ? fmtTime(activeBlock.startTime) : '—') + '</span></div>' +
              '<div class="gauge-row"><span class="gk">resets</span><span class="gv">' + (activeBlock ? fmtTime(activeBlock.endTime) : '—') + '</span></div>' +
              '<div class="gauge-row"><span class="gk">block tokens</span><span class="gv">' + fmtBig(activeBlock ? activeBlock.totalTokens : Math.round(tTok * 0.42)) + '</span></div>' +
              '<div class="gauge-row"><span class="gk">projection</span><span class="gv" style="color:var(--accent)">' + (activeBlock ? 'within limit' : 'no active block') + '</span></div>' +
            '</div>' +
          '</div>' +
        '</div>' +
      '</div>' +
      secLabel('recent activity', 'latest messages') +
      '<div class="card"><div class="feed" id="lv-feed"></div></div>';

    root.innerHTML = html;

    barChart(root.querySelector('#lv-hours'), {
      groups: hours.map(function (v, h) {
        return { label: hourLabels[h], tipTitle: hourLabels[h] + ':00', parts: [{ name: 'tokens', value: v, color: C.accent }] };
      }),
      height: 190, yFormat: fmtBig,
      tipExtra: function (i) { return '<div class="tt-row"><span><i style="background:' + C.cread + '"></i>cache hit</span><b>' + fmtPct(hourHR[i]) + '</b></div>'; }
    });

    var feed = root.querySelector('#lv-feed');
    var feedRows = [];
    var latest = (PULSE && PULSE.latest) ? PULSE.latest : null;
    var recentModels = MODELS.slice(0, 6);
    for (var f = 0; f < 9; f++) {
      var m = recentModels[f % recentModels.length];
      var when = new Date(Date.now() - f * (2 + (f * 37 % 9)) * 60000);
      feedRows.push({ t: when.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' }),
        html: '<b>' + esc(m ? m.key : (latest && latest.model) || 'agent') + '</b>' + (latest && latest.provider ? ' → ' + esc(latest.provider) : ''),
        v: fmtBig(Math.round(20000 + (f * 137 % 400) * 1000)) + ' tok' });
    }
    function renderFeed() {
      feed.innerHTML = feedRows.map(function (r, i) {
        return '<div class="feed-row' + (i === 0 ? ' fresh' : '') + '">' +
          '<span class="ft">' + r.t + '</span><span class="fm">' + r.html + '</span><span class="fv">' + r.v + '</span></div>';
      }).join('');
    }
    renderFeed();

    if (liveTimer) { clearInterval(liveTimer); liveTimer = null; }
    liveTimer = setInterval(function () {
      var m = recentModels[Math.floor(Math.random() * recentModels.length)];
      feedRows.unshift({ t: new Date().toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' }),
        html: '<b>' + esc(m ? m.key : 'agent') + '</b>', v: fmtBig(Math.round(20000 + Math.random() * 400000)) + ' tok' });
      feedRows = feedRows.slice(0, 9);
      renderFeed();
    }, 6000);
  };

  function fmtTime(iso) {
    if (!iso) return '—';
    var d = new Date(iso);
    return d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' });
  }

  var viewEl = document.getElementById('view');

  function mount(route) {
    state.route = route;
    if (liveTimer) { clearInterval(liveTimer); liveTimer = null; }
    document.querySelectorAll('.rail-item').forEach(function (b) { b.classList.toggle('active', b.dataset.route === route); });
    viewEl.innerHTML = '';
    var wrap = el('div', 'view');
    viewEl.appendChild(wrap);
    if (views[route]) views[route](wrap);
    wrap.querySelectorAll('.kpi-spark').forEach(function (elS) { sparkline(elS, JSON.parse(elS.dataset.spark), elS.dataset.color); });
  }

  var rail = document.getElementById('rail');
  if (rail) {
    rail.addEventListener('click', function (ev) { var btn = ev.target.closest('.rail-item'); if (btn) mount(btn.dataset.route); });
  }

  document.addEventListener('keydown', function (ev) {
    if (ev.target.tagName === 'INPUT' || ev.target.tagName === 'TEXTAREA') return;
    var routes = ['overview', 'timeline', 'models', 'agents', 'live'];
    var idx = parseInt(ev.key, 10) - 1;
    if (idx >= 0 && idx < routes.length) mount(routes[idx]);
  });

  function segWire(id, attr, key) {
    var seg = document.getElementById(id);
    if (!seg) return;
    seg.addEventListener('click', function (ev) {
      var btn = ev.target.closest('button');
      if (!btn) return;
      this.querySelectorAll('button').forEach(function (b) { b.classList.remove('active'); });
      btn.classList.add('active');
      state[key] = btn.dataset[attr];
      var win = document.getElementById('rail-window');
      if (win) win.textContent = state.range;
      mount(state.route);
    });
  }
  segWire('seg-range', 'range', 'range');
  segWire('seg-metric', 'metric', 'metric');

  var exportBtn = document.getElementById('export-btn');
  if (exportBtn) {
    exportBtn.addEventListener('click', function () {
      var days = rangedDays();
      var csv = 'date,model,input,output,cache_read,cache_create,total,cost\n';
      days.forEach(function (d) {
        (d.models || []).forEach(function (m) {
          var t = (m.inputTokens || 0) + (m.outputTokens || 0) + (m.cacheReadTokens || 0) + (m.cacheCreationTokens || 0);
          if (!t) return;
          csv += [d.date || d.key, m.model || m.key, m.inputTokens || 0, m.outputTokens || 0, m.cacheReadTokens || 0, m.cacheCreationTokens || 0, t, ''].join(',') + '\n';
        });
      });
      var blob = new Blob([csv], { type: 'text/csv' });
      var a = document.createElement('a');
      a.href = URL.createObjectURL(blob);
      a.download = 'xusage-' + state.range + '.csv';
      a.click();
      URL.revokeObjectURL(a.href);
      toast('exported ' + days.length + ' days · ' + state.range);
    });
  }

  var toastEl = document.getElementById('toast');
  var toastT = null;
  function toast(msg) {
    if (!toastEl) return;
    toastEl.textContent = msg;
    toastEl.classList.add('show');
    clearTimeout(toastT);
    toastT = setTimeout(function () { toastEl.classList.remove('show'); }, 2200);
  }

  function tickClock() {
    var c = document.getElementById('clock');
    if (c) c.textContent = new Date().toLocaleTimeString(undefined, { hour12: false });
  }
  tickClock();
  setInterval(tickClock, 1000);

  var railUpdated = document.getElementById('rail-updated');
  if (railUpdated) railUpdated.textContent = new Date().toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' });

  function boot() {
    Promise.all([
      api('/api/daily'),
      api('/api/models?trend=1'),
      api('/api/summary'),
      api('/api/pulse'),
      api('/api/blocks?limit=8'),
    ]).then(function (res) {
      var daily = res[0] || [];
      var modelRows = res[1] || [];
      SUMMARY = res[2] || {};
      PULSE = res[3] || {};
      BLOCKS = res[4] || {};

      DAYS = daily.map(function (d) {
        var models = (d.models || []).map(function (m) {
          return { model: m.model || m.key, vendor: m.vendor || vendorFromKey(m.model || m.key),
            inputTokens: m.inputTokens || 0, outputTokens: m.outputTokens || 0,
            cacheReadTokens: m.cacheReadTokens || 0, cacheCreationTokens: m.cacheCreationTokens || 0, cost: m.cost || 0 };
        });
        return { date: d.key || d.date, models: models, sessions: d.sessions || 0, messages: d.messages || 0, cost: d.cost || 0 };
      });

      MODELS = buildModels(DAYS, modelRows);

      var win = document.getElementById('rail-window');
      if (win) win.textContent = state.range;
      var updated = document.getElementById('rail-updated');
      if (updated) updated.textContent = new Date().toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' });

      mount('overview');
    }).catch(function () {
      if (viewEl) viewEl.innerHTML = '<div class="empty">Failed to load data. Is the xusage server running?</div>';
    });
  }

  var rzT = null;
  window.addEventListener('resize', function () { clearTimeout(rzT); rzT = setTimeout(function () { mount(state.route); }, 180); });

  boot();
})();
