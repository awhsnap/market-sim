// Market Sim front end. Plain JavaScript, no frameworks.
// Prices from the server are integer cents; the UI shows dollars.
"use strict";

const $ = (sel) => document.querySelector(sel);
const MAX_POINTS = 5000;

let state = null;
let history = [];
let hoverIndex = null;

// ---------- formatting ----------

const usd = new Intl.NumberFormat("en-US", { style: "currency", currency: "USD" });
const int = new Intl.NumberFormat("en-US");

function money(cents) {
  return usd.format((cents || 0) / 100);
}
function signedMoney(cents) {
  const s = money(Math.abs(cents));
  return cents > 0 ? "+" + s : cents < 0 ? "−" + s : s;
}
function tone(n) {
  return n > 0 ? "up" : n < 0 ? "down" : "";
}
function compactMoney(cents) {
  const d = cents / 100;
  const a = Math.abs(d);
  if (a >= 1e6) return (d / 1e6).toFixed(1) + "M";
  if (a >= 1e3) return (d / 1e3).toFixed(1) + "k";
  return d.toFixed(a < 10 ? 2 : 0);
}
const PERMISSIONS = { buy_and_sell: "Buy and sell", buy_only: "Buy only", sell_only: "Sell only" };
const STRATEGIES = { random: "Random (noise)", momentum: "Trend following", meanrev: "Mean reversion" };

function el(tag, attrs = {}, ...children) {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (k === "class") e.className = v;
    else if (k.startsWith("on")) e.addEventListener(k.slice(2), v);
    else if (v !== undefined && v !== null) e.setAttribute(k, v);
  }
  for (const c of children) e.append(c instanceof Node ? c : document.createTextNode(String(c)));
  return e;
}

// ---------- API ----------

async function api(method, path, body) {
  const res = await fetch(path, {
    method,
    headers: body ? { "Content-Type": "application/json" } : {},
    body: body ? JSON.stringify(body) : undefined,
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || res.statusText);
  return data;
}

async function act(method, path, body, okMsg) {
  try {
    const data = await api(method, path, body);
    applyState(data.state || data);
    if (okMsg) toast(typeof okMsg === "function" ? okMsg(data) : okMsg);
    return data;
  } catch (err) {
    toast(err.message, true);
    return null;
  }
}

let toastTimer;
function toast(msg, isError = false) {
  const t = $("#toast");
  t.textContent = msg;
  t.className = "toast show" + (isError ? " error" : "");
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => (t.className = "toast"), isError ? 4000 : 2200);
}

// ---------- live data ----------

async function loadAll() {
  const [st, hist] = await Promise.all([api("GET", "/api/state"), api("GET", "/api/history?n=" + MAX_POINTS)]);
  history = hist;
  applyState(st);
}

function connect() {
  const es = new EventSource("/api/stream");
  es.addEventListener("open", () => $("#conn").classList.add("live"));
  es.addEventListener("error", () => $("#conn").classList.remove("live"));
  es.addEventListener("tick", (ev) => {
    const msg = JSON.parse(ev.data);
    const lastTick = history.length ? history[history.length - 1].tick : 0;
    for (const p of msg.points) if (p.tick > lastTick) history.push(p);
    if (history.length > MAX_POINTS) history = history.slice(-MAX_POINTS);
    applyState(msg.state);
  });
  es.addEventListener("reset", () => loadAll());
}

function applyState(st) {
  if (!st || st.tick === undefined) return;
  state = st;
  render();
}

// ---------- rendering ----------

let lastTapeID = 0;

function render() {
  const s = state;
  $("#tick").textContent = "tick " + int.format(s.tick);
  const play = $("#play");
  play.textContent = s.running ? "Pause" : "Play";
  play.classList.toggle("primary", !s.running);
  const speed = $("#speed");
  if (document.activeElement !== speed && [...speed.options].some((o) => +o.value === s.intervalMs)) speed.value = s.intervalMs;

  renderStats();
  renderBook();
  renderMaker();
  renderOrders();
  renderParticipants();
  renderTape();
  fillStrategyOptions();
  updateTicketDefaults();
  drawCharts();
}

function renderStats() {
  const s = state;
  const bid = s.bids[0]?.price, ask = s.asks[0]?.price;
  const m = s.maker || {};
  const items = [
    ["Last", s.last ? money(s.last) : "—"],
    ["Bid", bid ? money(bid) : "—", "up"],
    ["Ask", ask ? money(ask) : "—", "down"],
    ["Spread", bid && ask ? money(ask - bid) : "—"],
    ["Your position", int.format(m.position || 0), tone(m.position)],
    ["Your P&L", signedMoney(m.pnl || 0), tone(m.pnl)],
    ["Participant capital", money(s.summary.participantEquity)],
    ["Participant P&L", signedMoney(s.summary.participantPnL), tone(s.summary.participantPnL)],
  ];
  $("#stats").replaceChildren(
    ...items.map(([label, value, cls]) =>
      el("div", { class: "stat" }, el("div", { class: "label" }, label), el("div", { class: "value " + (cls || "") }, value)),
    ),
  );
}

function renderBook() {
  const s = state;
  const mine = new Set(s.makerOrders.map((o) => o.side + ":" + o.price));
  const levels = 10;
  const asks = s.asks.slice(0, levels).reverse();
  const bids = s.bids.slice(0, levels);
  const maxQty = Math.max(1, ...asks.map((l) => l.qty), ...bids.map((l) => l.qty));

  const row = (l, side) =>
    el(
      "div",
      {
        class: `row ${side === "sell" ? "ask" : "bid"}${mine.has(side + ":" + l.price) ? " mine" : ""}`,
        title: `${l.orders} order${l.orders === 1 ? "" : "s"}. Click to use this price.`,
        onclick: () => setTicketPrice(l.price, side === "sell" ? "buy" : "sell"),
      },
      el("div", { class: "bar", style: `width:${(l.qty / maxQty) * 100}%` }),
      el("span", { class: "px" }, money(l.price)),
      el("span", { class: "q" }, int.format(l.qty)),
      el("span", { class: "n muted" }, l.orders),
    );

  const bid = s.bids[0]?.price, ask = s.asks[0]?.price;
  const mid = bid && ask ? `Spread ${money(ask - bid)} · mid ${money((bid + ask) / 2)}` : "One side of the book is empty";

  const head = el("div", { class: "row head" }, el("span", {}, "Price"), el("span", { class: "q" }, "Qty"), el("span", { class: "n" }, "Orders"));
  $("#book").replaceChildren(
    head,
    ...(asks.length ? asks.map((l) => row(l, "sell")) : [el("div", { class: "empty" }, "No offers")]),
    el("div", { class: "mid" }, mid),
    ...(bids.length ? bids.map((l) => row(l, "buy")) : [el("div", { class: "empty" }, "No bids")]),
  );
}

function renderMaker() {
  const m = state.maker || {};
  const items = [
    ["Position", int.format(m.position || 0), tone(m.position)],
    ["Avg price", m.position ? money(m.avgPrice) : "—"],
    ["Cash", money(m.cash)],
    ["Realized", signedMoney(m.realized || 0), tone(m.realized)],
    ["Unrealized", signedMoney(m.unrealized || 0), tone(m.unrealized)],
    ["Total P&L", signedMoney(m.pnl || 0), tone(m.pnl)],
  ];
  $("#maker").replaceChildren(
    ...items.map(([label, value, cls]) =>
      el("div", {}, el("span", { class: "label" }, label), el("span", { class: "value " + (cls || "") }, value)),
    ),
  );
  const mark = state.mark;
  const body = $("#lots tbody");
  if (!state.makerLots.length) {
    body.replaceChildren(el("tr", {}, el("td", { colspan: 5, class: "empty" }, "Flat. No open lots.")));
    return;
  }
  body.replaceChildren(
    ...state.makerLots.map((l, i) => {
      const u = l.qty * (mark - l.price);
      return el(
        "tr",
        {},
        el("td", { class: "muted" }, i + 1),
        el("td", { class: l.qty > 0 ? "up" : "down" }, l.qty > 0 ? "Long" : "Short"),
        el("td", { class: "r" }, int.format(Math.abs(l.qty))),
        el("td", { class: "r" }, money(l.price)),
        el("td", { class: "r " + tone(u) }, signedMoney(u)),
      );
    }),
  );
}

function renderOrders() {
  const body = $("#orders tbody");
  if (!state.makerOrders.length) {
    body.replaceChildren(el("tr", {}, el("td", { colspan: 6, class: "empty" }, "No open orders.")));
    return;
  }
  body.replaceChildren(
    ...state.makerOrders
      .slice()
      .sort((a, b) => b.price - a.price)
      .map((o) =>
        el(
          "tr",
          {},
          el("td", { class: o.side === "buy" ? "up" : "down" }, o.side === "buy" ? "Buy" : "Sell"),
          el("td", {}, o.type),
          el("td", { class: "r" }, money(o.price)),
          el("td", { class: "r" }, int.format(o.qty - o.remaining)),
          el("td", { class: "r" }, int.format(o.qty)),
          el("td", { class: "r" }, el("button", { class: "btn link", type: "button", title: "Cancel", onclick: () => act("DELETE", "/api/orders/" + o.id) }, "✕")),
        ),
      ),
  );
}

// Participant rows are kept and patched in place so selects don't reset
// while you are using them.
const rowCache = new Map();

function renderParticipants() {
  const body = $("#participants tbody");
  const seen = new Set();
  const ps = state.participants;

  for (const p of ps) {
    seen.add(p.info.id);
    let r = rowCache.get(p.info.id);
    if (!r) r = makeParticipantRow(p.info.id);
    const a = p.account;
    r.name.textContent = p.info.name;
    setSelect(r.strategy, p.info.strategy);
    setSelect(r.permission, p.info.permission);
    r.cash.textContent = money(a.cash);
    r.shares.textContent = int.format(a.position);
    r.avg.textContent = a.position ? money(a.avgPrice) : "—";
    r.equity.textContent = money(a.equity);
    r.pnl.textContent = signedMoney(a.pnl);
    r.pnl.className = "r " + tone(a.pnl);
    r.orders.textContent = `${p.openOrders} open`;
    r.orders.title = `${p.info.submitted} submitted, ${p.info.rejected} rejected`;
    r.pause.textContent = p.info.paused ? "Resume" : "Pause";
    r.tr.classList.toggle("paused", p.info.paused);
    body.append(r.tr); // keeps server order
  }
  for (const [id, r] of rowCache) {
    if (!seen.has(id)) {
      r.tr.remove();
      rowCache.delete(id);
    }
  }
  if (!ps.length) body.replaceChildren(el("tr", {}, el("td", { colspan: 10, class: "empty" }, "No participants. Add one below.")));

  const s = state.summary;
  $("#p-totals").textContent = `${ps.length} participants · capital ${money(s.participantEquity)} · net ${int.format(s.participantPos)} shares`;
}

function makeParticipantRow(id) {
  const opts = (map) => Object.entries(map).map(([v, t]) => el("option", { value: v }, t));
  const strategy = el("select", { "aria-label": "Logic", onchange: (e) => act("PATCH", "/api/participants/" + id, { strategy: e.target.value }) }, ...opts(STRATEGIES));
  const permission = el("select", { "aria-label": "Can trade", onchange: (e) => act("PATCH", "/api/participants/" + id, { permission: e.target.value }) }, ...opts(PERMISSIONS));
  const pause = el("button", { class: "btn small", type: "button" }, "Pause");
  pause.addEventListener("click", () => {
    const p = state.participants.find((x) => x.info.id === id);
    act("PATCH", "/api/participants/" + id, { paused: !(p && p.info.paused) });
  });
  const remove = el("button", { class: "btn link", type: "button", title: "Remove", onclick: () => act("DELETE", "/api/participants/" + id, null, "Participant removed") }, "✕");
  const r = {
    name: el("td"),
    strategy,
    permission,
    cash: el("td", { class: "r" }),
    shares: el("td", { class: "r" }),
    avg: el("td", { class: "r" }),
    equity: el("td", { class: "r" }),
    pnl: el("td", { class: "r" }),
    orders: el("td", { class: "r muted" }),
    pause,
  };
  r.tr = el("tr", {}, r.name, el("td", {}, strategy), el("td", {}, permission), r.cash, r.shares, r.avg, r.equity, r.pnl, r.orders, el("td", { class: "r" }, pause, remove));
  rowCache.set(id, r);
  return r;
}

function setSelect(sel, value) {
  if (document.activeElement !== sel && sel.value !== value) sel.value = value;
}

function renderTape() {
  const body = $("#tape tbody");
  const trades = state.trades.slice(0, 25);
  if (!trades.length) {
    body.replaceChildren(el("tr", {}, el("td", { colspan: 5, class: "empty" }, "No trades yet.")));
    return;
  }
  const newest = trades[0].id;
  body.replaceChildren(
    ...trades.map((t) =>
      el(
        "tr",
        { class: t.id > lastTapeID && lastTapeID ? "flash" : "" },
        el("td", { class: "muted" }, new Date(t.time).toLocaleTimeString()),
        el("td", {}, t.buyer),
        el("td", {}, t.seller),
        el("td", { class: "r " + (t.aggressor === "buy" ? "up" : "down") }, money(t.price)),
        el("td", { class: "r" }, int.format(t.qty)),
      ),
    ),
  );
  lastTapeID = newest;
}

function fillStrategyOptions() {
  const sel = $("#add select[name=strategy]");
  if (sel.options.length) return;
  for (const k of state.strategies) sel.append(el("option", { value: k }, STRATEGIES[k] || k));
}

// ---------- order ticket ----------

let priceTouched = false;

function ticketValues() {
  const f = $("#ticket");
  return { side: f.side.value, type: f.type.value };
}

function updateTicketDefaults() {
  const { side, type } = ticketValues();
  const price = $("#t-price");
  price.disabled = type === "market";
  if (!priceTouched && document.activeElement !== price && state) {
    const bid = state.bids[0]?.price, ask = state.asks[0]?.price;
    const ref = side === "buy" ? bid || state.mark : ask || state.mark;
    if (ref) price.value = (ref / 100).toFixed(2);
  }
  const btn = $("#t-submit");
  btn.textContent = `Place ${side} ${type}`;
  btn.className = "btn wide " + side;
}

function setTicketPrice(cents, side) {
  const f = $("#ticket");
  f.side.value = side;
  f.type.value = "limit";
  $("#t-price").value = (cents / 100).toFixed(2);
  priceTouched = true;
  updateTicketDefaults();
  $("#t-hint").textContent = `${side === "buy" ? "Buying" : "Selling"} at ${money(cents)} would take liquidity at that level.`;
}

$("#ticket").addEventListener("input", (e) => {
  if (e.target.id === "t-price") priceTouched = true;
  if (e.target.name === "side" || e.target.name === "type") {
    priceTouched = false;
    $("#t-hint").textContent = "Click a price in the book to fill it in.";
  }
  updateTicketDefaults();
});

$("#ticket").addEventListener("submit", async (e) => {
  e.preventDefault();
  const { side, type } = ticketValues();
  const qty = parseInt($("#t-qty").value, 10);
  const price = Math.round(parseFloat($("#t-price").value) * 100);
  const body = { side, type, qty, price: type === "market" ? 0 : price };
  const res = await act("POST", "/api/orders", body, (d) => {
    const filled = d.order.qty - d.order.remaining;
    if (!filled) return `Order resting: ${side} ${qty} at ${money(price)}`;
    const avg = d.trades.reduce((s, t) => s + t.price * t.qty, 0) / filled;
    const rest = d.order.status === "partially_filled" ? `, ${d.order.remaining} resting` : d.order.status === "cancelled" ? `, ${d.order.remaining} unfilled` : "";
    return `Filled ${filled} at avg ${money(avg)}${rest}`;
  });
  if (res) priceTouched = false;
});

// ---------- controls ----------

$("#play").addEventListener("click", () => act("POST", "/api/control", { action: state?.running ? "pause" : "play" }));
$("#step").addEventListener("click", () => act("POST", "/api/control", { action: "step" }));
$("#speed").addEventListener("change", (e) => act("POST", "/api/control", { action: "speed", intervalMs: +e.target.value }));
$("#reset").addEventListener("click", async () => {
  if (!confirmReset()) return;
  await act("POST", "/api/control", { action: "reset" }, "Market reset");
  rowCache.forEach((r) => r.tr.remove());
  rowCache.clear();
  await loadAll();
});
$("#cancel-all").addEventListener("click", () => act("DELETE", "/api/orders", null, "All your orders cancelled"));
$("#window").addEventListener("change", drawCharts);

// Two clicks to reset, so a stray click doesn't wipe the market.
let resetArmed = 0;
function confirmReset() {
  const btn = $("#reset");
  if (Date.now() - resetArmed < 3000) {
    resetArmed = 0;
    btn.textContent = "Reset";
    return true;
  }
  resetArmed = Date.now();
  btn.textContent = "Click again to reset";
  setTimeout(() => (btn.textContent = "Reset"), 3000);
  return false;
}

$("#add").addEventListener("submit", (e) => {
  e.preventDefault();
  const f = e.target.elements;
  const strategy = f.strategy.value;
  const body = {
    name: f.name.value.trim(),
    strategy,
    permission: f.permission.value,
    cash: Math.round(parseFloat(f.cash.value || "0") * 100),
    shares: parseInt(f.shares.value || "0", 10),
    params: {
      activity: parseFloat(f.activity.value),
      aggression: parseFloat(f.aggression.value),
      maxQty: parseInt(f.maxQty.value, 10),
    },
  };
  act("POST", "/api/participants", body, `Added ${body.name || STRATEGIES[strategy]}`).then((ok) => {
    if (ok) f.name.value = "";
  });
});

// ---------- charts ----------

function css(name) {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim();
}

// Size the canvas backing store to its CSS box. The CSS sets the height,
// so redrawing can never change the layout (and never moves the page).
function setupCanvas(canvas) {
  const dpr = window.devicePixelRatio || 1;
  const w = canvas.clientWidth;
  const h = canvas.clientHeight;
  const bw = Math.round(w * dpr), bh = Math.round(h * dpr);
  if (canvas.width !== bw || canvas.height !== bh) {
    canvas.width = bw;
    canvas.height = bh;
  }
  const ctx = canvas.getContext("2d");
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  ctx.clearRect(0, 0, w, h);
  return { ctx, w, h };
}

// Group points into candles of `size` ticks. Buckets are aligned to tick
// numbers, so existing candles keep their shape as new ticks arrive.
function bucket(points, size) {
  if (size <= 1) return points;
  const out = [];
  let cur = null;
  for (const p of points) {
    const key = Math.floor(p.tick / size);
    if (!cur || cur.key !== key) {
      cur = { ...p, key, firstTick: p.tick };
      out.push(cur);
      continue;
    }
    cur.tick = p.tick;
    cur.time = p.time;
    cur.close = p.close;
    cur.high = Math.max(cur.high, p.high);
    cur.low = Math.min(cur.low, p.low);
    cur.volume += p.volume;
    cur.trades += p.trades;
    cur.makerPnL = p.makerPnL;
    cur.participantPnL = p.participantPnL;
  }
  return out;
}

// Ticks per candle, rounded to a steady value so it doesn't change on
// every update.
const STEPS = [1, 2, 3, 4, 5, 10, 15, 20, 25, 50, 100, 200, 250, 500, 1000];
function candleSize(ticks, maxBars) {
  const need = Math.ceil(ticks / maxBars);
  return STEPS.find((s) => s >= need) || need;
}

const AXIS_W = 64;

function visiblePoints(canvas) {
  const n = +$("#window").value;
  const pts = history.slice(-n);
  const maxBars = Math.max(20, Math.floor((canvas.clientWidth - AXIS_W) / 6));
  const size = candleSize(Math.max(pts.length, Math.min(n, 300)), maxBars);
  // Start on a bucket boundary so the first candle isn't a partial one.
  const first = pts.findIndex((p) => p.tick % size === 0);
  return bucket(first > 0 ? pts.slice(first) : pts, size);
}

let drawQueued = false;
function drawCharts() {
  if (drawQueued) return;
  drawQueued = true;
  requestAnimationFrame(() => {
    drawQueued = false;
    drawChartsNow();
  });
}

function drawChartsNow() {
  const bars = visiblePoints($("#price"));
  drawPrice(bars);
  drawPnL(bars);
  updateLegend(bars);
}

function niceTicks(lo, hi, count) {
  const span = hi - lo || 1;
  const raw = span / count;
  const mag = Math.pow(10, Math.floor(Math.log10(raw)));
  const step = [1, 2, 2.5, 5, 10].map((m) => m * mag).find((s) => span / s <= count) || 10 * mag;
  const out = [];
  for (let v = Math.ceil(lo / step) * step; v <= hi; v += step) out.push(v);
  return out;
}

function drawPrice(bars) {
  const canvas = $("#price");
  const { ctx, w, h } = setupCanvas(canvas);
  const plotW = w - AXIS_W, volH = 50, plotH = h - volH - 18;
  ctx.font = "11px system-ui, sans-serif";
  if (!bars.length) {
    ctx.fillStyle = css("--muted");
    ctx.fillText("Press Play or Step to start trading.", 12, 24);
    return;
  }
  let lo = Math.min(...bars.map((b) => b.low)), hi = Math.max(...bars.map((b) => b.high));
  const pad = Math.max((hi - lo) * 0.08, 5);
  lo -= pad; hi += pad;
  const y = (v) => 6 + (1 - (v - lo) / (hi - lo)) * (plotH - 6);
  const slot = plotW / bars.length;
  const bw = Math.max(1, Math.min(10, slot * 0.7));

  // grid + axis labels
  ctx.strokeStyle = css("--grid");
  ctx.fillStyle = css("--muted");
  ctx.lineWidth = 1;
  for (const v of niceTicks(lo, hi, 6)) {
    const yy = Math.round(y(v)) + 0.5;
    ctx.beginPath(); ctx.moveTo(0, yy); ctx.lineTo(plotW, yy); ctx.stroke();
    ctx.fillText(money(v), plotW + 6, yy + 4);
  }

  // volume
  const maxVol = Math.max(1, ...bars.map((b) => b.volume));
  const up = css("--up"), down = css("--down");
  bars.forEach((b, i) => {
    const x = i * slot + slot / 2;
    const vh = (b.volume / maxVol) * (volH - 6);
    ctx.globalAlpha = 0.35;
    ctx.fillStyle = b.close >= b.open ? up : down;
    ctx.fillRect(x - bw / 2, h - 18 - vh, bw, vh);
    ctx.globalAlpha = 1;
  });

  // candles
  const flat = css("--muted");
  bars.forEach((b, i) => {
    const x = Math.round(i * slot + slot / 2) + 0.5;
    // Gray for candles with no trades, so quiet ticks don't read as moves.
    const color = !b.trades ? flat : b.close >= b.open ? up : down;
    ctx.strokeStyle = color;
    ctx.fillStyle = color;
    ctx.beginPath(); ctx.moveTo(x, y(b.high)); ctx.lineTo(x, y(b.low)); ctx.stroke();
    const top = y(Math.max(b.open, b.close)), bot = y(Math.min(b.open, b.close));
    ctx.fillRect(x - bw / 2, top, bw, Math.max(1, bot - top));
  });

  // last price line
  const last = bars[bars.length - 1].close;
  const ly = Math.round(y(last)) + 0.5;
  ctx.setLineDash([3, 3]);
  ctx.strokeStyle = css("--accent");
  ctx.beginPath(); ctx.moveTo(0, ly); ctx.lineTo(plotW, ly); ctx.stroke();
  ctx.setLineDash([]);
  ctx.fillStyle = css("--accent");
  ctx.fillRect(plotW + 1, ly - 9, AXIS_W - 2, 18);
  ctx.fillStyle = "#fff";
  ctx.fillText(money(last), plotW + 6, ly + 4);

  // x labels (ticks)
  ctx.fillStyle = css("--muted");
  const every = Math.max(1, Math.ceil(bars.length / 6));
  for (let i = 0; i < bars.length; i += every) ctx.fillText("t" + bars[i].tick, i * slot + 2, h - 4);

  // hover crosshair
  if (hoverIndex !== null && bars[hoverIndex]) {
    const x = Math.round(hoverIndex * slot + slot / 2) + 0.5;
    ctx.strokeStyle = css("--muted");
    ctx.globalAlpha = 0.5;
    ctx.beginPath(); ctx.moveTo(x, 0); ctx.lineTo(x, h - 18); ctx.stroke();
    ctx.globalAlpha = 1;
  }
}

function drawPnL(bars) {
  const canvas = $("#pnl");
  const { ctx, w, h } = setupCanvas(canvas);
  const plotW = w - AXIS_W;
  ctx.font = "11px system-ui, sans-serif";
  if (!bars.length) return;
  const vals = bars.flatMap((b) => [b.makerPnL, b.participantPnL]);
  let lo = Math.min(0, ...vals), hi = Math.max(0, ...vals);
  const pad = Math.max((hi - lo) * 0.1, 100);
  lo -= pad; hi += pad;
  const y = (v) => 4 + (1 - (v - lo) / (hi - lo)) * (h - 8);
  const slot = plotW / bars.length;

  ctx.strokeStyle = css("--grid");
  ctx.fillStyle = css("--muted");
  for (const v of niceTicks(lo, hi, 3)) {
    const yy = Math.round(y(v)) + 0.5;
    ctx.beginPath(); ctx.moveTo(0, yy); ctx.lineTo(plotW, yy); ctx.stroke();
    ctx.fillText(compactMoney(v), plotW + 6, yy + 4);
  }
  ctx.strokeStyle = css("--muted");
  const zy = Math.round(y(0)) + 0.5;
  ctx.beginPath(); ctx.moveTo(0, zy); ctx.lineTo(plotW, zy); ctx.stroke();

  const line = (key, color) => {
    ctx.strokeStyle = color;
    ctx.lineWidth = 2;
    ctx.beginPath();
    bars.forEach((b, i) => {
      const x = i * slot + slot / 2;
      i ? ctx.lineTo(x, y(b[key])) : ctx.moveTo(x, y(b[key]));
    });
    ctx.stroke();
    ctx.lineWidth = 1;
  };
  line("participantPnL", css("--pp"));
  line("makerPnL", css("--mm"));
}

function updateLegend(bars) {
  const b = hoverIndex !== null && bars[hoverIndex] ? bars[hoverIndex] : bars[bars.length - 1];
  if (!b) {
    $("#legend").textContent = "";
    return;
  }
  const label = b.firstTick && b.firstTick !== b.tick ? `t${b.firstTick}–${b.tick}` : `t${b.tick}`;
  $("#legend").textContent = `${label}  O ${money(b.open)}  H ${money(b.high)}  L ${money(b.low)}  C ${money(b.close)}  Vol ${int.format(b.volume)}  You ${signedMoney(b.makerPnL)}`;
}

for (const id of ["#price", "#pnl"]) {
  const c = $(id);
  c.addEventListener("mousemove", (e) => {
    const bars = visiblePoints($("#price"));
    const rect = c.getBoundingClientRect();
    const plotW = rect.width - AXIS_W;
    const i = Math.floor(((e.clientX - rect.left) / plotW) * bars.length);
    hoverIndex = i >= 0 && i < bars.length ? i : null;
    drawCharts();
  });
  c.addEventListener("mouseleave", () => {
    hoverIndex = null;
    drawCharts();
  });
}

new ResizeObserver(() => state && drawCharts()).observe($("#price"));
window.matchMedia("(prefers-color-scheme: dark)").addEventListener("change", () => state && drawCharts());

// ---------- start ----------

loadAll()
  .then(connect)
  .catch((err) => toast("Could not load market: " + err.message, true));
