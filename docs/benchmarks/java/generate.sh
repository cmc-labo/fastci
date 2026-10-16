#!/usr/bin/env bash
# Generates a synthetic Maven multi-module reactor with N independent
# "feature" modules, all depending on one shared "common" module - the
# same shape, for the same reason, as ../generate.sh uses for Go: a
# common pattern for a real, large Java monorepo (many mostly-independent
# modules sharing a handful of low-level utility modules).
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

cat > pom.xml <<EOF
<project xmlns="http://maven.apache.org/POM/4.0.0">
  <modelVersion>4.0.0</modelVersion>
  <groupId>bench</groupId>
  <artifactId>bench-root</artifactId>
  <version>1.0</version>
  <packaging>pom</packaging>
  <modules>
    <module>common</module>
$(for i in $(seq 1 "$n"); do echo "    <module>feature$i</module>"; done)
  </modules>
  <properties>
    <maven.compiler.source>11</maven.compiler.source>
    <maven.compiler.target>11</maven.compiler.target>
    <project.build.sourceEncoding>UTF-8</project.build.sourceEncoding>
  </properties>
</project>
EOF

mkdir -p common/src/main/java/bench/common common/src/test/java/bench/common
cat > common/pom.xml <<EOF
<project xmlns="http://maven.apache.org/POM/4.0.0">
  <modelVersion>4.0.0</modelVersion>
  <parent>
    <groupId>bench</groupId>
    <artifactId>bench-root</artifactId>
    <version>1.0</version>
  </parent>
  <artifactId>common</artifactId>
  <dependencies>
    <dependency>
      <groupId>junit</groupId>
      <artifactId>junit</artifactId>
      <version>4.13.2</version>
      <scope>test</scope>
    </dependency>
  </dependencies>
</project>
EOF
cat > common/src/main/java/bench/common/Common.java <<'EOF'
package bench.common;

// Add is a small shared helper, standing in for the kind of low-level
// utility function real large codebases centralize and reuse everywhere.
public class Common {
    public static int add(int a, int b) {
        return a + b;
    }
}
EOF
cat > common/src/test/java/bench/common/CommonTest.java <<'EOF'
package bench.common;

import org.junit.Test;
import static org.junit.Assert.assertEquals;

public class CommonTest {
    @Test
    public void testAdd() {
        assertEquals(5, Common.add(2, 3));
    }
}
EOF

for i in $(seq 1 "$n"); do
	mod="feature$i"
	mkdir -p "$mod/src/main/java/bench/$mod" "$mod/src/test/java/bench/$mod"
	cat > "$mod/pom.xml" <<EOF
<project xmlns="http://maven.apache.org/POM/4.0.0">
  <modelVersion>4.0.0</modelVersion>
  <parent>
    <groupId>bench</groupId>
    <artifactId>bench-root</artifactId>
    <version>1.0</version>
  </parent>
  <artifactId>$mod</artifactId>
  <dependencies>
    <dependency>
      <groupId>bench</groupId>
      <artifactId>common</artifactId>
      <version>1.0</version>
    </dependency>
    <dependency>
      <groupId>junit</groupId>
      <artifactId>junit</artifactId>
      <version>4.13.2</version>
      <scope>test</scope>
    </dependency>
  </dependencies>
</project>
EOF
	cat > "$mod/src/main/java/bench/$mod/Feature$i.java" <<EOF
package bench.$mod;

import bench.common.Common;

// Feature$i stands in for one independent feature module's own logic -
// most large multi-module builds have many modules like this that don't
// depend on each other, only on shared low-level utility modules.
public class Feature$i {
    public static int compute(int x) {
        return Common.add(x, $i);
    }
}
EOF
	cat > "$mod/src/test/java/bench/$mod/Feature${i}Test.java" <<EOF
package bench.$mod;

import org.junit.Test;
import static org.junit.Assert.assertEquals;

public class Feature${i}Test {
    @Test
    public void testCompute() {
        assertEquals(1 + $i, Feature$i.compute(1));
    }
}
EOF
done

echo "target/" > .gitignore
git add -A
git commit -q -m "generate $n synthetic feature modules"
echo "generated $n feature modules (+ 1 shared common module) in $dir"
