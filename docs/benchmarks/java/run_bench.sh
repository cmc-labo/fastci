#!/usr/bin/env bash
# Measures `mvn test` (baseline) vs `fastci test` (impact-narrowed)
# wall-clock time on a synthetic Maven reactor of N independent feature
# modules (see generate.sh), for a single-file body-only change to one
# feature module - the common case: touching one module that nothing
# else in the reactor depends on.
#
# Builds fastci fresh from this checkout, so it always benchmarks the code
# actually present in the working tree, not a stale binary. Runs fully
# offline (-o) - run `mvn -q test` once by hand first on a fresh machine
# to warm your local ~/.m2 cache (junit, the compiler/surefire plugins),
# the same one-time setup any real Maven project needs.
#
# Usage: run_bench.sh <N> [repeats]
set -euo pipefail

n="$1"
repeats="${2:-3}"
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/../../.." && pwd)"

work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

fastci_bin="$work_dir/fastci"
( cd "$repo_root" && go build -o "$fastci_bin" ./cmd/fastci )

gen_dir="$work_dir/repo"
bash "$script_dir/generate.sh" "$gen_dir" "$n" >/dev/null

cd "$gen_dir"
mvn -q -o test >/dev/null 2>&1   # warm target/ for every module once, untimed - see README's methodology note.

# One realistic change: a body-only edit to a single feature module's
# source file. Left uncommitted, so `fastci test` (no --base - the
# default "working tree vs HEAD" comparison) picks it up the same way a
# developer running it locally, mid-change, would.
apply_change() {
	sed -i "s/Common.add(x, 1);/Common.add(x, 1) + 0; \/\/ changed/" feature1/src/main/java/bench/feature1/Feature1.java
}
revert_change() {
	git checkout -q -- feature1/src/main/java/bench/feature1/Feature1.java
}

time_cmd() {
	local start end
	start=$(date +%s.%N)
	"$@" >"$work_dir/last-output.txt" 2>&1
	end=$(date +%s.%N)
	echo "$end - $start" | bc
}

baseline_times=()
fastci_times=()
selected_line=""

for i in $(seq 1 "$repeats"); do
	revert_change
	apply_change
	t=$(time_cmd mvn -q -o test)
	baseline_times+=("$t")
	revert_change
done

for i in $(seq 1 "$repeats"); do
	apply_change
	rm -rf .fastci-cache   # cold shim-compile cost every time - see README's methodology note.
	t=$(time_cmd "$fastci_bin" test -v -- -o)
	fastci_times+=("$t")
	if [ -z "$selected_line" ]; then
		selected_line=$(grep "selected" "$work_dir/last-output.txt" || true)
	fi
	revert_change
done

median() {
	printf '%s\n' "$@" | sort -n | awk '{a[NR]=$1} END{print a[int((NR+1)/2)]}'
}

b_median=$(median "${baseline_times[@]}")
f_median=$(median "${fastci_times[@]}")
speedup=$(echo "scale=1; $b_median / $f_median" | bc)

echo "N=$n"
echo "  baseline (mvn test)    times: ${baseline_times[*]} -> median ${b_median}s"
echo "  fastci   (fastci test) times: ${fastci_times[*]} -> median ${f_median}s"
echo "  speedup: ${speedup}x"
echo "  $selected_line"
