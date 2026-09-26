#!/usr/bin/env bash
# Generates a synthetic Go module with N independent "feature" packages,
# all depending on one shared internal/common package - a common shape for
# a real, large Go monorepo (many mostly-independent packages sharing a
# handful of low-level utility packages).
#
# Usage: generate.sh <target-dir> <N>
set -euo pipefail

dir="$1"
n="$2"

rm -rf "$dir"
mkdir -p "$dir"
cd "$dir"

git init -q -b main
git config user.email "bench@example.com"
git config user.name "Bench"

cat > go.mod <<EOF
module benchrepo

go 1.21
EOF

mkdir -p internal/common
cat > internal/common/common.go <<'EOF'
package common

// Add is a small shared helper, standing in for the kind of low-level
// utility function real large codebases centralize and reuse everywhere.
func Add(a, b int) int {
	return a + b
}
EOF

cat > internal/common/common_test.go <<'EOF'
package common

import "testing"

func TestAdd(t *testing.T) {
	if Add(2, 3) != 5 {
		t.Fatal("bad")
	}
}
EOF

for i in $(seq 1 "$n"); do
	pkg="feature$i"
	mkdir -p "$pkg"
	cat > "$pkg/$pkg.go" <<EOF
package $pkg

import "benchrepo/internal/common"

// Compute$i stands in for one independent feature package's own logic -
// most large codebases have many packages like this that don't depend on
// each other, only on shared low-level utilities.
func Compute$i(x int) int {
	return common.Add(x, $i)
}
EOF
	cat > "$pkg/${pkg}_test.go" <<EOF
package $pkg

import "testing"

func TestCompute$i(t *testing.T) {
	if Compute$i(1) != 1+$i {
		t.Fatalf("Compute$i(1) = %d, want %d", Compute$i(1), 1+$i)
	}
}
EOF
done

git add -A
git commit -q -m "generate $n synthetic feature packages"
echo "generated $n packages in $dir"
