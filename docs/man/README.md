# Manpages

This repository includes manpage sources for the `motel` server and the `motelier` CLI:

- `docs/man/man1/motel.1`
- `docs/man/man1/motelier.1`

## Preview locally (no install)

You can render a local manpage directly:

```sh
man ./docs/man/man1/motel.1
man ./docs/man/man1/motelier.1
```

If you prefer using `mandoc`:

```sh
mandoc -Tutf8 ./docs/man/man1/motel.1 | less
mandoc -Tutf8 ./docs/man/man1/motelier.1 | less
```

## Install (system-wide)

Copy the manpages into your system manpath (requires appropriate permissions):

```sh
install -m 0644 docs/man/man1/motel.1 /usr/local/share/man/man1/motel.1
install -m 0644 docs/man/man1/motelier.1 /usr/local/share/man/man1/motelier.1
```

Then rebuild the man database if your system requires it (varies by OS).

## macOS helper

For macOS, you can use:

```sh
./scripts/install_manpages_macos.sh --sudo
```
