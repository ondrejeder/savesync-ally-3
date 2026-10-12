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

## Self-update

1. `build.ps1` sestaví obě binárky, `savesync publish` je nahraje do `.app/` s `latest.json` (verze, sha256).
2. Klienti kontrolují při startu a každých pár hodin; UI nabídne „Nainstalovat“.
3. Stažení → ověření sha256 → náhrada vlastní binárky → restart
   (Linux: systemd `Restart=always`; Windows: přejmenování na `.old` a spuštění nové).

## UI

- Jeden seznam her (lokální i cloud-only), stavy: synchronizováno, probíhá, konflikt, jen v cloudu, chybí složka.
- Konflikt: dvě karty (zařízení + čas) s volbou „Ponechat“.
- Vlastní prohlížeč složek s rychlými odkazy (Dokumenty, AppData, Proton prefixy).
- Pojmenovaná zařízení („PC“, „ROG Ally“).
- Živé aktualizace přes Server-Sent Events.
- Velké dotykové prvky, tmavé téma, systémové písmo.

## Fáze

1. Kostra: binárka, okno, install/autostart, UI s demo daty.
2. Drive klient + OAuth, plánovač s testy.
3. Hlavní UI: přidání hry, sync, nastavení na druhém zařízení, konflikty.
4. Watcher, Changes API, lokální zálohy.
5. Self-update a publish.
6. Migrace z v2 (import seznamu her a cest), test na Ally.
