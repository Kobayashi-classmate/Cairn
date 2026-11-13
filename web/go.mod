// This file keeps the Go toolchain out of the frontend tree: some npm
// packages ship stray .go files inside node_modules, which would otherwise be
// picked up by `go build ./...` and `go vet ./...`. The frontend is not a Go
// module; the built UI is embedded from internal/ui/dist instead.
module cairn.invalid/web

go 1.27.0
