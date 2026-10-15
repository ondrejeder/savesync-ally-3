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
  const d = s.drive || {};
  const parts = [];
  if (s.demo) parts.push(`<span class="chip demo">Demo data</span>`);
  if (d.connected) {
    parts.push(`<span class="chip ok" title="${esc(d.account)}">${icon("drive")}${d.account ? esc(d.account) : "Drive připojen"}</span>`);
  } else {
    parts.push(`<span class="chip bad">${icon("drive")}Drive nepřipojen</span>`);
  }
  parts.push(`<span class="chip">${icon(deviceIcon(s.device))}Toto zařízení: ${esc(s.device)}</span>`);
  if (d.connected && s.cloudChecked) parts.push(`<span class="chip subtle">${icon("refresh")}Cloud zkontrolován ${ago(s.cloudChecked)}</span>`);
  $("#chips").innerHTML = parts.join("");

  // Drive problems get a prominent notice with the fix right in it.
  const n = $("#notice");
  const u = s.update;
  if (u && u.installing) {
    n.innerHTML = `<span>Instaluji verzi ${esc(u.version)}… Aplikace se za chvíli restartuje.</span>`;
  } else if (u && u.version) {
    n.innerHTML = `<span><strong>Je dostupná verze ${esc(u.version)}</strong>${u.notes ? ` · ${esc(u.notes)}` : ""}${u.error ? `<br><span class="err">${esc(u.error)}</span>` : ""}</span><button class="btn primary" data-act="update-install">Nainstalovat</button>`;
  } else if (u && u.error) {
    n.innerHTML = `<span class="err">${esc(u.error)}</span>`;
  } else if (d.needsReauth) {
    n.innerHTML = `<span>Přihlášení ke Google Drive vypršelo. Synchronizace počká, dokud ho neobnovíš.</span><button class="btn primary" data-act="connect">Znovu připojit</button>`;
  } else if (!d.connected && !s.demo && !d.error) {
    n.innerHTML = `<span>Připoj svůj Google Drive, kam se budou ukládat savy.</span><button class="btn primary" data-act="connect">Připojit Google Drive</button>`;
  } else if (d.error) {
    n.innerHTML = `<span>${esc(d.error)}</span>`;
  } else {
    n.innerHTML = "";
  }
  n.hidden = n.innerHTML === "";
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
      if (g.watching) bits.push("sleduji změny");
      const notice = g.notice ? `<div class="card-notice">${icon("alert")}<span>${esc(g.notice)}</span></div>` : "";
      return `<section class="card">${head("b-ok", "check", esc(bits.join(" · ")))}<div class="actions">${btn("sync", "Sync")}${more}</div>${notice}</section>`;
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
      const why = g.guarded
        ? "Save se změnil hned po stažení novější verze. Možná hra běžela se starým stavem. Kterou verzi ponechat?"
        : "Konflikt: zařízení mají jinou verzi. Kterou ponechat?";
      return `<section class="card conflict">${head("b-warn", "merge", why, "warn")}<div class="actions">${more}</div><div class="sides">${sides}</div></section>`;
    }
    case "cloud_only": {
      const meta = `Jen v cloudu${g.cloudDevice ? ` · naposledy z ${esc(g.cloudDevice)}` : ""}${g.cloudModified ? ` ${esc(when(g.cloudModified))}` : ""}`;
      return `<section class="card cloud">${head("b-cloud", "cloud", meta)}<div class="actions">${btn("setup", "Nastavit na tomto zařízení", "primary")}${more}</div></section>`;
    }
    case "missing":
      return `<section class="card">${head("b-bad", "folder-off", esc(g.message || "Složka nenalezena"), "bad")}<div class="actions">${btn("edit", "Upravit cestu")}${more}</div></section>`;
    case "error":
      return `<section class="card">${head("b-bad", "alert", esc(g.message || "Chyba synchronizace"), "bad")}<div class="actions">${btn("sync", "Zkusit znovu")}${more}</div></section>`;
    default:
      return `<section class="card">${head("b-cloud", "clock", esc(g.message || "Čeká na synchronizaci"))}<div class="actions">${btn("sync", "Sync")}${more}</div></section>`;
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

// ---------- api ----------

async function api(method, path, body) {
  let res;
  try {
    res = await fetch(path, {
      method,
      headers: body ? { "Content-Type": "application/json" } : {},
      body: body ? JSON.stringify(body) : undefined,
    });
  } catch {
    throw new Error("Aplikace neodpovídá.");
  }
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || "Akce selhala.");
  return data;
}

let toastTimer;
function toast(msg) {
  const t = $("#toast");
  t.textContent = msg;
  t.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => (t.hidden = true), 3600);
}

async function action(kind, gameId = "") {
  try {
    const data = await api("POST", "/api/action", { kind, gameId });
    if (data.message) toast(data.message);
  } catch (e) {
    toast(e.message);
  }
}

const gameById = (id) => (state?.games || []).find((g) => g.id === id);
const isLocal = (g) => g && g.status !== "cloud_only";

// ---------- modals ----------

const modals = [];

function openModal({ title, body, footer = "", wide = false }) {
  const overlay = document.createElement("div");
  overlay.className = "overlay";
  overlay.innerHTML = `
    <div class="modal${wide ? " wide" : ""}" role="dialog" aria-modal="true" aria-label="${esc(title)}">
      <header class="modal-head"><h2>${esc(title)}</h2>
        <button class="btn icon ghost" data-close aria-label="Zavřít">${icon("x")}</button></header>
      <div class="modal-body">${body}</div>
      ${footer ? `<footer class="modal-foot">${footer}</footer>` : ""}
    </div>`;
  overlay.addEventListener("click", (e) => {
    if (e.target === overlay || e.target.closest("[data-close]")) closeModal(overlay);
  });
  $("#modals").appendChild(overlay);
  modals.push(overlay);
  overlay.querySelector("input:not([readonly])")?.focus();
  return overlay;
}

function closeModal(overlay = modals[modals.length - 1]) {
  if (!overlay) return;
  modals.splice(modals.indexOf(overlay), 1);
  overlay.remove();
  overlay.dispatchEvent(new Event("closed"));
}

document.addEventListener("keydown", (e) => {
  if (e.key === "Escape" && modals.length) closeModal();
});

function setError(m, msg) {
  const el = m.querySelector(".form-error");
  el.textContent = msg || "";
  el.hidden = !msg;
}

async function submitting(m, fn) {
  const btn = m.querySelector("[data-submit]");
  btn.disabled = true;
  setError(m, "");
  try {
    await fn();
  } catch (e) {
    setError(m, e.message);
  } finally {
    btn.disabled = false;
  }
}

// confirmModal resolves true only when the confirm button is pressed.
function confirmModal({ title, text, confirm, danger = false }) {
  return new Promise((resolve) => {
    let yes = false;
    const m = openModal({
      title,
      body: `<p class="lead">${text}</p>`,
      footer: `<button class="btn" data-close>Zrušit</button><button class="btn ${danger ? "danger" : "primary"}" data-yes>${esc(confirm)}</button>`,
    });
    m.addEventListener("closed", () => resolve(yes));
    m.querySelector("[data-yes]").addEventListener("click", () => {
      yes = true;
      closeModal(m);
    });
  });
}

// ---------- game form (add / setup / edit) ----------

function gameForm({ mode, game }) {
  const titles = { add: "Přidat hru", setup: `Nastavit ${game?.name} na tomto zařízení`, edit: `Upravit ${game?.name}` };
  const submit = { add: "Přidat a synchronizovat", setup: "Nastavit", edit: "Uložit" };
  const intro = {
    add: "Vyber složku, kde hra ukládá pozice. Celá složka se bude synchronizovat přes Google Drive.",
    setup: "Vyber složku, kde tahle hra ukládá pozice na tomto zařízení. Prázdná složka se naplní z cloudu; když v ní už savy jsou a liší se, zeptám se, kterou verzi ponechat.",
    edit: "Po změně složky nebo filtru se při další synchronizaci obě strany znovu porovnají.",
  };
  const m = openModal({
    title: titles[mode],
    body: `
      <p class="lead">${intro[mode]}</p>
      ${mode !== "setup" ? `<label class="field"><span>Název hry</span><input name="name" value="${esc(game?.name || "")}" placeholder="Hollow Knight" autocomplete="off"></label>` : ""}
      <div class="field"><span>Složka se savy</span>
        <div class="path-row">
          <div class="path ${game?.root ? "" : "empty"}" data-path>${esc(game?.root || "Zatím nevybráno")}</div>
          <button class="btn" type="button" data-pick>${icon("folder")}Vybrat složku</button>
        </div>
      </div>
      <label class="field"><span>Filtr souborů <em>(nepovinné)</em></span>
        <input name="include" value="${esc((game?.include || []).join("; "))}" placeholder="*.sav; profile*.dat" autocomplete="off">
        <small>Prázdné znamená celou složku. Více vzorů odděl středníkem.</small></label>
      <p class="form-error" hidden></p>`,
    footer: `<button class="btn" data-close>Zrušit</button><button class="btn primary" data-submit>${submit[mode]}</button>`,
  });
  let root = game?.root || "";
  const pathEl = m.querySelector("[data-path]");
  m.querySelector("[data-pick]").addEventListener("click", async () => {
    const picked = await pickFolder(root);
    if (!picked) return;
    root = picked.path;
    pathEl.textContent = root;
    pathEl.classList.remove("empty");
    const name = m.querySelector('input[name="name"]');
    if (name && !name.value.trim()) name.value = picked.label || picked.name;
  });
  m.querySelector("[data-submit]").addEventListener("click", () =>
    submitting(m, async () => {
      const req = {
        name: m.querySelector('input[name="name"]')?.value || game?.name || "",
        root,
        include: m.querySelector('input[name="include"]').value.split(/[;,]/),
      };
      const id = encodeURIComponent(game?.id || "");
      const path = mode === "add" ? "/api/games" : mode === "setup" ? `/api/games/${id}/setup` : `/api/games/${id}`;
      const data = await api("POST", path, req);
      closeModal(m);
      toast(data.message);
    })
  );
  m.addEventListener("keydown", (e) => {
    if (e.key === "Enter" && e.target.tagName === "INPUT") m.querySelector("[data-submit]").click();
  });
}

// ---------- folder picker ----------

function pickFolder(start) {
  return new Promise((resolve) => {
    let current = null;
    let label = "";
    let picked = null;
    const m = openModal({
      title: "Vyber složku se savy",
      wide: true,
      body: `
        <div class="picker">
          <nav class="places" data-places></nav>
          <div class="browser">
            <div class="crumbs">
              <button class="btn icon" data-up aria-label="O úroveň výš">${icon("up")}</button>
              <input data-pathinput spellcheck="false" autocomplete="off" aria-label="Cesta">
            </div>
            <div class="entries" data-entries><p class="muted pad">Načítám…</p></div>
          </div>
        </div>`,
      footer: `<span class="muted grow" data-hint></span><button class="btn" data-close>Zrušit</button><button class="btn primary" data-choose>Vybrat tuto složku</button>`,
    });
    m.addEventListener("closed", () => resolve(picked));
    const placesEl = m.querySelector("[data-places]");
    const entriesEl = m.querySelector("[data-entries]");
    const input = m.querySelector("[data-pathinput]");

    async function load(path, nextLabel = "") {
      let listing;
      try {
        listing = await api("GET", "/api/fs?path=" + encodeURIComponent(path || ""));
      } catch (e) {
        toast(e.message);
        return;
      }
      current = listing;
      label = nextLabel;
      input.value = listing.path || "";
      m.querySelector("[data-up]").disabled = !listing.parent;
      placesEl.innerHTML = (listing.places || [])
        .map((p) => `<button class="place${p.path === listing.path ? " active" : ""}" data-go="${esc(p.path)}">${icon(placeIcon(p.icon))}<span>${esc(p.name)}</span></button>`)
        .join("");
      const dirs = listing.dirs.map((d) => `
        <button class="entry" data-go="${esc(d.path)}" data-label="${esc(d.label || "")}">${icon("folder")}
          <span class="entry-name">${d.label ? `${esc(d.label)} <span class="muted">${esc(d.name)}</span>` : esc(d.name)}</span>
        </button>`).join("");
      const filesHtml = listing.files.map((f) => `<div class="entry file">${icon("file")}<span class="entry-name">${esc(f.name)}</span></div>`).join("");
      const more = listing.more ? `<p class="muted pad">a dalších ${listing.more} souborů</p>` : "";
      entriesEl.innerHTML = dirs + filesHtml + more || `<p class="muted pad">Složka je prázdná.</p>`;
      entriesEl.scrollTop = 0;
      const n = listing.files.length + (listing.more || 0);
      m.querySelector("[data-hint]").textContent = n ? `${files(n)} v této složce` : "";
    }

    m.addEventListener("click", (e) => {
      const go = e.target.closest("[data-go]");
      if (go) load(go.dataset.go, go.dataset.label || "");
      if (e.target.closest("[data-up]") && current?.parent) load(current.parent);
      if (e.target.closest("[data-choose]") && current?.path) {
        const name = current.path.split(/[\\/]/).filter(Boolean).pop() || current.path;
        picked = { path: current.path, name, label };
        closeModal(m);
      }
    });
    input.addEventListener("keydown", (e) => {
      if (e.key === "Enter") load(input.value.trim());
    });
    load(start || "");
  });
}

function placeIcon(name) {
  return { home: "home", doc: "file", gamepad: "gamepad", steam: "gamepad", disk: "disk" }[name] || "folder";
}

// ---------- game menu, backups ----------

function gameMenu(g) {
  const local = isLocal(g);
  const item = (act, ic, label, cls = "") => `<button class="sheet-item ${cls}" data-menu="${act}">${icon(ic)}<span>${label}</span></button>`;
  const m = openModal({
    title: g.name,
    body: `<div class="sheet">
      ${local ? item("sync", "refresh", "Synchronizovat teď") : item("setup", "down", "Nastavit na tomto zařízení")}
      ${local ? item("edit", "edit", "Upravit název, složku nebo filtr") : ""}
      ${local ? item("open-folder", "folder", "Otevřít složku se savy") : ""}
      ${local ? item("versions", "history", "Historie verzí v cloudu") : ""}
      ${local ? item("backups", "file", "Lokální zálohy") : ""}
      ${local ? item("remove", "unlink", "Přestat synchronizovat na tomto zařízení") : ""}
      ${item("delete-cloud", "trash", "Smazat z cloudu", "danger")}
    </div>`,
  });
  m.addEventListener("click", (e) => {
    const it = e.target.closest("[data-menu]");
    if (!it) return;
    closeModal(m);
    handle(it.dataset.menu, g.id);
  });
}

async function showBackups(g) {
  let list;
  try {
    list = await api("GET", `/api/games/${encodeURIComponent(g.id)}/backups`);
  } catch (e) {
    toast(e.message);
    return;
  }
  const rows = list.map((b) => `
    <div class="row-item">
      <div><div class="strong">${esc(when(b.created) || b.name)}</div><div class="muted">${files(b.files)} · ${size(b.bytes) || "0 B"}</div></div>
      <button class="btn" data-restore="${esc(b.name)}">Obnovit</button>
    </div>`).join("");
  const m = openModal({
    title: `Zálohy: ${g.name}`,
    body: `<p class="lead">Záloha vzniká automaticky pokaždé, když se lokální savy přepisují nebo mažou (třeba stažením novější verze z cloudu). Uchovává se posledních 10.</p>
      ${rows || `<p class="muted pad">Zatím žádné zálohy. Vzniknou při prvním přepsání lokálních souborů.</p>`}`,
  });
  m.addEventListener("click", async (e) => {
    const b = e.target.closest("[data-restore]");
    if (!b) return;
    const ok = await confirmModal({
      title: "Obnovit zálohu?",
      text: "Soubory ze zálohy přepíšou současné savy a pak se nahrají do cloudu. Současný stav se předtím také zazálohuje, takže to půjde vrátit.",
      confirm: "Obnovit",
    });
    if (!ok) return;
    try {
      const data = await api("POST", `/api/games/${encodeURIComponent(g.id)}/backups/restore`, { name: b.dataset.restore });
      closeModal(m);
      toast(data.message);
    } catch (err) {
      toast(err.message);
    }
  });
}

async function showVersions(g) {
  let list;
  try {
    list = await api("GET", `/api/games/${encodeURIComponent(g.id)}/versions`);
  } catch (e) {
    toast(e.message);
    return;
  }
  const rows = list.map((v, i) => `
    <div class="row-item">
      <div><div class="strong">${icon(deviceIcon(v.device))} ${esc(v.device || "?")} · ${esc(when(v.created))}${i === 0 ? ' <span class="pill">nejnovější</span>' : ""}</div>
        <div class="muted">${files(v.files)} · ${size(v.bytes) || "0 B"}</div></div>
      <button class="btn" data-version="${esc(v.id)}">Obnovit</button>
    </div>`).join("");
  const m = openModal({
    title: `Historie verzí: ${g.name}`,
    body: `<p class="lead">Snímek celé hry se v cloudu uloží po nahrání změn, nejvýš jednou za 30 minut a vždy při předání mezi zařízeními. Uchovává se ${state?.versions || 3} posledních (lze změnit v nastavení).</p>
      ${rows || `<p class="muted pad">Zatím žádné verze. První vznikne při příštím nahrání změn.</p>`}`,
  });
  m.addEventListener("click", async (e) => {
    const b = e.target.closest("[data-version]");
    if (!b) return;
    const ok = await confirmModal({
      title: "Obnovit tuto verzi?",
      text: "Savy na tomto zařízení se nahradí vybranou verzí a ta se pak nahraje jako aktuální na všechna zařízení. Současný stav se předtím zazálohuje (Lokální zálohy).",
      confirm: "Obnovit verzi",
    });
    if (!ok) return;
    try {
      const data = await api("POST", `/api/games/${encodeURIComponent(g.id)}/versions/restore`, { id: b.dataset.version });
      closeModal(m);
      toast(data.message);
    } catch (err) {
      toast(err.message);
    }
  });
}

// ---------- settings ----------

function showSettings() {
  const s = state || {};
  const d = s.drive || {};
  const m = openModal({
    title: "Nastavení",
    body: `
      <label class="field"><span>Název tohoto zařízení</span>
        <input name="device" value="${esc(s.device)}" placeholder="ROG Ally" autocomplete="off">
        <small>Zobrazuje se u konfliktů a v cloudu, aby bylo jasné, odkud která verze pochází.</small></label>
      <label class="field"><span>Počet uchovávaných verzí v cloudu</span>
        <select name="versions">${[1, 2, 3, 4, 5].map((n) => `<option value="${n}"${n === (s.versions || 3) ? " selected" : ""}>${n}</option>`).join("")}</select>
        <small>Kolik posledních snímků každé hry držet v cloudu pro návrat ke starší verzi.</small></label>
      <div class="field"><span>Google Drive</span>
        <div class="row-item">
          <div>${d.connected ? esc(d.account || "Připojeno") : d.needsReauth ? "Přihlášení vypršelo" : "Nepřipojeno"}</div>
          ${d.connected ? `<button class="btn" data-drive="disconnect">Odpojit</button>` : `<button class="btn primary" data-drive="connect">Připojit</button>`}
        </div></div>
      <div class="field"><span>Spouštění</span>
        <div class="muted">${s.autostart ? "Spouští se automaticky po přihlášení do systému." : "Automatické spouštění není nastavené (spusť <code>savesync install</code>)."}</div></div>
      <div class="field"><span>Verze</span>
        <div class="row-item"><div class="muted">SaveSync Ally ${esc(s.version)}${s.update?.version ? ` · dostupná ${esc(s.update.version)}` : ""}</div>
        <button class="btn" data-drive="update-check">Zkontrolovat aktualizace</button></div></div>
      <p class="form-error" hidden></p>`,
    footer: `<button class="btn" data-close>Zavřít</button><button class="btn primary" data-submit>Uložit</button>`,
  });
  m.querySelector("[data-submit]").addEventListener("click", () =>
    submitting(m, async () => {
      const data = await api("POST", "/api/settings", {
        device: m.querySelector('input[name="device"]').value,
        versions: Number(m.querySelector('select[name="versions"]').value),
      });
      closeModal(m);
      toast(data.message);
    })
  );
  m.addEventListener("click", async (e) => {
    const b = e.target.closest("[data-drive]");
    if (!b) return;
    if (b.dataset.drive === "update-check") {
      action("update-check");
      return;
    }
    if (b.dataset.drive === "disconnect") {
      const ok = await confirmModal({ title: "Odpojit Google Drive?", text: "Synchronizace se zastaví, dokud Drive znovu nepřipojíš. Savy v cloudu ani na disku se nesmažou.", confirm: "Odpojit" });
      if (!ok) return;
    }
    closeModal(m);
    action(b.dataset.drive);
  });
}

// ---------- dispatch ----------

async function handle(kind, id) {
  const g = gameById(id);
  switch (kind) {
    case "add":
      if (!state?.drive?.connected) return toast("Nejdřív připoj Google Drive.");
      return gameForm({ mode: "add" });
    case "setup":
      return g && gameForm({ mode: "setup", game: g });
    case "edit":
      return g && gameForm({ mode: "edit", game: g });
    case "menu":
      return g && gameMenu(g);
    case "backups":
      return g && showBackups(g);
    case "versions":
      return g && showVersions(g);
    case "settings":
      return showSettings();
    case "remove": {
      const ok = await confirmModal({
        title: "Přestat synchronizovat?",
        text: `${esc(g?.name)} se na tomto zařízení přestane synchronizovat. Soubory na disku i v cloudu zůstanou beze změny a hru můžeš kdykoli znovu nastavit.`,
        confirm: "Přestat synchronizovat",
      });
      if (!ok) return;
      try { toast((await api("DELETE", `/api/games/${encodeURIComponent(id)}`)).message); } catch (e) { toast(e.message); }
      return;
    }
    case "delete-cloud": {
      const ok = await confirmModal({
        title: "Smazat z cloudu?",
        text: `Složka hry ${esc(g?.name)} se přesune do koše Google Drive (30 dní jde obnovit) a hra se přestane synchronizovat na všech zařízeních. <strong>Lokální savy na disku zůstanou.</strong>`,
        confirm: "Smazat z cloudu",
        danger: true,
      });
      if (!ok) return;
      try { toast((await api("DELETE", `/api/games/${encodeURIComponent(id)}?cloud=true`)).message); } catch (e) { toast(e.message); }
      return;
    }
    default:
      return action(kind, id);
  }
}

document.addEventListener("click", (e) => {
  if (e.target.closest(".overlay")) return;
  const el = e.target.closest("[data-act]");
  if (el) handle(el.dataset.act, el.dataset.id || "");
});
$("#sync-all").addEventListener("click", () => action("sync-all"));
$("#settings").addEventListener("click", () => showSettings());

// Feel like an app, not a web page.
document.addEventListener("contextmenu", (e) => {
  if (!e.target.closest("input")) e.preventDefault();
});
document.addEventListener("keydown", (e) => {
  if ((e.ctrlKey || e.metaKey) && ["p", "s", "f", "u"].includes(e.key.toLowerCase())) e.preventDefault();
});

// ---------- live state ----------

function connect() {
  const es = new EventSource("/api/events");
  es.addEventListener("state", (e) => {
    const next = JSON.parse(e.data);
    // After an update the daemon restarts with new UI files: reload them.
    if (state && next.version !== state.version) {
      location.reload();
      return;
    }
    state = next;
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
