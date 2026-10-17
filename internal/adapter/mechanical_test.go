package adapter

import "testing"

func TestIsMechanical(t *testing.T) {
	for cmd, want := range map[string]bool{
		"gofmt -w .":               true,
		"goimports -w main.go":     true,
		"npx prettier --write src": true,
		"eslint src --fix":         true,
		"eslint src":               false,
		"black .":                  true,
		"ruff format .":            true,
		"ruff check . --fix":       true,
		"ruff check .":             false,
		"cargo fmt":                true,
		"go generate ./...":        true,
		"go mod tidy":              true,
		"npm install left-pad":     true,
		"npm ci":                   true,
		"yarn":                     true,
		"pnpm i":                   true,
		"pip install requests":     true,
		"sed -i 's/a/b/' f.txt":    true,
		"sed 's/a/b/' f.txt":       false,
		"go test ./...":            false,
		"echo hi > out.txt":        false,
		"git commit -m 'blackout'": false,
		"rm -rf build":             false,
	} {
		if got := IsMechanical(cmd); got != want {
			t.Errorf("IsMechanical(%q) = %v, want %v", cmd, got, want)
		}
	}
}
