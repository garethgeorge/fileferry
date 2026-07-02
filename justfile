# Recipes run their tools through `nix develop` (see flake.nix), so the pinned
# go / tailwindcss / goreleaser versions are used without entering the shell.

build:
    nix develop -c go build -o fileferry .

test:
    nix develop -c go test -race ./...

vet:
    nix develop -c go vet ./...

# Validate the goreleaser config.
release-check:
    nix develop -c goreleaser check

# Regenerate the committed Tailwind stylesheet. Only needed when classes in
# web/static change.
css:
    nix develop -c tailwindcss -i web/src/input.css -o web/static/tailwind.css --minify
