# QR Printer

A thermal printer server for the cheap X6/X6H bluetooth printers found on Aliexpress.
It only prints QR codes right now (since that what I use them for).

## Features
- Simple web interface for manual printing.
- REST API for integration with other apps.
- Single binary with embedded assets.
- Persistent configuration for printer MAC address.

## Usage

Download the latest version for your platform from the [Releases](https://github.com/imlinus/qr-printer/releases) page.

### Linux
Run the binary:
```bash
./qr-printer-linux
```
Access the dashboard at http://localhost:2030

### Windows
Double-click `qr-printer-windows.exe`. The application runs in the background. Access the control panel via a browser at http://localhost:2030
(Untested, I only have Linux)

## API Reference

### Print QR
`GET /print?qr=<text>`
Returns JSON success/error.

### Configuration
`GET /config` - View current MAC address.
`POST /config` - Set new MAC address (JSON body: `{"mac": "XX:XX:XX:XX:XX:XX"}`).

## Building
To build binaries for all supported platforms, run the packaging script:
```bash
./pack.sh
```
Output binaries are placed in the build/ directory.

## Requirements
- Linux: Bluetooth adapter and CAP_NET_RAW capabilities (handled by pack.sh).
- Windows: Bluetooth adapter.
- Font: MesloLGS or DejaVuSans (Linux), Arial (Windows).

### Inspiration
I got a lot of inspiration from these projects and wanna give them a shout-out
- [Cat-Printer](https://github.com/NaitLee/Cat-Printer)
- [TiMini-Print](https://github.com/Dejniel/TiMini-Print)
- [ble-printer-server](https://github.com/proffalken/ble-printer-server)