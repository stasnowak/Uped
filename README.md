# uped

A USB stick for your home network. Run `uped` on a small Proxmox LXC (or any
Linux box), open its page from any device on your LAN, drop files or whole
folders onto it, and download them on another device. No accounts, no app to
install, no cloud. Uploads are chunked and resume after a dropped Wi-Fi
connection, and everything is deleted automatically after 7 days.

> **Work in progress.** uped is being built in phases; see
> [`plans/uped-home-file-drop.md`](plans/uped-home-file-drop.md) for the design
> and progress. Installation instructions arrive with the first release.

## Building from source

```sh
make build          # produces dist/uped, a static binary
dist/uped --help
make run            # serves http://127.0.0.1:8080 from ./data
```

Requires Go 1.24 or newer.

## Security

uped has no authentication by design. Anyone who can reach the port can
upload, download and delete. Run it on your home LAN only and never forward
its port to the internet.

## License

MIT, see [LICENSE](LICENSE).
