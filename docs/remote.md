# Remote control from a phone

An agent asks a question while you are on the couch, at the other end of the flat, or at the coffee machine. neonmux can put your terminals on your iPhone: pick one, read it, answer it. The phone does not run a web terminal of its own. The daemon sends the screen it already keeps for each terminal, and the app draws it on a canvas with Iosevka Term and the colors of a neonmux theme, box drawing included. It works with the window closed, since the daemon serves the phone alone.

- **Terminals**: every terminal of the daemon, garage included, with its directory and its agent state. Agents blocked on a question come first.
- **New terminal**: `+ New terminal` opens a shell in your home directory, which shows up as a new pane on the desktop (in the garage if no window is open).
- **Typing**: the text field sends its text followed by `enter` when you press return. A text pasted over several lines goes as a bracketed paste when the program asked for it, so a shell does not run it line by line and Claude gets it in one block. Above the field, a row of keys the iOS keyboard lacks: `esc`, `tab`, `^C`, the four arrows and backspace.
- **Scrolling**: one finger scrolls the scrollback. In a full-screen program (Claude Code, nvim, less), the same gesture goes to the program, as wheel events when it reads the mouse and as arrow keys otherwise, the way the window does it. Two fingers zoom up to 3×, a double tap resets the view. "Copy screen" puts the visible text on the clipboard.
- **Size**: the last side used decides. Selecting a terminal on the phone, or typing into it, resizes it to the phone. Typing in the window gives its terminals their size back. Meanwhile the other side shows the terminal at the size it was given.
- **Themes**: the ten neonmux themes are on the phone too. You pick one for the whole app, or "Follow the desktop" to take the theme of the machine you are connected to.
- **Resume**: iOS cuts the connection as soon as the app goes to the background. Coming back within 5 minutes reconnects without Face ID, for up to 12 hours after the last Face ID.

One app serves every machine you pair, and one machine can be paired with several phones. A machine with several addresses (Tailscale and the LAN) is tried on each of them, starting with the last one that worked.

## Security

Whoever gets this channel types into your shells, so security comes first:

- **Off by default.** Nothing listens until you turn it on, and it turns itself off after 30 minutes without a phone connected.
- **Private addresses only.** The daemon listens on the LAN (RFC 1918) and Tailscale addresses of the machine, never on a public interface, and drops any connection from another address. Tailscale's 100.64.0.0/10 range counts only on a Tailscale interface, since some carriers hand out the same range on their own links. The page server listens on 127.0.0.1 only.
- **WebTransport with a pinned certificate.** The phone connects over HTTP/3 (QUIC, TLS 1.3) to a self-signed certificate whose fingerprint it received when pairing. No certificate authority is involved, and another certificate fails the connection. The daemon keeps a year of 13-day certificates, a new one every week, and hands the phone the updated list on each connection.
- **Pairing in front of the machine.** It starts from the desktop, which shows a QR code valid for 5 minutes and usable once. The phone scans it from inside the app, then both screens show a 6-digit code that you confirm on the desktop. Without that confirmation, the pairing is rejected.
- **A passkey per phone.** Pairing creates a passkey (Face ID) on the phone, and the machine keeps its public key. Every connection signs a fresh challenge with it, and the daemon checks the signature, the origin of the app and the relying party. The 5-minute resume relies on a non-exportable session key held by the app, which the daemon forgets when it restarts, when remote control turns off and when the phone is revoked.
- **Nothing before authentication.** Until the phone has proved who it is, the daemon answers nothing but the handshake: no terminal list, no screen. A connection has a minute to authenticate, and its messages are capped at 64 KiB until then.
- **Rate limits.** 5 failed handshakes in a minute ban an address for a minute. Connections waiting for their handshake are capped per address and in total, and at most 4 phones are connected at once. An authenticated phone opens at most 5 terminals a minute, and switches terminals at most 4 times a second.
- **Revoking** a phone cuts its connection at once.

The window drives remote control through the daemon's socket, which only your user can open. Any program running as your user can drive the window protocol and could pair a phone, the same trust boundary as `~/.ssh/authorized_keys`.

The bottom bar shows `remote` while it is on, and the names of the phones connected. Passkeys on iOS are synced through the iCloud keychain (end-to-end encrypted), so the key is not confined to the phone.

## Setting it up

The app is a PWA: a web page added to the home screen, which opens full screen and works offline from its cache. It must be opened once over HTTPS from an address the phone trusts, and neonmux uses `tailscale serve` for that, so at least one of your machines needs [Tailscale](https://tailscale.com) (with HTTPS certificates enabled in the tailnet). The page is served by your own machine, never by a third party. Other machines are then reached directly, over Tailscale or a LAN address, with no Tailscale needed on them. It needs a recent iOS, whose Safari supports WebTransport with pinned certificates.

1. Turn it on: `neonmux remote on`, or "Remote: enable" in the palette.
2. Publish the page on the tailnet. `neonmux remote status` prints the exact command, with the defaults:

   ```sh
   tailscale serve --bg --https=9006 http://127.0.0.1:8006
   ```

3. On the iPhone, open `https://<machine>.<tailnet>.ts.net:9006` in Safari, then Share, "Add to Home Screen". Open the app from the home screen from then on: Safari and the installed app do not share their storage.
4. Open the UDP port in the firewall. On Linux with firewalld, the port must be open in the zone of your LAN: `sudo firewall-cmd --add-port=8007/udp --permanent`, then `sudo firewall-cmd --reload`. macOS asks whether to accept incoming connections the first time remote control is turned on.
5. Pair: "Remote: pair a phone" in the palette shows the QR code. In the app, "Pair a machine" opens the camera. Scan the code, name the phone, accept Face ID, check that the 6-digit code matches the one on the desktop and press `enter` there (`esc` cancels).

Pairing needs the window, for the QR code and the confirmation. The rest works with the window closed. "Remote: devices" lists the paired phones (`d` revokes one), and "Remote: regenerate identity" makes a new certificate series and unpairs every phone.

## Configuration

In `~/.config/neonmux/config.json`:

| Key | Default | |
|---|---|---|
| `remote` | `false` | `true` turns remote control on every time the daemon starts |
| `remote_idle_off` | `"30m"` | turns it off after this long without a phone connected (a Go duration, `"0"` for never) |
| `remote_port` | `8007` | the UDP port of WebTransport |
| `remote_page_port` | `8006` | the local port of the page, on 127.0.0.1; `tailscale serve` publishes it 1000 above (9006) |

These keys are read when the daemon starts (`neonmux restart-server` applies a change). With `"remote": true`, you will likely want `"remote_idle_off": "0"` too, so the phone always finds the machine. Pairing always stays a gesture in front of the machine.

## Commands

```sh
neonmux remote on               # start listening
neonmux remote off              # stop, and cut the phones connected
neonmux remote status           # state, addresses, tailscale serve command, phones, recent connections
neonmux remote revoke iPhone    # unpair a phone, by name or by the id from status
```

## Limits

- iPhone only, with a recent iOS: the app relies on Safari's WebTransport with pinned certificates.
- At most 4 phones connected at once.
- A terminal whose screen does not fit in one 1 MiB frame (a very large terminal full of colors) cannot be shown, and the app says so.
- A hot restart of the daemon (`neonmux restart`) cuts the phones: they reconnect with Face ID, since the resume keys live in the daemon's memory only.
