# Highbay for npm

Install Highbay on macOS or Linux with Node.js 20 or later. Highbay also needs
Go 1.27.3 or later, Git 2.45 or later, and Docker.

```sh
# Register your coding agents and start Highbay's companion services.
npx @goadesign/highbay up

# Run from your project folder to open the browser workspace.
npx @goadesign/highbay
```

The package verifies the Highbay archive against its published SHA-256
checksum before caching the executable under `~/.cache/highbay/npm`. The
Chorus package installs and verifies its own executable, and `highbay up`
verifies and installs Herdr Core (`hc`) when needed.

`HIGHBAY_NPM_CACHE_DIR` selects the binary cache directory.
`HIGHBAY_RELEASE_BASE_URL` selects a mirror for the package's release version.
The official release and each mirror have separate cache entries.

Use `npx @goadesign/highbay@latest` to launch the latest release. For a
global installation, use `npm install -g @goadesign/highbay@latest`. This
updates the launcher and its Chorus companion together.

[Highbay documentation](https://github.com/goadesign/highbay#readme)
