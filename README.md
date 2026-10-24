# GoPress

A high-performance, single-binary WordPress alternative written in Go – built to replace the
WordPress stack behind [hadler.me](https://hadler.me). WordPress-like admin, SQLite embedded,
plugins as WebAssembly (Extism/wazero).

Status: early development. Plan, milestones and devlog: [PLAN.md](PLAN.md).

## Build & run

Requires Go 1.27.

```sh
make build          # static binary in bin/gopress
make test           # go vet + go test
./bin/gopress serve --addr 127.0.0.1:8080 --data ./local/data
```

## Local test data

```sh
make testdata       # fresh DB dump, uploads, plugin/theme sources -> ./local (gitignored)
make devdb-up       # MariaDB 11.8 on 127.0.0.1:33066 seeded with the dump
make devdb-counts   # reference counts -> local/reference-counts.txt
```

`./local` contains personal data (comments, donor data) and must never be committed.
