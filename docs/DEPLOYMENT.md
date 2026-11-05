# Nasazení Windows x64 a Linux ARM64

V3 používá samostatnou konfiguraci i složku Google Drive oproti v2.
Automatický import her a cest z v2 zatím není implementovaný.
První synchronizaci proveď na kopii save složky a se samostatnou testovací hrou.

## Windows x64

V PowerShellu ve složce s binárkou:

```powershell
.\savesync-windows-amd64.exe install
```

Instalace pro aktuálního uživatele vytvoří autostart a zástupce a otevře UI.
V UI připoj Google Drive a nastav jméno zařízení. Build potřebuje OAuth konfiguraci
z `.env.local`; její hodnoty nevkládej do návodů ani logů.

## Odin 3 s Linuxem ARM64

Tato binárka je pro Linux, nikoli pro spuštění přímo v Androidu.
Konkrétní SteamOS port musí být ověřen na zařízení.
V terminálu desktopové relace nejprve zkontroluj:

```sh
uname -m
systemctl --user show-environment >/dev/null && echo "systemd user OK"
command -v xdg-open
command -v chromium || command -v google-chrome || command -v firefox
command -v flatpak >/dev/null && flatpak list --app
```

Očekává se `aarch64` a funkční systemd uživatelská relace. Okno potřebuje nativní
nebo Flatpak prohlížeč; aplikace hledá Chromium/Chrome/Brave/Edge/Vivaldi a Firefox.
Dostupnost a oprávnění prohlížeče je nutné ověřit pro daný port.

Přenes `savesync-linux-arm64` a `SHA256SUMS` do stejné složky na Odinu a spusť:

```sh
grep '  savesync-linux-arm64$' SHA256SUMS | sha256sum -c -
chmod +x ./savesync-linux-arm64
./savesync-linux-arm64 version
./savesync-linux-arm64 install
systemctl --user status savesync-ally3.service
```

Příkaz `install` otevře okno jednorázově. Při dalším přihlášení uživatele se
spouští pouze daemon na pozadí, bez okna. Zavřením okna synchronizaci neukončíš.
Po restartu do Game Mode ověř ze stejné uživatelské relace:

```sh
systemctl --user is-enabled savesync-ally3.service
systemctl --user is-active savesync-ally3.service
```

Očekávaný výstup je `enabled` a `active`. Fungování v Game Mode závisí na
uživatelské relaci konkrétního portu. Ruční spuštění služby:

```sh
systemctl --user start savesync-ally3.service
```

Kontrola součtu musí před instalací skončit `OK`. Instalaci spouštěj jako běžný
uživatel, bez `sudo`. Aplikace se instaluje do uživatelských dat; neupravuje
systémový oddíl. Následující příkazy otevřou UI a zobrazí diagnostiku:

```sh
~/.local/share/savesync-ally3/savesync open
journalctl --user -u savesync-ally3.service -n 100 --no-pager
```

Uvedená cesta předpokládá výchozí XDG adresáře. Přístup k save složkám a jejich
skutečné umístění zjisti na zařízení. Pro herní režim lze zkusit přidat aplikaci
do Steamu jako hru mimo Steam s argumentem `open`; fungování okna, přihlášení
a ovládání v herním režimu je samostatný test.

## Ověření před používáním na skutečných savech

1. Na obou zařízeních připoj stejný Google účet a nastav rozdílná jména zařízení.
2. Přidej testovací hru s kopií savů na Windows; na Odinu vyber její místní složku.
3. Ověř upload z Windows a download na Odinu, pak opačný směr. Porovnej obsah souborů.
4. S vypnutou hrou uprav stejný soubor offline na obou zařízeních, připoj síť
   a ověř konflikt i vědomou volbu vítězné verze.
5. Ověř obnovu zálohy, chování po výpadku sítě a uspání/probuzení.
6. Po restartu zařízení ověř autostart a dostupnost UI, včetně zamýšleného herního režimu.
7. S další testovací verzí ověř nabídku aktualizace, výběr správné architektury
   a úspěšný restart po instalaci.

## Vydávání

`scripts/build.ps1` vytváří všechny tři platformy. `scripts/release.ps1` pak
publikuje binárky na připojený Drive a manifest uloží až nakonec. Updater vybírá
`windows-amd64`, `linux-amd64` nebo `linux-arm64` podle běžící binárky a kontroluje
velikost a SHA-256. Samotný lokální build nic nepublikuje.
