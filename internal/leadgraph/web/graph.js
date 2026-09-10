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
  // No viewBox is set here. fit() is the ONLY thing that ever sets one, so the
  // frame the reader gets cannot disagree with the frame the code believes in.
  // W and H stay what they always were - the world the layout is solved in -
  // and are no longer also a claim about what is on screen.

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

  // touched = the reader has framed this picture themselves. After that nothing
  // re-frames it behind their back - not a resize, not a redraw. fit() is only
  // ever automatic before the first interaction.
  var touched = false;

  var drag = null;
  function start(e, n, g) {
    drag = { n: n, g: g };
    n.fixed = true;
    touched = true;
    g.setPointerCapture(e.pointerId);
    e.preventDefault();
  }

  // PANNING THE BACKGROUND. Zoom shipped without it: once the reader had zoomed
  // in there was no way to reach the rest of the graph, and inside the
  // conversation card the wheel zoomed on its own (see the wheel handler), so
  // readers arrived at a magnified corner they could not leave. A viewport you
  // can enter and cannot leave is worse than no viewport at all.
  // See docs/bugfix/2026-09-10-the-graph-card-could-not-be-read.md
  var pan = null;
  svg.addEventListener("pointerdown", function (e) {
    if (drag) return;                                  // a node took this press first
    if (e.pointerType === "mouse" && e.button !== 0) return;
    var vb = svg.viewBox.baseVal;
    pan = { cx: e.clientX, cy: e.clientY, x: vb.x, y: vb.y, w: vb.width, h: vb.height };
    touched = true;
    // Capture is an optimisation - it keeps the pan alive when the cursor
    // leaves the picture - and it throws for a pointer the element does not
    // own. A pan that dies because of a failed optimisation would look exactly
    // like a pan that was never implemented.
    try { svg.setPointerCapture(e.pointerId); } catch (err) { /* pan still works */ }
    e.preventDefault();
  });
  svg.addEventListener("pointermove", function (e) {
    if (drag) {
      var p = point(e);
      drag.n.x = p.x; drag.n.y = p.y;
      solve(6); draw();
      return;
    }
    if (!pan) return;
    var r = svg.getBoundingClientRect();
    if (!r.width || !r.height) return;
    // Screen pixels to world units, so the point under the cursor stays under it.
    var dx = (e.clientX - pan.cx) * (pan.w / r.width);
    var dy = (e.clientY - pan.cy) * (pan.h / r.height);
    svg.setAttribute("viewBox",
      (pan.x - dx) + " " + (pan.y - dy) + " " + pan.w + " " + pan.h);
  });
  function release(e) {
    if (drag) { drag.n.fixed = false; drag = null; }
    if (pan) {
      pan = null;
      try { svg.releasePointerCapture(e.pointerId); } catch (err) { /* already gone */ }
    }
  }
  svg.addEventListener("pointerup", release);
  svg.addEventListener("pointercancel", release);
  // point converts a screen position into world units.
  //
  // THE ZERO-SIZED BOX IS NOT HYPOTHETICAL: an svg that has just been revealed,
  // one in a frame the browser has not laid out yet, and one in a hidden tab
  // all measure 0x0 while still being perfectly real. Dividing by that width
  // gives Infinity, and the zoom's `p.x - (p.x - vb.x) * k` then evaluates
  // Infinity - Infinity = NaN, which goes straight into the viewBox attribute
  // and takes the whole picture off screen — permanently, because every later
  // pan and zoom reads that NaN back out. Caught by dispatching one wheel event
  // at a freshly reloaded card.
  // See docs/bugfix/2026-09-10-the-graph-card-could-not-be-read.md
  function point(e) {
    var r = svg.getBoundingClientRect();
    var vb = svg.viewBox.baseVal;
    if (!r.width || !r.height) {
      return { x: vb.x + vb.width / 2, y: vb.y + vb.height / 2 };
    }
    return { x: vb.x + ((e.clientX - r.left) / r.width) * vb.width,
             y: vb.y + ((e.clientY - r.top) / r.height) * vb.height };
  }

  // ZOOM IS ON A MODIFIER, AND A BARE WHEEL IS LEFT ALONE.
  //
  // This handler used to preventDefault() every wheel event over the picture.
  // On its own page that is merely opinionated. Inside the conversation the
  // picture is something the reader scrolls PAST, so scrolling the conversation
  // with the pointer over the card moved nothing and silently zoomed the graph
  // instead - readers reported "the graph did not show" while looking at a
  // graph magnified past its own edges, with no way to pan back.
  //
  // macOS pinch-to-zoom arrives as a wheel event with ctrlKey set, so the
  // trackpad gesture people already expect keeps working with no extra rule.
  // See docs/bugfix/2026-09-10-the-graph-card-could-not-be-read.md
  svg.addEventListener("wheel", function (e) {
    if (!(e.ctrlKey || e.metaKey)) return;   // the page keeps its scroll
    e.preventDefault();
    touched = true;
    var vb = svg.viewBox.baseVal;
    var k = e.deltaY > 0 ? 1.1 : 0.9;
    var p = point(e);
    var w = Math.max(200, Math.min(3000, vb.width * k));
    var h = w * (vb.height / vb.width);
    svg.setAttribute("viewBox", (p.x - (p.x - vb.x) * (w / vb.width)) + " " +
      (p.y - (p.y - vb.y) * (h / vb.height)) + " " + w + " " + h);
    rescale();
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
    nodes.forEach(function (n) { n.fixed = false; });
    solve(300); draw();
    // Back to automatic framing, which is what "reset" means to the reader.
    // Restoring the CONSTANT viewBox instead is what it used to do, and in a
    // card that put them straight back in the unreadable state.
    touched = false;
    fit();
  });

  // fit frames the graph in whatever box the host actually gave it.
  //
  // WHY NOT A CONSTANT viewBox: the picture used to be drawn at "0 0 1000 620"
  // whatever the frame. On its own page that frame is 60vh and the constant is
  // fine. In the conversation card the frame is a wide, short strip, so
  // xMidYMid meet scaled the drawing down to the strip's HEIGHT - measured on
  // the live deployment: 287px of picture inside a 758px box, nodes about 2px
  // across. The graph was drawn, and unreadable, which a reader correctly
  // reports as "the graph did not show".
  // See docs/bugfix/2026-09-10-the-graph-card-could-not-be-read.md
  function fit() {
    if (!nodes.length) return;
    var minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity;
    nodes.forEach(function (n) {
      if (n.x < minX) minX = n.x;
      if (n.x > maxX) maxX = n.x;
      if (n.y < minY) minY = n.y;
      if (n.y > maxY) maxY = n.y;
    });
    // Labels sit to the RIGHT of their node, so the box needs more room on that
    // side than on the others or every rightmost name is cut in half.
    var padL = 40, padR = 170, padY = 44;
    var x = minX - padL, y = minY - padY;
    var w = Math.max((maxX - minX) + padL + padR, 320);
    var h = Math.max((maxY - minY) + padY * 2, 200);
    var r = svg.getBoundingClientRect();
    if (r.width > 0 && r.height > 0) {
      // GROW the box - never shrink it - to the frame's shape, so `meet`
      // letterboxes nothing and the picture uses the whole frame it was given.
      var frame = r.width / r.height;
      if (w / h < frame) { var nw = h * frame; x -= (nw - w) / 2; w = nw; }
      else { var nh = w / frame; y -= (nh - h) / 2; h = nh; }
    }
    svg.setAttribute("viewBox", x + " " + y + " " + w + " " + h);
    rescale();
  }

  // rescale keeps a node the same size ON SCREEN whatever viewBox is in force.
  //
  // WHY: R and the label size used to be constants in WORLD units, so the
  // moment the picture was framed into a small box everything in it shrank with
  // the frame. In the conversation card that came out at 3.8px circles and 6px
  // labels — Chinese at 6px is not small text, it is no text. Zoom had the same
  // problem in the other direction.
  //
  // This is not a change to the rule in this file's header. "Node radius is a
  // constant" means every node is the SAME size as every other, so that size
  // cannot smuggle in a claim about who matters. That still holds; what is
  // constant is now measured where the reader is, not where the maths is.
  //
  // Inline styles rather than attributes for the two that graph.css also sets:
  // a stylesheet beats a presentation attribute, so setAttribute would have
  // been silently overridden and looked like this code doing nothing.
  function rescale() {
    var vb = svg.viewBox.baseVal;
    var box = svg.getBoundingClientRect();
    if (!vb.width || !box.width) return;
    var k = vb.width / box.width;          // world units per CSS pixel
    nodeEls.forEach(function (g) {
      var c = g.firstChild, t = g.lastChild;
      c.setAttribute("r", R * k);
      c.style.strokeWidth = (1.5 * k) + "px";
      t.setAttribute("x", (R + 4) * k);
      t.setAttribute("y", 4 * k);
      t.style.fontSize = (11 * k) + "px";
    });
    linkEls.forEach(function (el) { el.style.strokeWidth = (1.4 * k) + "px"; });
  }

  // Settle first, reveal second, frame third. The first frame the reader sees
  // is finished AND fits its box. fit() has to come after `on`, because a
  // display:none ancestor gives the svg no measurable box to be fitted to.
  solve(300);
  draw();
  stage.classList.add("on");
  fit();

  // The frame changes size after the first paint more often than it looks: the
  // card is laid out while its iframe is still sizing, and readers resize
  // windows. Re-framing stays automatic only until the reader frames it.
  if (window.ResizeObserver) {
    new ResizeObserver(function () { if (!touched) fit(); }).observe(svg);
  }
})();
