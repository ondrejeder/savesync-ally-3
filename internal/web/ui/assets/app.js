"use strict";

const $ = (sel) => document.querySelector(sel);
const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
const icon = (name) => `<svg class="ic"><use href="#i-${name}"/></svg>`;

let state = null;

// ---------- formatting ----------

const rtf = new Intl.RelativeTimeFormat("cs", { numeric: "auto" });
function ago(iso) {
  if (!iso) return "";
  const diff = (Date.parse(iso) - Date.now()) / 1000;
  const abs = Math.abs(diff);
  if (abs < 45) return "právě teď";
  if (abs < 3600) return rtf.format(Math.round(diff / 60), "minute");
  if (abs < 86400) return rtf.format(Math.round(diff / 3600), "hour");
  return when(iso);
}
function when(iso) {
  if (!iso) return "";
  const d = new Date(iso);
  const today = new Date();
  const time = d.toLocaleTimeString("cs-CZ", { hour: "2-digit", minute: "2-digit" });
  if (d.toDateString() === today.toDateString()) return `dnes ${time}`;
  const y = new Date(today); y.setDate(today.getDate() - 1);
  if (d.toDateString() === y.toDateString()) return `včera ${time}`;
  return `${d.getDate()}. ${d.getMonth() + 1}. ${time}`;
}
function size(bytes) {
  if (!bytes) return "";
  const units = ["B", "kB", "MB", "GB"];
  let i = 0, v = bytes;
  while (v >= 1000 && i < units.length - 1) { v /= 1000; i++; }
  return `${v.toLocaleString("cs-CZ", { maximumFractionDigits: v < 10 ? 1 : 0 })} ${units[i]}`;
}
function files(n) {
  if (n === 1) return "1 soubor";
  if (n >= 2 && n <= 4) return `${n} soubory`;
  return `${n} souborů`;
}
function deviceIcon(name) {
  return /ally|deck|handheld|legion/i.test(name || "") ? "gamepad" : "desktop";
}

// ---------- rendering ----------

function renderChips() {
  const s = state;
  const parts = [];
  if (s.demo) parts.push(`<span class="chip demo">Demo data</span>`);
  if (s.drive.connected) parts.push(`<span class="chip ok">${icon("drive")}Drive připojen</span>`);
  else parts.push(`<span class="chip bad">${icon("drive")}Drive nepřipojen</span>`);
  parts.push(`<span class="chip">${icon(deviceIcon(s.device))}Toto zařízení: ${esc(s.device)}</span>`);
  $("#chips").innerHTML = parts.join("");
}

function card(g) {
  const more = `<button class="btn icon" data-act="menu" data-id="${esc(g.id)}" aria-label="Další akce">${icon("dots")}</button>`;
  const btn = (act, label, cls = "") => `<button class="btn ${cls}" data-act="${act}" data-id="${esc(g.id)}">${label}</button>`;
  const head = (badgeCls, ic, meta, metaCls = "") => `
    <div class="badge ${badgeCls}">${icon(ic)}</div>
    <div><p class="title">${esc(g.name)}</p><p class="meta ${metaCls}">${meta}</p>${g.progress ? progressBar(g.progress) : ""}</div>`;

  switch (g.status) {
    case "synced": {
      const bits = [`Synchronizováno ${ago(g.lastSync)}`];
      if (g.files) bits.push(files(g.files));
      if (g.bytes) bits.push(size(g.bytes));
      return `<section class="card">${head("b-ok", "check", esc(bits.join(" · ")))}<div class="actions">${btn("sync", "Sync")}${more}</div></section>`;
    }
    case "syncing": {
      const p = g.progress || { direction: "up", done: 0, total: 0 };
      const verb = p.direction === "down" ? "Stahuji" : "Nahrávám";
      const meta = p.total ? `${verb} ${p.done} z ${p.total} souborů…` : "Synchronizuji…";
      return `<section class="card">${head("b-busy", p.direction === "down" ? "down" : "up", meta)}<div class="actions">${more}</div></section>`;
    }
    case "conflict": {
      const sides = (g.conflict || []).map((c) => `
        <div class="side">
          <div><div class="side-name">${icon(deviceIcon(c.device))}${esc(c.device)}${c.side === "cloud" ? " (cloud)" : ""}</div>
          <div class="meta">${esc(when(c.modified))}${c.files ? " · " + files(c.files) : ""}</div></div>
          ${`<button class="btn" data-act="keep-${c.side}" data-id="${esc(g.id)}">Ponechat</button>`}
        </div>`).join("");
      return `<section class="card conflict">${head("b-warn", "merge", "Konflikt: zařízení mají jinou verzi. Kterou ponechat?", "warn")}<div class="actions">${more}</div><div class="sides">${sides}</div></section>`;
    }
    case "cloud_only": {
      const meta = `Jen v cloudu${g.cloudDevice ? ` · naposledy z ${esc(g.cloudDevice)}` : ""}${g.cloudModified ? ` ${esc(when(g.cloudModified))}` : ""}`;
      return `<section class="card cloud">${head("b-cloud", "cloud", meta)}<div class="actions">${btn("setup", "Nastavit na tomto zařízení", "primary")}</div></section>`;
    }
    case "missing":
      return `<section class="card">${head("b-bad", "folder-off", esc(g.message || "Složka nenalezena"), "bad")}<div class="actions">${btn("edit", "Upravit cestu")}${more}</div></section>`;
    case "error":
      return `<section class="card">${head("b-bad", "alert", esc(g.message || "Chyba synchronizace"), "bad")}<div class="actions">${btn("sync", "Zkusit znovu")}${more}</div></section>`;
    default:
      return `<section class="card">${head("b-cloud", "clock", "Čeká na synchronizaci")}<div class="actions">${more}</div></section>`;
  }
}

function progressBar(p) {
  const pct = p.total ? Math.round((p.done / p.total) * 100) : 0;
  return `<div class="bar"><div style="width:${pct}%"></div></div>`;
}

function render() {
  if (!state) return;
  renderChips();
  const games = state.games || [];
  const add = `<button class="btn dashed" data-act="add">${icon("plus")}Přidat hru</button>`;
  if (!games.length) {
    $("#games").innerHTML = `<div class="empty"><h2>Přidej svou první hru</h2><p>Vyber složku s uloženými pozicemi a SaveSync ji bude držet v cloudu.</p>${add}</div>`;
    return;
  }
  $("#games").innerHTML = games.map(card).join("") + add;
}

// ---------- actions ----------

let toastTimer;
function toast(msg) {
  const t = $("#toast");
  t.textContent = msg;
  t.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => (t.hidden = true), 3200);
}

async function action(kind, gameId = "") {
  try {
    const res = await fetch("/api/action", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ kind, gameId }),
    });
    const data = await res.json().catch(() => ({}));
    toast(data.message || data.error || (res.ok ? "Hotovo" : "Akce selhala"));
  } catch {
    toast("Aplikace neodpovídá.");
  }
}

document.addEventListener("click", (e) => {
  const el = e.target.closest("[data-act]");
  if (el) action(el.dataset.act, el.dataset.id || "");
});
$("#sync-all").addEventListener("click", () => action("sync-all"));
$("#settings").addEventListener("click", () => action("settings"));

// Feel like an app, not a web page.
document.addEventListener("contextmenu", (e) => e.preventDefault());
document.addEventListener("keydown", (e) => {
  if ((e.ctrlKey || e.metaKey) && ["p", "s", "f", "u"].includes(e.key.toLowerCase())) e.preventDefault();
});

// ---------- live state ----------

function connect() {
  const es = new EventSource("/api/events");
  es.addEventListener("state", (e) => {
    state = JSON.parse(e.data);
    $("#banner").hidden = true;
    render();
  });
  es.onerror = () => {
    const b = $("#banner");
    b.textContent = "Spojení s aplikací je přerušené, zkouším ho obnovit…";
    b.hidden = false;
  };
}
connect();
// Keep relative times fresh without server pushes.
setInterval(render, 30_000);
