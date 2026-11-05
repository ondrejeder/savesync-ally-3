# SaveSync Ally 3

Synchronizace uložených pozic her mezi Windows PC a Linux handheldem (ROG Ally / Bazzite)
přes vlastní Google Drive. Jeden binární soubor, UI se otevírá jako okno aplikace.

Návrh a rozhodnutí: [docs/DESIGN.md](docs/DESIGN.md).

## Build

Potřeba je jen Go 1.25+ (CGO není potřeba). Z Windows:

```powershell
powershell -ExecutionPolicy Bypass -File scripts\build.ps1
```

Výstup:

- `dist/savesync-windows-amd64.exe` – Windows x64.
- `dist/savesync-linux-arm64` – Linux ARM64 (Odin 3 s ARM64 Linuxem).
- `dist/savesync-linux-amd64` – Linux x86-64 (ROG Ally / Bazzite).
- `dist/SHA256SUMS` – SHA-256 kontrolní součty všech tří binárek.

Build pro ARM64 vzniká přímo na Windows, bez WSL a bez CGO.
Příprava instalace a ověření na zařízení: [docs/DEPLOYMENT.md](docs/DEPLOYMENT.md).

OAuth klient se bere z `.env.local` (viz `.env.example`) a vkládá se do binárky při buildu.
Použij OAuth klienta typu **Desktop app**. Přihlášení preferuje lokální port 3000;
pokud je obsazený, automaticky vybere volný port. Webový OAuth klient s pevně
registrovaným redirectem tento náhradní port nepodporuje.

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

Bez něj se použije Firefox v běžném okně, případně výchozí prohlížeč.
Zavření okna neukončuje synchronizaci na pozadí.

## Vydání nové verze

```powershell
powershell -ExecutionPolicy Bypass -File scripts\release.ps1 -Version 3.0.1 -Notes "Co je nového"
```

Vytvoří tag, sestaví všechny tři binárky a nahraje je na Google Drive (účet přihlášený v aplikaci na tomto PC).
Všechna zařízení pak nabídnou aktualizaci jedním klikem.

## Vývoj

```
savesync run --demo     # daemon s ukázkovými daty
savesync open           # otevře okno
```

Pro testování bez zásahu do skutečné instalace nastav `SAVESYNC_CONFIG_DIR`,
`SAVESYNC_DATA_DIR` a případně `SAVESYNC_DEVICE_NAME`.
