# SaveSync Ally 3

Synchronizace uložených pozic her mezi Windows PC a Linux handheldem (ROG Ally / Bazzite)
přes vlastní Google Drive. Jeden binární soubor, UI se otevírá jako okno aplikace.

Návrh a rozhodnutí: [docs/DESIGN.md](docs/DESIGN.md).

## Build

Potřeba je jen Go 1.25+ (CGO není potřeba). Z Windows:

```powershell
powershell -ExecutionPolicy Bypass -File scripts\build.ps1
```

Výstup: `dist/savesync-windows-amd64.exe` a `dist/savesync-linux-amd64`.

OAuth klient se bere z `.env.local` (viz `.env.example`) a vkládá se do binárky při buildu.

## Instalace

```
savesync install
```

Zkopíruje se do datové složky, nastaví autostart (Windows `Run` klíč / systemd user služba),
vytvoří zástupce a otevře okno. `savesync uninstall` vše odebere, data a konfigurace zůstanou.

Na Linuxu je pro okno aplikace potřeba prohlížeč založený na Chromiu, např.:

```
flatpak install flathub org.chromium.Chromium
```

Bez něj se použije Firefox v kiosk režimu, případně výchozí prohlížeč.

## Vývoj

```
savesync run --demo     # daemon s ukázkovými daty
savesync open           # otevře okno
```

Pro testování bez zásahu do skutečné instalace nastav `SAVESYNC_CONFIG_DIR`,
`SAVESYNC_DATA_DIR` a případně `SAVESYNC_DEVICE_NAME`.
