import com.sun.source.tree.ClassTree;
import com.sun.source.tree.CompilationUnitTree;
import com.sun.source.tree.IdentifierTree;
import com.sun.source.tree.ImportTree;
import com.sun.source.tree.MemberSelectTree;
import com.sun.source.tree.Tree;
import com.sun.source.util.JavacTask;
import com.sun.source.util.TreeScanner;

import javax.tools.Diagnostic;
import javax.tools.DiagnosticCollector;
import javax.tools.JavaCompiler;
import javax.tools.JavaFileObject;
import javax.tools.StandardJavaFileManager;
import javax.tools.ToolProvider;

import java.io.BufferedReader;
import java.io.File;
import java.io.InputStreamReader;
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.HashSet;
import java.util.List;
import java.util.Set;
import java.util.TreeSet;

// FastciJavaParse is fastci's own small shim onto the JDK's own Compiler
// Tree API (com.sun.source.*) - the exact same frontend parser javac
// itself uses, run in parse-only mode (JavacTask.parse(), never
// .analyze()), so it needs nothing beyond the source files themselves: no
// classpath, no dependency resolution, no successful compile. This is
// deliberately the same shape as pytestanalyzer's own embedded
// resolve_imports.py - delegate to the real language tooling's own
// parser, run as a short-lived subprocess, rather than reimplementing a
// parser from scratch.
//
// Reads one absolute .java file path per line from stdin (so an
// arbitrarily large file list never risks an argv length limit) and
// prints one JSON object to stdout: {"files": {"<path>": {...}, ...}}.
// Per file:
//   "package"   - the package name ("" for the default package).
//   "types"     - every top-level type's simple name declared in the
//                 file (a real parse handles more than one per file
//                 correctly, unlike a filename-matching assumption).
//   "imports"   - every import that names one specific, resolvable type:
//                 a plain single-type import ("import a.b.C;"), a static
//                 member import ("import static a.b.C.FOO;" - the type is
//                 a.b.C, FOO is a member, not a type, so the member name
//                 is dropped here using the parser's own knowledge of
//                 which it is, not a guess), and a static wildcard import
//                 ("import static a.b.C.*;" - same reasoning: the type is
//                 still a.b.C). All three are resolved identically on the
//                 Go side: an exact fully-qualified-name match against
//                 another file's own declared type.
//   "wildcardImports" - a plain, non-static wildcard import's package
//                 name ("import a.b.*;" -> "a.b") - this is the one case
//                 that isn't one specific type, so it's resolved
//                 differently (against every type declared in that
//                 package, filtered by whether this file's own
//                 identifiers actually mention it - see javaanalyzer.go).
//   "identifiers" - every real identifier/member-name token the parser
//                 found anywhere in the file's actual code (not its
//                 comments or string literals, unlike a text-based scan) -
//                 used for the same-package-reference and wildcard-import
//                 resolution above, neither of which a parse-only pass
//                 can fully resolve without a complete classpath.
//
// A file with any parse error is dropped entirely (not included in the
// output at all) rather than risk emitting data recovered from a broken
// parse tree - the same "skip it, don't guess" conservatism as a file
// that fails to even open.
public class FastciJavaParse {
    public static void main(String[] args) throws Exception {
        List<String> paths = new ArrayList<>();
        BufferedReader in = new BufferedReader(new InputStreamReader(System.in, StandardCharsets.UTF_8));
        String line;
        while ((line = in.readLine()) != null) {
            line = line.trim();
            if (!line.isEmpty()) {
                paths.add(line);
            }
        }

        StringBuilder out = new StringBuilder();
        out.append("{\"files\":{");
        if (!paths.isEmpty()) {
            appendFiles(out, paths);
        }
        out.append("}}");
        System.out.println(out);
    }

    private static void appendFiles(StringBuilder out, List<String> paths) throws Exception {
        JavaCompiler compiler = ToolProvider.getSystemJavaCompiler();
        DiagnosticCollector<JavaFileObject> diagnostics = new DiagnosticCollector<>();
        StandardJavaFileManager fm = compiler.getStandardFileManager(diagnostics, null, StandardCharsets.UTF_8);
        Iterable<? extends JavaFileObject> units = fm.getJavaFileObjectsFromStrings(paths);
        JavacTask task = (JavacTask) compiler.getTask(null, fm, diagnostics, List.of(), null, units);
        Iterable<? extends CompilationUnitTree> trees = task.parse();

        Set<JavaFileObject> broken = new HashSet<>();
        for (Diagnostic<? extends JavaFileObject> d : diagnostics.getDiagnostics()) {
            if (d.getKind() == Diagnostic.Kind.ERROR && d.getSource() != null) {
                broken.add(d.getSource());
            }
        }

        boolean first = true;
        for (CompilationUnitTree cu : trees) {
            if (broken.contains(cu.getSourceFile())) {
                continue;
            }
            if (!first) {
                out.append(",");
            }
            first = false;
            appendFile(out, cu);
        }
    }

    private static void appendFile(StringBuilder out, CompilationUnitTree cu) {
        String path = new File(cu.getSourceFile().toUri()).getPath();
        String pkg = cu.getPackageName() == null ? "" : cu.getPackageName().toString();

        List<String> types = new ArrayList<>();
        for (Tree t : cu.getTypeDecls()) {
            if (t instanceof ClassTree) {
                types.add(((ClassTree) t).getSimpleName().toString());
            }
        }

        List<String> imports = new ArrayList<>();
        List<String> wildcardImports = new ArrayList<>();
        for (ImportTree imp : cu.getImports()) {
            String q = imp.getQualifiedIdentifier().toString();
            if (q.endsWith(".*")) {
                String base = q.substring(0, q.length() - 2);
                if (imp.isStatic()) {
                    imports.add(base); // "import static a.b.C.*;" - C is the type.
                } else {
                    wildcardImports.add(base); // "import a.b.*;" - a.b is a package.
                }
            } else if (imp.isStatic()) {
                int i = q.lastIndexOf('.');
                imports.add(i > 0 ? q.substring(0, i) : q); // drop the trailing member name.
            } else {
                imports.add(q);
            }
        }

        // Deliberately doesn't descend into import or package
        // declarations at all (overriding visitImport/visitPackage to
        // stop there, rather than calling the super method) - without
        // this, "import a.b.C;" and "package a.b;" both contribute "a",
        // "b", "C" purely from the declaration text itself, even when
        // the file's actual code never once refers to any of them by
        // name - a real false-positive source found by testing this
        // shim directly, twice, before wiring it into Go at all.
        Set<String> identifiers = new TreeSet<>();
        new TreeScanner<Void, Void>() {
            @Override
            public Void visitImport(ImportTree node, Void p) {
                return null;
            }

            @Override
            public Void visitPackage(com.sun.source.tree.PackageTree node, Void p) {
                return null;
            }

            @Override
            public Void visitIdentifier(IdentifierTree node, Void p) {
                identifiers.add(node.getName().toString());
                return super.visitIdentifier(node, p);
            }

            @Override
            public Void visitMemberSelect(MemberSelectTree node, Void p) {
                identifiers.add(node.getIdentifier().toString());
                return super.visitMemberSelect(node, p);
            }
        }.scan(cu, null);

        out.append(jsonString(path)).append(":{");
        out.append("\"package\":").append(jsonString(pkg)).append(",");
        out.append("\"types\":").append(jsonArray(types)).append(",");
        out.append("\"imports\":").append(jsonArray(imports)).append(",");
        out.append("\"wildcardImports\":").append(jsonArray(wildcardImports)).append(",");
        out.append("\"identifiers\":").append(jsonArray(new ArrayList<>(identifiers)));
        out.append("}");
    }

    private static String jsonArray(List<String> items) {
        List<String> encoded = new ArrayList<>(items.size());
        for (String item : items) {
            encoded.add(jsonString(item));
        }
        return "[" + String.join(",", encoded) + "]";
    }

    private static String jsonString(String s) {
        StringBuilder sb = new StringBuilder("\"");
        for (int i = 0; i < s.length(); i++) {
            char c = s.charAt(i);
            switch (c) {
                case '"':
                    sb.append("\\\"");
                    break;
                case '\\':
                    sb.append("\\\\");
                    break;
                case '\n':
                    sb.append("\\n");
                    break;
                case '\r':
                    sb.append("\\r");
                    break;
                case '\t':
                    sb.append("\\t");
                    break;
                default:
                    if (c < 0x20) {
                        sb.append(String.format("\\u%04x", (int) c));
                    } else {
                        sb.append(c);
                    }
            }
        }
        return sb.append("\"").toString();
    }
}
