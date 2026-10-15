# SaveSync Ally 3 – návrh

Synchronizace uložených pozic her mezi Windows PC a Linux handheldem (ROG Ally / Bazzite)
přes uživatelův Google Drive. Nástupce v2 (Go daemon + Fyne GUI).

## Principy

- **Jeden binární soubor** `savesync` v čistém Go (`CGO_ENABLED=0`), build pro obě platformy z Windows jedním příkazem.
- **UI = lokální web** vestavěný v binárce (`embed`), otevíraný jako okno aplikace (`--app` režim Edge/Chromium).
- **Bezpečnost dat má přednost** před pohodlím: nikdy nepřepsat save bez zálohy, nikdy nemazat bez historie.
- **Jednoduchost**: žádný npm build, žádné instalační skripty, žádný updater skript.

## Příkazy

| příkaz | význam |
|---|---|
| `savesync` / `savesync open` | otevře okno; pokud daemon neběží, spustí ho |
| `savesync run` | daemon (sync + UI server), spouští ho autostart |
| `savesync install` | zkopíruje se do instalační složky, nastaví autostart a zástupce |
| `savesync uninstall` | odebere autostart a zástupce (data a konfigurace zůstanou) |
| `savesync publish <dist>` | (dev) nahraje nové buildy na Drive pro self-update |

## Cesty

| | Windows | Linux |
|---|---|---|
| konfigurace | `%APPDATA%\SaveSyncAlly3` | `~/.config/savesync-ally3` |
| data, binárka, logy, zálohy | `%LOCALAPPDATA%\SaveSyncAlly3` | `~/.local/share/savesync-ally3` |
| autostart | `HKCU\...\Run` | `systemd --user` služba `savesync-ally3.service` |
| zástupce | Start menu + plocha | `~/.local/share/applications/savesync-ally3.desktop` |

UI server: `127.0.0.1:45455`. Přístup chrání token (`<config>/session`), který `open` předá v URL,
server ho vymění za HttpOnly cookie. Kontroluje se i hlavička `Host` (ochrana proti DNS rebinding).

## Okno aplikace

- Windows: Edge (vždy přítomen), fallback Chrome, poslední fallback výchozí prohlížeč.
- Linux: Chromium/Chrome/Brave/Edge (PATH i Flatpak), fallback Firefox `--kiosk`, poslední `xdg-open`.
- Vlastní profil prohlížeče (`--user-data-dir`), takže okno je oddělené od běžného prohlížení.

## Google Drive

- Scope **`drive.file`**: aplikace vidí jen soubory, které sama vytvořila.
- Struktura:
  ```
  SaveSync Ally 3/
    <gameId>/game.json        název, zařízení a čas poslední změny
    <gameId>/files/...        save soubory (relativní cesty vůči kořeni hry)
    .app/latest.json          verze + sha256 pro self-update
    .app/savesync-windows-amd64.exe
    .app/savesync-linux-amd64
  ```
- Soubory se aktualizují přes `Files.Update` (stabilní ID, Drive drží revize).
- Změny v cloudu se zjišťují přes **Changes API** (levný dotaz cca 1× za minutu).

## Model hry

- `id` (stabilní), `name`, kořenová složka pro každé zařízení, volitelné filtry (`include` glob).
- Relativní cesty jsou vždy vůči kořeni → stejné na Windows i Linuxu.
- Žádný samostatný „režim souborů“; složka + filtr ho nahrazuje.

## Sync engine

- Porovnání **podle obsahu** (MD5; Drive vrací `md5Checksum`), ne podle časů.
- **Baseline pro každé zařízení zvlášť** (lokálně): stav každého souboru při poslední úspěšné synchronizaci.
- Tříbodové porovnání (baseline / lokál / cloud):

  | lokál | cloud | akce |
  |---|---|---|
  | = | = | nic |
  | změna | = | upload |
  | = | změna | download |
  | změna | změna, stejný obsah | jen aktualizovat baseline |
  | změna | změna, jiný obsah | **konflikt** (řeší se za celou hru) |
  | smazán | = | smazat v cloudu (zůstává revize) |
  | = | smazán | smazat lokálně (po zálohe) |

- `plan(baseline, local, cloud) → []Action` je čistá funkce s tabulkovými testy.
- Stahování do dočasného souboru + `rename` (atomicky).
- Před každým přepsáním/smazáním lokálních souborů zip záloha do `backups/<gameId>/` (posledních ~10).
- **Jedna fronta, jeden worker**: watcher, časovač, UI i restore jen řadí hry do fronty → žádné souběhy.
- fsnotify rekurzivně + debounce ~10 s klidu (nesynchronizovat soubor, do kterého hra zapisuje).

## Detekce změn (fáze 4)

- **Lokálně:** rekurzivní fsnotify na kořen každé hry; sync po 10 s klidu, nejpozději 2 min při
  nepřetržitém zápisu. Vlastní zápisy (stahování, obnova) watcher ignoruje (`Pause`/`Resume`).
- **Cloud:** Drive Changes API 1× za minutu; změněný soubor → podle rodiče jen dotčená hra.
  Pozice v logu změn se ukládá (`state/drive-changes.token`), při expiraci plná kontrola.
- **Pojistky:** plná kontrola po probuzení ze spánku (zpožděný tik), po výpadku sítě
  a každých 30 min (`platform.DefaultScanInterval`, na Androidu se počítá s kratším).
- **Cache hashů:** MD5 se přepočítává jen u souborů se změněnou velikostí nebo časem
  (soubory změněné < 2 s před minulým skenem se pro jistotu hashují znovu).
- **Zamčené soubory** (hra má save otevřený): sync se zopakuje za minutu, nehlásí chybu.

## Pojistka po stažení

Lokální změna souboru do 10 min po jeho stažení z cloudu se nenahraje automaticky, ale ukáže
se jako konflikt („možná hra běžela se starým stavem“). Po stažení UI i notifikace radí hru
restartovat.

## Verze v cloudu

- Po nahrání změn se snímek celé hry (zip souborů v syncu) uloží do `<gameId>/.versions/`.
- Nejvýš 1 verze za 30 min ze stejného zařízení; při předání mezi zařízeními vždy.
- Uchovává se N posledních (výchozí 3, nastavení 1–5), starší se mažou natrvalo.
- Obnova: lokální zálohu aktuálního stavu → přesné rozbalení verze (smaže i soubory, které
  ve verzi nejsou) → nahrání jako nová aktuální verze.

## Notifikace

`platform.Notify`: Windows toast (přes PowerShell), Linux `notify-send`/`gdbus`. Konflikt,
pojistka, stažení z jiného zařízení, smazání hry z cloudu, vypršelé přihlášení; max. 1 za
10 min pro stejnou událost a hru.

## Self-update

1. `scriptselease.ps1 -Version X.Y.Z -Notes "…"` (čistý repozitář): vytvoří tag `vX.Y.Z`, sestaví
   binárky a `savesync publish` je nahraje do `SaveSync Ally 3/.app/` (stejný název = stejné ID souboru).
   `latest.json` jde až nakonec: verze, poznámky a pro každou platformu (`windows-amd64`,
   `linux-amd64`, později `linux-arm64`) ID souboru, velikost a SHA-256.
2. Klienti čtou manifest při plné kontrole, nejvýš 1× za 6 h, nebo ručně v Nastavení; novější
   verze → lišta „Nainstalovat“ a jednorázová notifikace.
3. Instalace (jen u nainstalované binárky): stažení → kontrola velikosti a SHA-256 → stará
   binárka přejmenována na `.old`, nová na její místo → restart. Linux pod systemd: daemon skončí
   a systemd ho spustí znovu; jinak se spustí `run --replace`, který počká na uvolnění portu.
   Okno aplikace se po změně verze samo znovu načte. `.old` se smaže po úspěšném startu.

## UI

- Jeden seznam her (lokální i cloud-only), stavy: synchronizováno, probíhá, konflikt, jen v cloudu, chybí složka.
- Konflikt: dvě karty (zařízení + čas) s volbou „Ponechat“.
- Vlastní prohlížeč složek s rychlými odkazy (Dokumenty, AppData, Proton prefixy).
- Pojmenovaná zařízení („PC“, „ROG Ally“).
- Živé aktualizace přes Server-Sent Events.
- Velké dotykové prvky, tmavé téma, systémové písmo.

## Fáze

1. ✅ Kostra: binárka, okno, install/autostart, UI s demo daty.
2. ✅ Drive klient + OAuth, plánovač s testy.
3. ✅ Hlavní UI: přidání hry, sync, nastavení na druhém zařízení, konflikty, lokální zálohy.
4. ✅ Watcher, Changes API, cache hashů, pojistka po stažení, notifikace, verze v cloudu.
5. ✅ Self-update a publish.
6. Migrace z v2 (import seznamu her a cest), test na Ally.

## Později: Linux ARM64 a Android (AYN Odin 3)

- Linux ARM64: další cíl v `build.ps1`, self-update vybírá binárku podle architektury.
- Android: lehké APK (Kotlin foreground service s Go jádrem + WebView na lokální UI),
  notifikace přes Android, emulátory ukládají do nastavitelných složek ve sdíleném úložišti.
