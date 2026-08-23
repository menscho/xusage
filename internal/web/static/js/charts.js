// xusage charts — XUChart global. Pure SVG, zero dependencies (SPEC §9.1 + §13).
//
// Every renderer takes (container, options) and returns a cleanup function
// that detaches its ResizeObserver and removes the drawn DOM. A shared
// tooltip follows the pointer — the static #tooltip element from index.html
// when present, else a lazily created div (GSAP nested #191919 on #42433d
// hairline, monospaced cream numbers, green title); axes use nice-number
// ticks with mono cream tabular-nums labels; colors come from the caller or
// the GSAP palette (window.XU.colors.categorical wins at draw time when
// app.js is loaded, else the local default — green + category hues, per
// SPEC §13.0).
//
// Renderers:
//   XUChart.line(el,     {series:[{name,color,axis,points:[{x,y}]}], xType:'time'|'index',
//                         yFormat, y2Format, height, stacked})
//       stacked cumulates series in order over the union of x values; the
//       tooltip then reports the cumulative height at that point.
//   XUChart.bars(el,     {groups:[{label,values:[{name,value,color,tip}]}], stacked, yFormat, height})
//       values[].tip (exact text) REPLACES the default tooltip content —
//       used for timestamped token counts (SPEC §13.4). Emits GSAP
//       svg.chart markup: green rounded-top rects, #42433d hairline gridlines,
//       mono cream axis text, 2px #191919 stub for zero buckets, .bar.live
//       pulse on the last non-zero bucket.
//   XUChart.donut(el,    {slices:[{name,value,color}], center:{label,value}, yFormat})
//   XUChart.heatmap(el,  {rowLabels, colLabels, cells:[[v]], yFormat, colorScale})
//   XUChart.hbar(el,     {bars:[{label,value,color,fullName}], yFormat})
//       bars[].fullName rides on the rect as data-model for click-to-detail.
//   XUChart.sparkline(el,{values:[n], color, width, height})
//       minimal single-path chart, no axes, no tooltip; fixed size.
//   XUChart.rankbar(el,  {rows:[{label,value,share,color,tip}], total, yFormat})
//       ranked proportional bars (sorted by value desc) with % labels; width
//       from row.share when given, else value/total (total = sum when absent).
//   XUChart.gauge(el,    {segments:[{name,value,color}], total, yFormat})
//       single stacked horizontal bar + legend chips with exact counts; the
//       track shows sum/total when an explicit total is supplied.
//
// Empty data renders a muted "no data" placeholder instead of an empty plot.

(function () {
  'use strict';

  var NS = 'http://www.w3.org/2000/svg';
  // SPEC §13.0 GSAP categorical order: green first (brand), then category
  // taxonomy hues (orange/pink/lilac/blue — never reused), then cream steps.
  var CATEGORICAL = ['#0ae448', '#ff8709', '#fec5fb', '#9d95ff', '#00bae2',
                     'rgba(255,252,225,0.72)', 'rgba(255,252,225,0.55)', 'rgba(255,252,225,0.40)'];
  var BG = '#0e100f';
  var CARD = '#191919';
  var ELEV = '#42433d';
  var BORDER = '#42433d';
  var TEXT = '#fffce1';
  var TEXT_MUTED = '#7c7c6f';
  var TEXT_FAINT = '#7c7c6f';
  var ACCENT = '#0ae448';
  var FONT = '"Inter", -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif';
  var MONO = '"JetBrains Mono", ui-monospace, "SF Mono", Menlo, Consolas, monospace';

  // Resolved per call (charts.js loads before app.js, so window.XU may not
  // exist yet when this file evaluates).
  function catColor(i) {
    var arr = (window.XU && window.XU.colors && window.XU.colors.categorical) || CATEGORICAL;
    return arr[i % arr.length];
  }

  // ---- shared tooltip -------------------------------------------------------

  var tip = null;

  function tooltip() {
    if (tip && tip.isConnected) { return tip; }
    var el = typeof document.getElementById === 'function'
      ? document.getElementById('tooltip') : null;
    if (!el) {
      el = (tip && !tip.id) ? tip : document.createElement('div');
      document.body.appendChild(el);
    }
    tip = el;
    tip.className = 'tooltip';
    tip.style.position = 'fixed';
    tip.style.pointerEvents = 'none';
    tip.style.zIndex = '50';
    tip.style.display = 'none';
    tip.style.background = CARD;
    tip.style.border = '1px solid ' + BORDER;
    tip.style.borderRadius = '8px';
    tip.style.padding = '6px 9px';
    tip.style.fontFamily = FONT;
    tip.style.fontSize = '12px';
    tip.style.lineHeight = '1.5';
    tip.style.color = TEXT;
    tip.style.whiteSpace = 'nowrap';
    return tip;
  }

  function tipShow(html, clientX, clientY) {
    if (!html) { tipHide(); return; }
    var t = tooltip();
    t.innerHTML = html;
    t.style.display = 'block';
    var r = t.getBoundingClientRect();
    var x = clientX + 12, y = clientY - r.height - 8;
    if (y < 4) { y = clientY + 14; }
    if (x + r.width > window.innerWidth - 8) { x = clientX - r.width - 12; }
    t.style.left = x + 'px';
    t.style.top = y + 'px';
  }

  function tipHide() { if (tip) { tip.style.display = 'none'; } }

  function tipTitle(str) {
    return '<div class="tt-title" style="font-weight:600;margin-bottom:2px;color:' + ACCENT + '">' + esc(str) + '</div>';
  }

  function tipRow(color, label, value) {
    return '<div class="tt-row" style="display:flex;align-items:center;gap:6px">' +
      (color ? '<span class="sw" style="width:8px;height:8px;border-radius:2px;display:inline-block;flex:none;background:' + color + '"></span>' : '') +
      '<span>' + esc(label) + '</span><b style="font-family:' + MONO + ';font-variant-numeric:tabular-nums;font-weight:500">' + value + '</b></div>';
  }

  function hoverTip(node, htmlFn) {
    node.style.cursor = 'pointer';
    node.addEventListener('mousemove', function (ev) {
      tipShow(htmlFn(ev), ev.clientX, ev.clientY);
    });
    node.addEventListener('mouseleave', tipHide);
  }

  function esc(s) {
    return String(s == null ? '' : s)
      .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
  }

  // ---- svg + drawing helpers --------------------------------------------------

  function svgEl(name, attrs) {
    var n = document.createElementNS(NS, name);
    for (var k in attrs) { if (attrs[k] != null) { n.setAttribute(k, attrs[k]); } }
    return n;
  }

  function fmtDefault(n) { return String(Math.round(n * 100) / 100); }

  function niceTicks(max) {
    if (!(max > 0)) { max = 1; }
    var rough = max / 4;
    var pow = Math.pow(10, Math.floor(Math.log(rough) / Math.LN10));
    var step = pow;
    var f = rough / pow;
    if (f >= 5) { step = 5 * pow; }
    else if (f >= 2) { step = 2 * pow; }
    var top = Math.ceil(max / step) * step;
    var ticks = [];
    for (var v = 0; v <= top + step / 2; v += step) { ticks.push(v); }
    return ticks;
  }

  function fmtTick(n) {
    var abs = Math.abs(n);
    if (abs >= 1e9) { return round1(n / 1e9) + 'B'; }
    if (abs >= 1e6) { return round1(n / 1e6) + 'M'; }
    if (abs >= 1e3) { return round1(n / 1e3) + 'k'; }
    return String(Math.round(n * 100) / 100);
  }

  function round1(x) {
    var r = Math.round(x * 10) / 10;
    return String(r);
  }

  function emptyPlot(container, msg) {
    container.innerHTML = '';
    var d = document.createElement('div');
    d.className = 'empty';
    d.textContent = msg || 'no data';
    container.appendChild(d);
    return function () {};
  }

  function watch(container, redraw) {
    var ro = null;
    var frame = 0;
    function schedule() {
      if (frame) { return; }
      frame = requestAnimationFrame(function () { frame = 0; redraw(); });
    }
    if (typeof ResizeObserver === 'function') {
      ro = new ResizeObserver(schedule);
      ro.observe(container);
    } else {
      window.addEventListener('resize', schedule);
    }
    return function () {
      if (ro) { ro.disconnect(); }
      else { window.removeEventListener('resize', schedule); }
      if (frame) { cancelAnimationFrame(frame); frame = 0; }
      tipHide();
    };
  }

  function cleanupFor(container, extra) {
    return function () {
      if (typeof extra === 'function') { try { extra(); } catch (e) {} }
      container.innerHTML = '';
    };
  }

  // axis-txt: mono faint label used by bars()/line().
  function axisTxt(x, y, str, anchor) {
    var t = svgEl('text', { x: x, y: y, 'text-anchor': anchor || 'start', fill: TEXT,
      'font-size': 10.5, 'font-variant-numeric': 'tabular-nums', 'font-family': MONO });
    t.textContent = str;
    return t;
  }

  // ---- line -----------------------------------------------------------------

  function line(container, opts) {
    opts = opts || {};
    var series = (opts.series || []).filter(function (s) { return s && s.points && s.points.length; });
    if (!series.length) { return emptyPlot(container); }
    var H = opts.height || 220;
    var fmtY = opts.yFormat || fmtDefault;

    var plotted = series;
    if (opts.stacked) {
      var seen = {};
      series.forEach(function (s) {
        s.points.forEach(function (p) { seen[p.x] = true; });
      });
      var union = Object.keys(seen).map(function (k) { return +k; })
        .sort(function (a, b) { return a - b; });
      plotted = series.map(function (s) {
        var at = {};
        s.points.forEach(function (p) { at[p.x] = p.y || 0; });
        var run = 0;
        return {
          name: s.name, color: s.color, axis: s.axis,
          points: union.map(function (x) { run += (at[x] || 0); return { x: x, y: run }; })
        };
      });
    }

    var draw = function () {
      var W = Math.max(container.clientWidth || 0, 120);
      var m = { top: 10, right: opts.y2Format ? 52 : 14, bottom: 24, left: 52 };
      var iw = Math.max(W - m.left - m.right, 10);
      var ih = Math.max(H - m.top - m.bottom, 10);

      var xs = [], ys = [], ys2 = [];
      plotted.forEach(function (s) {
        s.points.forEach(function (p) {
          xs.push(p.x);
          (s.axis === 'right' ? ys2 : ys).push(p.y);
        });
      });
      var xMin = Math.min.apply(null, xs), xMax = Math.max.apply(null, xs);
      if (xMin === xMax) { xMax = xMin + 1; }
      var lMax = Math.max.apply(null, ys.concat([0]));
      var lTicks = niceTicks(lMax);
      var lTop = lTicks[lTicks.length - 1] || 1;
      var rTop = 0, rTicks = [];
      if (opts.y2Format && ys2.length) {
        rTicks = niceTicks(Math.max.apply(null, ys2.concat([0])));
        rTop = rTicks[rTicks.length - 1] || 1;
      }

      var time = opts.xType !== 'index';
      var X = function (x) { return m.left + ((x - xMin) / (xMax - xMin)) * iw; };
      var Y = function (y) { return m.top + ih - (y / lTop) * ih; };
      var Y2 = function (y) { return opts.y2Format ? m.top + ih - (y / rTop) * ih : Y(y); };

      var svg = svgEl('svg', { width: W, height: H, viewBox: '0 0 ' + W + ' ' + H, role: 'img' });
      svg.style.fontFamily = FONT;

      lTicks.forEach(function (t) {
        var y = Y(t);
        svg.appendChild(svgEl('line', { x1: m.left, x2: m.left + iw, y1: y, y2: y,
          stroke: ELEV, 'stroke-width': 1, 'stroke-dasharray': t === 0 ? '0' : '3 3',
          'stroke-opacity': t === 0 ? 1 : 0.6 }));
        svg.appendChild(axisTxt(m.left - 6, y + 3.5, fmtTick(t), 'end'));
      });
      if (opts.y2Format && ys2.length) {
        rTicks.forEach(function (t) {
          svg.appendChild(axisTxt(m.left + iw + 6, Y2(t) + 3.5, fmtTick(t), 'start'));
        });
      }
      var tickCount = Math.min(6, Math.max(2, Math.floor(iw / 90)));
      for (var i = 0; i <= tickCount; i++) {
        var xv = xMin + ((xMax - xMin) * i) / tickCount;
        var label;
        if (time) {
          var d = new Date(xv);
          label = (d.getMonth() + 1) + '/' + d.getDate();
        } else {
          label = String(Math.round(xv));
        }
        svg.appendChild(axisTxt(X(xv), H - 8, label, 'middle'));
      }

      plotted.forEach(function (s, si) {
        var fy = s.axis === 'right' ? Y2 : Y;
        var pts = s.points.slice().sort(function (a, b) { return a.x - b.x; });
        var color = s.color || catColor(si);
        var d = '';
        pts.forEach(function (p, i) {
          d += (i ? 'L' : 'M') + X(p.x).toFixed(1) + ',' + fy(p.y).toFixed(1);
        });
        svg.appendChild(svgEl('path', { d: d, fill: 'none', stroke: color,
          'stroke-width': 1.8, 'stroke-linejoin': 'round', 'stroke-linecap': 'round' }));
        pts.forEach(function (p) {
          var c = svgEl('circle', { cx: X(p.x), cy: fy(p.y), r: 8, fill: 'transparent' });
          hoverTip(c, function () {
            return tipTitle(s.name) + tipRow(color,
              time ? new Date(p.x).toLocaleDateString() : String(p.x), fmtY(p.y));
          });
          c.style.cursor = 'crosshair';
          svg.appendChild(c);
        });
      });

      container.innerHTML = '';
      container.appendChild(svg);
    };

    draw();
    var stop = watch(container, draw);
    return cleanupFor(container, stop);
  }

  // ---- bars (grouped / stacked) — GSAP svg.chart markup (§13.0) --------------

  function bars(container, opts) {
    opts = opts || {};
    var groups = (opts.groups || []).filter(function (g) { return g; });
    if (!groups.length) { return emptyPlot(container); }
    var H = opts.height || 240;
    var fmtY = opts.yFormat || fmtDefault;
    var stacked = !!opts.stacked;

    var draw = function () {
      var W = Math.max(container.clientWidth || 0, 120);
      var m = { top: 10, right: 14, bottom: 26, left: 52 };
      var iw = Math.max(W - m.left - m.right, 10);
      var ih = Math.max(H - m.top - m.bottom, 10);

      // series names in stable first-seen order
      var names = [];
      var seen = {};
      groups.forEach(function (g) {
        (g.values || []).forEach(function (v) {
          if (!seen[v.name]) { seen[v.name] = true; names.push(v.name); }
        });
      });
      var colorOf = {};
      var ci = 0;
      groups.forEach(function (g) {
        (g.values || []).forEach(function (v) {
          if (v.color) { colorOf[v.name] = v.color; }
        });
      });
      // Default fill is green; stacked series step through cream opacities.
      names.forEach(function (n) {
        if (!colorOf[n]) { colorOf[n] = catColor(ci++); }
      });

      var totals = groups.map(function (g) {
        if (stacked) {
          return (g.values || []).reduce(function (a, v) { return a + (v.value || 0); }, 0);
        }
        return (g.values || []).reduce(function (a, v) { return Math.max(a, v.value || 0); }, 0);
      });
      var ticks = niceTicks(Math.max.apply(null, totals.concat([0])));
      var top = ticks[ticks.length - 1] || 1;

      var svg = svgEl('svg', { 'class': 'chart', width: W, height: H, viewBox: '0 0 ' + W + ' ' + H });
      svg.style.fontFamily = FONT;

      // dashed gridlines (#42433d) + solid baseline (#42433d)
      ticks.forEach(function (t) {
        var y = m.top + ih - (t / top) * ih;
        svg.appendChild(svgEl('line', { x1: m.left, x2: m.left + iw, y1: y, y2: y,
          stroke: t === 0 ? BORDER : ELEV, 'stroke-width': 1,
          'stroke-dasharray': t === 0 ? '0' : '3 3' }));
        svg.appendChild(axisTxt(m.left - 6, y + 3.5, fmtTick(t), 'end'));
      });

      var slot = iw / groups.length;
      var barW = Math.min(slot * 0.7, stacked ? 42 : 14);

      // last non-zero bucket index (for .bar.live pulse)
      var lastNonZero = -1;
      (function () {
        for (var gi = groups.length - 1; gi >= 0; gi--) {
          var vals = groups[gi].values || [];
          for (var vi = 0; vi < vals.length; vi++) {
            if ((vals[vi].value || 0) > 0) { lastNonZero = gi; gi = -1; break; }
          }
        }
      })();

      groups.forEach(function (g, gi) {
        var cx = m.left + slot * gi + slot / 2;
        var vals = g.values || [];
        // the last non-zero bucket carries the .live pulse on its topmost bar
        var isLive = gi === lastNonZero;
        if (stacked) {
          var acc = 0;
          vals.forEach(function (v, vi) {
            var h = ((v.value || 0) / top) * ih;
            var y = m.top + ih - acc - h;
            var fill = v.color || colorOf[v.name];
            var live = isLive && vi === vals.length - 1;
            var r = svgEl('rect', { 'class': 'bar' + (live ? ' live' : ''), x: cx - barW / 2, y: y, width: barW,
              height: Math.max(h, 0), fill: fill, rx: Math.min(2, barW / 2) });
            if (h >= 1) { attachBarTip(r, v, g, fill); }
            svg.appendChild(r);
            acc += h;
          });
          // zero bucket → 2px stub (#191919)
          if (!(acc > 0)) {
            svg.appendChild(svgEl('rect', { 'class': 'bar', x: cx - barW / 2,
              y: m.top + ih - 2, width: barW, height: 2, fill: '#191919', rx: 1 }));
          }
        } else {
          var n = Math.max(vals.length, 1);
          var w = barW / n;
          vals.forEach(function (v, vi) {
            var h = ((v.value || 0) / top) * ih;
            var x = cx - barW / 2 + vi * w;
            var fill = v.color || colorOf[v.name];
            var live = isLive && vi === vals.length - 1;
            if (h < 1) {
              // zero bucket → 2px stub (#191919)
              svg.appendChild(svgEl('rect', { 'class': 'bar', x: x, y: m.top + ih - 2,
                width: Math.max(w - 1, 1), height: 2, fill: '#191919', rx: 1 }));
            } else {
              var r = svgEl('rect', { 'class': 'bar' + (live ? ' live' : ''), x: x, y: m.top + ih - h,
                width: Math.max(w - 1, 1), height: h, fill: fill, rx: Math.min(2, w / 2) });
              attachBarTip(r, v, g, fill);
              svg.appendChild(r);
            }
          });
        }
        // x label: skip when crowded
        if (groups.length <= Math.floor(iw / 60) || groups.length <= 8 || gi % 2 === 0) {
          svg.appendChild(axisTxt(cx, H - 8, String(g.label == null ? '' : g.label), 'middle'));
        }
      });

      container.innerHTML = '';
      container.appendChild(svg);
    };

    function attachBarTip(node, v, g, color) {
      hoverTip(node, function () {
        if (v.tip != null) { return tipTitle(v.tip); }
        return tipRow(color, v.name + ' — ' + g.label, fmtY(v.value));
      });
    }

    draw();
    var stop = watch(container, draw);
    return cleanupFor(container, stop);
  }

  // ---- donut -----------------------------------------------------------------

  function donut(container, opts) {
    opts = opts || {};
    var slices = (opts.slices || []).filter(function (s) { return s && (s.value || 0) > 0; });
    if (!slices.length) { return emptyPlot(container); }
    var fmtY = opts.yFormat || fmtDefault;
    var total = slices.reduce(function (a, s) { return a + (s.value || 0); }, 0);

    var draw = function () {
      var W = Math.max(container.clientWidth || 0, 160);
      var H = 220;
      var R = Math.min(W, H) / 2 - 8;
      var cx = W / 2, cy = H / 2;
      var svg = svgEl('svg', { width: W, height: H, viewBox: '0 0 ' + W + ' ' + H });
      svg.style.fontFamily = FONT;

      var a0 = -Math.PI / 2;
      slices.forEach(function (s, i) {
        var frac = total > 0 ? (s.value || 0) / total : 0;
        var a1 = a0 + frac * Math.PI * 2;
        var large = a1 - a0 > Math.PI ? 1 : 0;
        var rOut = R, rIn = R * 0.62;
        var p = function (r, a) { return (cx + r * Math.cos(a)).toFixed(2) + ',' + (cy + r * Math.sin(a)).toFixed(2); };
        var color = s.color || catColor(i);
        var d = 'M' + p(rOut, a0) +
          ' A' + rOut + ',' + rOut + ' 0 ' + large + ' 1 ' + p(rOut, a1) +
          ' L' + p(rIn, a1) +
          ' A' + rIn + ',' + rIn + ' 0 ' + large + ' 0 ' + p(rIn, a0) + ' Z';
        var path = svgEl('path', { d: d, fill: color, stroke: BG, 'stroke-width': 1 });
        hoverTip(path, function () {
          return tipRow(color, s.name, fmtY(s.value) + ' (' + Math.round(frac * 100) + '%)');
        });
        svg.appendChild(path);
        a0 = a1;
      });

      if (opts.center) {
        var t1 = svgEl('text', { x: cx, y: cy - 4, 'text-anchor': 'middle', fill: TEXT_FAINT, 'font-size': 10.5 });
        t1.textContent = opts.center.label || '';
        var t2 = svgEl('text', { x: cx, y: cy + 12, 'text-anchor': 'middle', fill: TEXT,
          'font-size': 14, 'font-weight': 600, 'font-variant-numeric': 'tabular-nums' });
        t2.textContent = opts.center.value || '';
        svg.appendChild(t1);
        svg.appendChild(t2);
      }

      container.innerHTML = '';
      container.appendChild(svg);
    };

    draw();
    var stop = watch(container, draw);
    return cleanupFor(container, stop);
  }

  // ---- heatmap -----------------------------------------------------------------

  function heatmap(container, opts) {
    opts = opts || {};
    var rows = opts.rowLabels || [];
    var cols = opts.colLabels || [];
    var cells = opts.cells || [];
    var fmtY = opts.yFormat || fmtDefault;
    var scale = opts.colorScale || function (v) {
      // GSAP ramp: canvas (#0e100f) -> green (#0ae448)
      var t = Math.max(0, Math.min(1, v));
      var from = [14, 16, 15], to = [10, 224, 72];
      var c = from.map(function (f, i) { return Math.round(f + (to[i] - f) * t); });
      return 'rgb(' + c.join(',') + ')';
    };

    var flat = [];
    cells.forEach(function (row) { (row || []).forEach(function (v) { if (v > 0) { flat.push(v); } }); });
    if (!rows.length || !cols.length || !flat.length) { return emptyPlot(container); }
    var max = Math.max.apply(null, flat);

    var draw = function () {
      var W = Math.max(container.clientWidth || 0, 200);
      var m = { top: 22, right: 8, bottom: 4, left: 78 };
      var cw = Math.max((W - m.left - m.right) / cols.length, 8);
      var ch = 17;
      var H = m.top + rows.length * ch + m.bottom;
      var svg = svgEl('svg', { width: W, height: H, viewBox: '0 0 ' + W + ' ' + H });
      svg.style.fontFamily = FONT;

      cols.forEach(function (c, i) {
        if (i % 3 === 0 || cols.length <= 12) {
          svg.appendChild(axisTxt(m.left + cw * i + cw / 2, m.top - 8, String(c), 'middle'));
        }
      });
      rows.forEach(function (r, ri) {
        svg.appendChild(axisTxt(m.left - 6, m.top + ch * ri + ch / 2 + 3.5, String(r), 'end'));
        var row = cells[ri] || [];
        for (var ci = 0; ci < cols.length; ci++) {
          var v = row[ci] || 0;
          var rect = svgEl('rect', {
            x: m.left + cw * ci + 0.5, y: m.top + ch * ri + 0.5,
            width: Math.max(cw - 1.5, 1), height: ch - 2.5,
            rx: 2,
            fill: v > 0 ? scale(v / max) : ELEV
          });
          (function (v, r, c) {
            hoverTip(rect, function () {
              if (!(v > 0)) { return ''; }
              return tipRow(scale(v / max), r + ' ' + c, fmtY(v));
            });
          })(v, String(r), String(cols[ci]));
          svg.appendChild(rect);
        }
      });

      container.innerHTML = '';
      container.appendChild(svg);
    };

    draw();
    var stop = watch(container, draw);
    return cleanupFor(container, stop);
  }

  // ---- hbar (ranking) -------------------------------------------------------------

  function hbar(container, opts) {
    opts = opts || {};
    var items = (opts.bars || []).filter(function (b) { return b; });
    if (!items.length) { return emptyPlot(container); }
    var fmtY = opts.yFormat || fmtDefault;
    var max = Math.max(Math.max.apply(null, items.map(function (b) { return b.value || 0; })), 0);
    if (max <= 0) { return emptyPlot(container); }

    var draw = function () {
      var W = Math.max(container.clientWidth || 0, 200);
      var labelW = Math.min(Math.max(90, W * 0.34), 190);
      var rowH = 24;
      var H = items.length * rowH + 6;
      var svg = svgEl('svg', { width: W, height: H, viewBox: '0 0 ' + W + ' ' + H });
      svg.style.fontFamily = FONT;

      items.forEach(function (b, i) {
        var y = i * rowH + 3;
        var label = String(b.label == null ? '' : b.label);
        var t = axisTxt(labelW - 8, y + 12, label.length > 26 ? label.slice(0, 25) + '…' : label, 'end');
        svg.appendChild(t);
        var bw = Math.max(((b.value || 0) / max) * (W - labelW - 64), 1);
        var color = b.color || catColor(i);
        var r = svgEl('rect', { x: labelW, y: y + 3, width: bw, height: rowH - 12, rx: 2, fill: color });
        if (b.fullName != null) { r.setAttribute('data-model', String(b.fullName)); }
        hoverTip(r, function () {
          return tipRow(color, label, fmtY(b.value));
        });
        svg.appendChild(r);
        var v = axisTxt(labelW + bw + 6, y + 12, fmtY(b.value), 'start');
        svg.appendChild(v);
      });

      container.innerHTML = '';
      container.appendChild(svg);
    };

    draw();
    var stop = watch(container, draw);
    return cleanupFor(container, stop);
  }

  // ---- sparkline (minimal, no axes, no tooltip) --------------------------------

  function sparkline(container, opts) {
    opts = opts || {};
    var values = (opts.values || []).map(function (v) { return +v || 0; });
    var W = opts.width || 60;
    var H = opts.height || 14;
    var color = opts.color || catColor(0);

    container.innerHTML = '';
    if (!values.length) { return function () {}; }

    var min = Math.min.apply(null, values);
    var max = Math.max.apply(null, values);
    var span = max - min;
    var x = function (i) {
      return values.length > 1 ? 1.5 + (i * (W - 3)) / (values.length - 1) : W / 2;
    };
    var y = function (v) { return span > 0 ? 1.5 + (H - 3) * (1 - (v - min) / span) : H / 2; };

    var svg = svgEl('svg', { width: W, height: H, viewBox: '0 0 ' + W + ' ' + H, 'aria-hidden': 'true' });
    svg.style.display = 'block';
    if (values.length === 1) {
      svg.appendChild(svgEl('circle', { cx: x(0), cy: y(values[0]), r: 2, fill: color }));
    } else {
      var d = '';
      values.forEach(function (v, i) {
        d += (i ? 'L' : 'M') + x(i).toFixed(1) + ',' + y(v).toFixed(1);
      });
      svg.appendChild(svgEl('path', { d: d, fill: 'none', stroke: color,
        'stroke-width': 1.5, 'stroke-linecap': 'round', 'stroke-linejoin': 'round' }));
    }
    container.appendChild(svg);
    return function () { container.innerHTML = ''; };
  }

  // ---- rankbar (ranked proportional bars with % labels) -------------------------

  function rankbar(container, opts) {
    opts = opts || {};
    var rows = (opts.rows || []).filter(function (r) { return r; });
    if (!rows.length) { return emptyPlot(container); }
    var fmtY = opts.yFormat || fmtDefault;
    var sorted = rows.slice().sort(function (a, b) { return (b.value || 0) - (a.value || 0); });
    var sum = sorted.reduce(function (a, r) { return a + (r.value || 0); }, 0);
    var total = opts.total != null ? opts.total : sum;

    var draw = function () {
      var W = Math.max(container.clientWidth || 0, 160);
      var labelW = Math.min(Math.max(80, W * 0.28), 170);
      var pctW = 44;
      var rowH = 22;
      var H = sorted.length * rowH + 4;
      var trackW = Math.max(W - labelW - pctW - 12, 10);
      var svg = svgEl('svg', { width: W, height: H, viewBox: '0 0 ' + W + ' ' + H });
      svg.style.fontFamily = FONT;

      sorted.forEach(function (r, i) {
        var y = i * rowH + 3;
        var frac = r.share != null ? r.share : (total > 0 ? (r.value || 0) / total : 0);
        frac = Math.max(0, Math.min(1, frac));
        var color = r.color || catColor(i);
        var label = String(r.label == null ? '' : r.label);

        svg.appendChild(axisTxt(labelW - 8, y + 11, label.length > 24 ? label.slice(0, 23) + '…' : label, 'end'));
        svg.appendChild(svgEl('rect', { x: labelW, y: y + 4, width: trackW, height: rowH - 12,
          rx: 2, fill: ELEV }));
        var bw = frac > 0 ? Math.max(frac * trackW, 2) : 0;
        if (bw > 0) {
          var rect = svgEl('rect', { x: labelW, y: y + 4, width: bw, height: rowH - 12,
            rx: 2, fill: color });
          hoverTip(rect, function () {
            if (r.tip != null) { return tipTitle(r.tip); }
            return tipRow(color, label, fmtY(r.value || 0) + ' · ' + Math.round(frac * 100) + '%');
          });
          svg.appendChild(rect);
        }
        svg.appendChild(axisTxt(labelW + trackW + 8, y + 11, Math.round(frac * 100) + '%', 'start'));
      });

      container.innerHTML = '';
      container.appendChild(svg);
    };

    draw();
    var stop = watch(container, draw);
    return cleanupFor(container, stop);
  }

  // ---- gauge (single stacked horizontal bar + legend chips) ----------------------

  function gauge(container, opts) {
    opts = opts || {};
    var segments = (opts.segments || []).filter(function (s) { return s; });
    if (!segments.length) { return emptyPlot(container); }
    var fmtY = opts.yFormat || fmtDefault;
    var sum = segments.reduce(function (a, s) { return a + (s.value || 0); }, 0);
    var total = Math.max(opts.total != null ? opts.total : sum, sum);
    if (!(total > 0)) { return emptyPlot(container); }

    var draw = function () {
      var W = Math.max(container.clientWidth || 0, 160);
      var barH = 10;
      var svg = svgEl('svg', { width: W, height: barH, viewBox: '0 0 ' + W + ' ' + barH });
      svg.style.fontFamily = FONT;
      svg.appendChild(svgEl('rect', { x: 0, y: 0, width: W, height: barH, rx: 2, fill: ELEV }));

      var x = 0;
      segments.forEach(function (s, i) {
        var frac = total > 0 ? (s.value || 0) / total : 0;
        var w = frac * W;
        var color = s.color || catColor(i);
        if (w > 0) {
          var rect = svgEl('rect', { x: x.toFixed(2), y: 0, width: w.toFixed(2), height: barH,
            fill: color, stroke: BG, 'stroke-width': 1 });
          hoverTip(rect, function () {
            return tipRow(color, s.name, fmtY(s.value || 0) + ' · ' + Math.round(frac * 100) + '%');
          });
          svg.appendChild(rect);
        }
        x += w;
      });
      container.innerHTML = '';
      container.appendChild(svg);

      var legend = document.createElement('div');
      legend.className = 'gauge-legend';
      legend.style.display = 'flex';
      legend.style.flexWrap = 'wrap';
      legend.style.gap = '4px 14px';
      legend.style.marginTop = '8px';
      segments.forEach(function (s, i) {
        var frac = total > 0 ? (s.value || 0) / total : 0;
        var color = s.color || catColor(i);
        var chip = document.createElement('span');
        chip.className = 'gauge-key';
        chip.style.display = 'inline-flex';
        chip.style.alignItems = 'center';
        chip.style.gap = '6px';
        chip.style.fontSize = '11px';
        chip.style.color = TEXT_MUTED;
        chip.style.whiteSpace = 'nowrap';
        var sw = document.createElement('span');
        sw.style.width = '8px';
        sw.style.height = '8px';
        sw.style.borderRadius = '2px';
        sw.style.display = 'inline-block';
        sw.style.flex = 'none';
        sw.style.background = color;
        chip.appendChild(sw);
        var name = document.createElement('span');
        name.textContent = s.name;
        chip.appendChild(name);
        var val = document.createElement('b');
        val.textContent = fmtY(s.value || 0);
        val.style.fontFamily = MONO;
        val.style.fontVariantNumeric = 'tabular-nums';
        val.style.fontWeight = '500';
        val.style.color = TEXT;
        chip.appendChild(val);
        hoverTip(chip, function () {
          return tipRow(color, s.name, fmtY(s.value || 0) + ' · ' + Math.round(frac * 100) + '%');
        });
        legend.appendChild(chip);
      });
      container.appendChild(legend);
    };

    draw();
    var stop = watch(container, draw);
    return cleanupFor(container, stop);
  }

  // ---- export -----------------------------------------------------------------

  window.XUChart = {
    line: line,
    bars: bars,
    donut: donut,
    heatmap: heatmap,
    hbar: hbar,
    sparkline: sparkline,
    rankbar: rankbar,
    gauge: gauge
  };
})();
