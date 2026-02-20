# Contributing to motel

Thanks for your interest in contributing.

## Getting started

```sh
git clone https://github.com/andrewh/motel.git
cd motel
make build
make test
make lint
```

## Making changes

1. Fork the repository and create a branch from `main`.
2. Make your changes. Keep commits focused — one logical change per commit.
3. Ensure `make test` and `make lint` pass.
4. Open a pull request against `main`.

PRs are squash-merged, so don't worry about perfecting your commit history.

## Reporting bugs

Open an issue with steps to reproduce, expected behaviour, and actual
behaviour. Include your Go version and OS.

## Security issues

Please do **not** open a public issue for security vulnerabilities. See
[SECURITY.md](SECURITY.md) for responsible disclosure instructions.

## Licence

By contributing, you agree that your contributions will be licensed under the
[Apache 2.0 licence](LICENSE).
