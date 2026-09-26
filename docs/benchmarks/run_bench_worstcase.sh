#!/usr/bin/env bash
# The honest counterpart to run_bench.sh: measures the *worst case* for
# fastci - a change to the shared internal/common package that every
# feature package depends on, which correctly forces every package to be
# considered affected (a full run). Demonstrates that fastci adds only a
# small, bounded overhead here rather than making things worse.
#
# Usage: run_bench_worstcase.sh <N> [repeats]
set -euo pipefail

n="$1"
repeats="${2:-3}"
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/../.." && pwd)"

work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

fastci_bin="$work_dir/fastci"
( cd "$repo_root" && go build -o "$fastci_bin" ./cmd/fastci )

gen_dir="$work_dir/repo"
bash "$script_dir/generate.sh" "$gen_dir" "$n" >/dev/null

cd "$gen_dir"

apply_change() {
	sed -i 's/return a + b/return a + b + 0 \/\/ changed/' internal/common/common.go
}
revert_change() {
	git checkout -q -- internal/common/common.go
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
	go clean -testcache
	t=$(time_cmd go test ./...)
	baseline_times+=("$t")
	revert_change
done

for i in $(seq 1 "$repeats"); do
	apply_change
	go clean -testcache
	rm -rf .fastci-cache
	t=$(time_cmd "$fastci_bin" test -v)
	fastci_times+=("$t")
	if [ -z "$selected_line" ]; then
		selected_line=$(grep -E "selected|full suite" "$work_dir/last-output.txt" || true)
	fi
	revert_change
done

median() {
	printf '%s\n' "$@" | sort -n | awk '{a[NR]=$1} END{print a[int((NR+1)/2)]}'
}

b_median=$(median "${baseline_times[@]}")
f_median=$(median "${fastci_times[@]}")
ratio=$(echo "scale=1; $f_median / $b_median" | bc)

echo "N=$n (worst case: shared dependency changed)"
echo "  baseline (go test ./...) times: ${baseline_times[*]} -> median ${b_median}s"
echo "  fastci   (fastci test)   times: ${fastci_times[*]} -> median ${f_median}s"
echo "  fastci/baseline ratio: ${ratio}x"
echo "  $selected_line"
