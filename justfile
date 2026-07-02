build:
    go build -o fileferry .

test:
    go test -race ./...

vet:
    go vet ./...

# Regenerate the committed Tailwind stylesheet. Only needed when classes in
# web/static change; requires the standalone tailwindcss CLI on PATH
# (https://tailwindcss.com/blog/standalone-cli).
css:
    tailwindcss -i web/src/input.css -o web/static/tailwind.css --minify
