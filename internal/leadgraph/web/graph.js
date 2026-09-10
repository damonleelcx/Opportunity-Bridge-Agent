// Lead Graph — the enhancement layer.
//
// WHY THE LAYOUT IS SOLVED SYNCHRONOUSLY AND THEN DRAWN ONCE
//   A force layout that settles inside requestAnimationFrame only settles while
//   the tab is visible. Rendered into a hidden tab, a print, a screenshot or a
//   throttled background it draws its first chaotic frame and stops - and that
//   frame looks like a finished picture. So the simulation is run to completion
//   BEFORE the first paint. Dragging re-solves locally; nothing animates on its
//   own, which also means prefers-reduced-motion needs no special case.
//
// WHAT IT MAY NOT ENCODE
//   Colour = kind. Dashes = not confirmed. Everything else is constant. Node
//   radius in particular is a constant, deliberately: a bigger circle is a
//   claim about a person that nobody can argue with because it never says what
//   it means.
(function () {
  "use strict";
  var data = window.__LEAD_GRAPH__;
  if (!data || !data.nodes || !data.nodes.length) return;

  var stage = document.getElementById("stage");
  var svg = document.getElementById("graph");
  var panel = document.getElementById("panel");
  if (!stage || !svg || !panel) return;

  var R = 7;                       // every node, always
  var W = 1000, H = 620;
  var nodes = data.nodes.map(function (n, i) {
    var a = (i / data.nodes.length) * Math.PI * 2;
    return { d: n, x: W / 2 + Math.cos(a) * 220, y: H / 2 + Math.sin(a) * 180, vx: 0, vy: 0 };
  });
  var index = {};
  nodes.forEach(function (n) { index[n.d.id] = n; });
  var links = data.links.filter(function (l) { return index[l.from] && index[l.to]; });

  function solve(iterations) {
    for (var step = 0; step < iterations; step++) {
      for (var i = 0; i < nodes.length; i++) {
        var a = nodes[i];
        if (a.fixed) continue;
        for (var j = i + 1; j < nodes.length; j++) {
          var b = nodes[j];
          var dx = a.x - b.x, dy = a.y - b.y;
          var d2 = dx * dx + dy * dy || 0.01;
          var f = 2600 / d2;
          var d = Math.sqrt(d2);
          var ux = (dx / d) * f, uy = (dy / d) * f;
          a.vx += ux; a.vy += uy;
          if (!b.fixed) { b.vx -= ux; b.vy -= uy; }
        }
        a.vx += (W / 2 - a.x) * 0.0016;
        a.vy += (H / 2 - a.y) * 0.0016;
      }
      links.forEach(function (l) {
        var a = index[l.from], b = index[l.to];
        var dx = b.x - a.x, dy = b.y - a.y;
        var d = Math.sqrt(dx * dx + dy * dy) || 0.01;
        var f = (d - 120) * 0.02;
        var ux = (dx / d) * f, uy = (dy / d) * f;
        if (!a.fixed) { a.vx += ux; a.vy += uy; }
        if (!b.fixed) { b.vx -= ux; b.vy -= uy; }
      });
      nodes.forEach(function (n) {
        if (n.fixed) { n.vx = n.vy = 0; return; }
        n.x += (n.vx *= 0.82); n.y += (n.vy *= 0.82);
        n.x = Math.max(R * 3, Math.min(W - R * 3, n.x));
        n.y = Math.max(R * 3, Math.min(H - R * 3, n.y));
      });
    }
  }

  var ns = "http://www.w3.org/2000/svg";
  var gLinks = document.createElementNS(ns, "g");
  var gNodes = document.createElementNS(ns, "g");
  var root = document.createElementNS(ns, "g");
  root.appendChild(gLinks); root.appendChild(gNodes);
  svg.appendChild(root);
  svg.setAttribute("viewBox", "0 0 " + W + " " + H);

  var linkEls = links.map(function (l) {
    var el = document.createElementNS(ns, "line");
    el.setAttribute("class", "link" + (l.corroboration === "hearsay" ? " hearsay" : ""));
    gLinks.appendChild(el);
    return el;
  });
  var nodeEls = nodes.map(function (n) {
    var g = document.createElementNS(ns, "g");
    var cls = "node k-" + n.d.kind + (n.d.presence === "mentioned" ? " mentioned" : "");
    g.setAttribute("class", cls);
    g.setAttribute("tabindex", "0");
    var c = document.createElementNS(ns, "circle");
    c.setAttribute("r", R);
    var t = document.createElementNS(ns, "text");
    t.setAttribute("x", R + 4); t.setAttribute("y", 4);
    t.textContent = n.d.label;
    g.appendChild(c); g.appendChild(t);
    g.addEventListener("pointerdown", function (e) { start(e, n, g); });
    g.addEventListener("click", function () { select(n); });
    g.addEventListener("keydown", function (e) { if (e.key === "Enter" || e.key === " ") { select(n); e.preventDefault(); } });
    gNodes.appendChild(g);
    return g;
  });

  function draw() {
    links.forEach(function (l, i) {
      var a = index[l.from], b = index[l.to];
      linkEls[i].setAttribute("x1", a.x); linkEls[i].setAttribute("y1", a.y);
      linkEls[i].setAttribute("x2", b.x); linkEls[i].setAttribute("y2", b.y);
    });
    nodes.forEach(function (n, i) {
      nodeEls[i].setAttribute("transform", "translate(" + n.x + "," + n.y + ")");
    });
  }

  var drag = null;
  function start(e, n, g) {
    drag = { n: n, g: g };
    n.fixed = true;
    g.setPointerCapture(e.pointerId);
    e.preventDefault();
  }
  svg.addEventListener("pointermove", function (e) {
    if (!drag) return;
    var p = point(e);
    drag.n.x = p.x; drag.n.y = p.y;
    solve(6); draw();
  });
  svg.addEventListener("pointerup", function () {
    if (drag) { drag.n.fixed = false; drag = null; }
  });
  function point(e) {
    var r = svg.getBoundingClientRect();
    var vb = svg.viewBox.baseVal;
    return { x: vb.x + ((e.clientX - r.left) / r.width) * vb.width,
             y: vb.y + ((e.clientY - r.top) / r.height) * vb.height };
  }

  svg.addEventListener("wheel", function (e) {
    e.preventDefault();
    var vb = svg.viewBox.baseVal;
    var k = e.deltaY > 0 ? 1.1 : 0.9;
    var p = point(e);
    var w = Math.max(200, Math.min(3000, vb.width * k));
    var h = w * (H / W);
    svg.setAttribute("viewBox", (p.x - (p.x - vb.x) * (w / vb.width)) + " " +
      (p.y - (p.y - vb.y) * (h / vb.height)) + " " + w + " " + h);
  }, { passive: false });

  function esc(s) { var d = document.createElement("div"); d.textContent = s == null ? "" : s; return d.innerHTML; }

  // 名词字典. The same words the page's own markup uses and the same words the
  // conversation's result cards use (web/static/i18n.js). This panel used to
  // print the raw enum — "person", "hearsay" — on an otherwise Chinese screen.
  // See docs/20-lead-graph.zh-CN.md §3.
  var KIND = { org: "公司", unit: "组织单元", person: "人", event: "组织变动", lead: "线索" };
  var CORR = { hearsay: "听说", corroborated: "多源印证", announced: "官方公布" };
  var CONTACT = { phone: "电话", email: "邮箱", wechat: "微信", other: "其他" };
  // The names of the fields still worth chasing (store.go, unconfirmable).
  var FIELD = { role_title: "职务", duty: "职责", org: "公司", occurred_at: "发生时间" };

  // word falls back to the value itself, not to blank: a state this page has
  // not been taught about is something the reader should see.
  function word(table, v) { return v ? (table[v] || v) : ""; }

  function select(n) {
    nodeEls.forEach(function (el) { el.classList.remove("sel"); });
    nodeEls[nodes.indexOf(n)].classList.add("sel");
    var d = n.d, rows = "";
    function row(k, v) { if (v) rows += "<dt>" + esc(k) + "</dt><dd>" + esc(v) + "</dd>"; }
    row("类别", word(KIND, d.kind));
    row("公司", d.org);
    row("职务", d.role_title);
    row("职责", d.duty);
    row("证实", word(CORR, d.corroboration));
    if (d.unconfirmed && d.unconfirmed.length) {
      row("待确认", d.unconfirmed.map(function (f) { return word(FIELD, f); }).join("、"));
    }
    if (d.presence === "mentioned") row("状态", "只被提过，没有记录");
    if (d.contacts && d.contacts.length) {
      row("联系方式", d.contacts.map(function (c) { return word(CONTACT, c.kind) + " " + c.value; }).join("、"));
    }
    row("我的备注", d.note);
    var mine = data.links.filter(function (l) {
      return (l.from === d.id || l.to === d.id) && l.mine;
    });
    if (mine.length) row("我的关系强度", mine.map(function (l) { return l.strength; }).join("、"));
    panel.innerHTML = "<h2>" + esc(d.label) + "</h2><dl class=\"kv\">" + rows + "</dl>";
  }

  // Category filter. Dimming rather than removing: a node that vanishes takes
  // its lines with it and the shape of the graph changes under the reader.
  var active = {};
  document.querySelectorAll(".filters button[data-kind]").forEach(function (b) {
    b.addEventListener("click", function () {
      var k = b.getAttribute("data-kind");
      if (k === "all") { active = {}; }
      else { active[k] = !active[k]; }
      var any = Object.keys(active).some(function (x) { return active[x]; });
      document.querySelectorAll(".filters button[data-kind]").forEach(function (o) {
        var ok = o.getAttribute("data-kind");
        o.setAttribute("aria-pressed", ok === "all" ? String(!any) : String(!!active[ok]));
      });
      nodes.forEach(function (n, i) {
        nodeEls[i].classList.toggle("dim", any && !active[n.d.kind]);
      });
      links.forEach(function (l, i) {
        var on = !any || (active[index[l.from].d.kind] && active[index[l.to].d.kind]);
        linkEls[i].classList.toggle("dim", !on);
      });
    });
  });

  var reset = document.getElementById("reset");
  if (reset) reset.addEventListener("click", function () {
    svg.setAttribute("viewBox", "0 0 " + W + " " + H);
    nodes.forEach(function (n) { n.fixed = false; });
    solve(300); draw();
  });

  // Settle first, reveal second. The first frame the reader sees is finished.
  solve(300);
  draw();
  stage.classList.add("on");
})();
