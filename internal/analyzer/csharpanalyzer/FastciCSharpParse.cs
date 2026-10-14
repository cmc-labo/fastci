using System;
using System.Collections.Generic;
using System.IO;
using System.Text;
using Microsoft.CodeAnalysis;
using Microsoft.CodeAnalysis.CSharp;
using Microsoft.CodeAnalysis.CSharp.Syntax;

// FastciCSharpParse is fastci's own small shim onto Roslyn
// (Microsoft.CodeAnalysis.CSharp) - the same compiler frontend `csc`
// itself uses, run in parse-only mode (CSharpSyntaxTree.ParseText, never a
// Compilation/SemanticModel), so it needs nothing beyond the source files
// themselves: no project references, no NuGet restore, no successful
// build. The assemblies this links against are the ones already bundled
// inside every .NET SDK install at <sdk>/Roslyn/bincore (the same ones
// `csc.dll`/`vbc.dll` themselves use) - not a separate NuGet package - see
// csharpanalyzer.go's own doc comment for exactly how this gets compiled
// and run without a restore step.
//
// Reads one absolute .cs file path per line from stdin (so an arbitrarily
// large file list never risks an argv length limit) and prints one JSON
// object to stdout: {"files": {"<path>": {...}, ...}}. Per file:
//   "types"          - every type declared anywhere in the file (nested
//                       types included, each with its own entry - a real
//                       parse handles arbitrary nesting correctly, unlike
//                       a "top-level only" assumption): each entry is
//                       {"namespace": "...", "fqcn": "..."}, where fqcn
//                       uses "." between namespace segments and "+"
//                       between an outer and inner type (the same
//                       notation reflection's own Type.FullName uses for a
//                       nested type).
//   "usings"         - every plain "using X;" directive's namespace,
//                       found anywhere in the file (compilation-unit level
//                       or inside a namespace block) - excludes `global`
//                       ones (see "globalUsings").
//   "directTypeRefs" - every "using static X.Y;" or "using Alias = X.Y;"
//                       directive's target (Y is dropped as a member
//                       access the same way javaanalyzer drops a
//                       static-import's member name) - both name one
//                       specific type (or, for an alias, possibly a
//                       namespace - see csharpanalyzer.go's Build for how
//                       that's handled) rather than "any member of this
//                       namespace", so both are resolved by exact
//                       fully-qualified-name match on the Go side.
//                       Excludes `global` ones (see "globalUsings").
//   "globalUsings"   - the same three kinds (plain/static/alias), but for
//                       a `global using` directive (C# 10+) specifically:
//                       applies to every file in the project, not just
//                       this one, so it's kept separate for the Go side
//                       to apply project-wide rather than only to the
//                       file declaring it. Each entry is {"kind": "plain"
//                       | "static" | "alias", "target": "..."}.
//   "identifiers"    - every real identifier token the parser found
//                       anywhere in the file's actual code (not its
//                       comments or string literals, unlike a text-based
//                       scan, and not a using directive's own target
//                       text) - used for the same-namespace-reference and
//                       plain-using resolution, neither of which a
//                       parse-only pass can fully resolve without a
//                       complete semantic model (C#'s own "using X;"
//                       names a namespace, not one type, so it's "this
//                       file can reference anything in X", not a pinpoint
//                       reference - see csharpanalyzer.go).
//   "hasTestAttribute" - whether the file contains a recognized
//                       test-framework attribute ([Fact]/[Theory] for
//                       xUnit, [Test]/[TestCase] for NUnit, [TestMethod]/
//                       [DataTestMethod] for MSTest, with or without the
//                       attribute-class's own "Attribute" suffix) - see
//                       isTestFile in csharpanalyzer.go for why this, not
//                       the filename, is the real signal for C#.
//
// A file with any parse error (ParseErrorsOnly below) is dropped entirely
// from the output, not included at all - the same "skip it, don't guess
// from a broken recovery tree" conservatism FastciJavaParse.java uses.
public static class FastciCSharpParse
{
    private static readonly HashSet<string> TestAttributeNames = new HashSet<string>(StringComparer.Ordinal)
    {
        "Fact", "Theory", "Test", "TestCase", "TestMethod", "DataTestMethod",
    };

    public static void Main()
    {
        var paths = new List<string>();
        string line;
        while ((line = Console.In.ReadLine()) != null)
        {
            line = line.Trim();
            if (line.Length > 0)
            {
                paths.Add(line);
            }
        }

        var sb = new StringBuilder();
        sb.Append("{\"files\":{");
        bool first = true;
        foreach (var path in paths)
        {
            string entry = ParseOne(path);
            if (entry == null)
            {
                continue; // unreadable, or a genuine parse error - see the doc comment above.
            }
            if (!first)
            {
                sb.Append(",");
            }
            first = false;
            sb.Append(JsonString(path)).Append(":").Append(entry);
        }
        sb.Append("}}");
        Console.Out.Write(sb.ToString());
    }

    private static string ParseOne(string path)
    {
        string text;
        try
        {
            text = File.ReadAllText(path);
        }
        catch (IOException)
        {
            return null;
        }

        var options = new CSharpParseOptions(LanguageVersion.Preview);
        var tree = CSharpSyntaxTree.ParseText(text, options, path);
        foreach (var diag in tree.GetDiagnostics())
        {
            if (diag.Severity == DiagnosticSeverity.Error)
            {
                return null;
            }
        }

        var root = tree.GetCompilationUnitRoot();

        var types = new List<TypeEntry>();
        var usings = new List<UsingEntry>();
        WalkContainer(root, new List<string>(), new List<string>(), types, usings);

        var identifiers = new SortedSet<string>(StringComparer.Ordinal);
        CollectIdentifiers(root, identifiers);

        bool hasTestAttribute = HasTestAttribute(root);

        var plain = new List<string>();
        var direct = new List<string>();
        var global = new List<UsingEntry>();
        foreach (var u in usings)
        {
            if (u.Global)
            {
                global.Add(u);
                continue;
            }
            if (u.Kind == "plain")
            {
                plain.Add(u.Target);
            }
            else
            {
                direct.Add(u.Target);
            }
        }

        var sb = new StringBuilder();
        sb.Append("{");
        sb.Append("\"types\":[");
        for (int i = 0; i < types.Count; i++)
        {
            if (i > 0) sb.Append(",");
            sb.Append("{\"namespace\":").Append(JsonString(types[i].Namespace))
              .Append(",\"fqcn\":").Append(JsonString(types[i].Fqcn)).Append("}");
        }
        sb.Append("],");
        sb.Append("\"usings\":").Append(JsonArray(plain)).Append(",");
        sb.Append("\"directTypeRefs\":").Append(JsonArray(direct)).Append(",");
        sb.Append("\"globalUsings\":[");
        for (int i = 0; i < global.Count; i++)
        {
            if (i > 0) sb.Append(",");
            sb.Append("{\"kind\":").Append(JsonString(global[i].Kind))
              .Append(",\"target\":").Append(JsonString(global[i].Target)).Append("}");
        }
        sb.Append("],");
        sb.Append("\"identifiers\":").Append(JsonArray(new List<string>(identifiers))).Append(",");
        sb.Append("\"hasTestAttribute\":").Append(hasTestAttribute ? "true" : "false");
        sb.Append("}");
        return sb.ToString();
    }

    private class TypeEntry
    {
        public string Namespace;
        public string Fqcn;
    }

    private class UsingEntry
    {
        public string Kind; // "plain", "static", or "alias"
        public string Target;
        public bool Global;
    }

    // WalkContainer recurses through compilation-unit/namespace nesting
    // only (the only places a `using` directive or a type declaration can
    // appear at - C# doesn't allow either loose inside a method body,
    // aside from the rare local-function/local-class case this
    // deliberately doesn't special-case), tracking the enclosing namespace
    // segments and outer-type names along the way so every type - at any
    // nesting depth - gets its own correct fqcn.
    private static void WalkContainer(SyntaxNode node, List<string> namespaceStack, List<string> outerStack,
        List<TypeEntry> types, List<UsingEntry> usings)
    {
        if (node is CompilationUnitSyntax cu)
        {
            foreach (var u in cu.Usings) usings.Add(MakeUsingEntry(u));
            foreach (var member in cu.Members) WalkMember(member, namespaceStack, outerStack, types, usings);
        }
        else if (node is BaseNamespaceDeclarationSyntax ns)
        {
            namespaceStack.Add(ns.Name.ToString());
            foreach (var u in ns.Usings) usings.Add(MakeUsingEntry(u));
            foreach (var member in ns.Members) WalkMember(member, namespaceStack, outerStack, types, usings);
            namespaceStack.RemoveAt(namespaceStack.Count - 1);
        }
    }

    private static void WalkMember(MemberDeclarationSyntax member, List<string> namespaceStack, List<string> outerStack,
        List<TypeEntry> types, List<UsingEntry> usings)
    {
        if (member is BaseNamespaceDeclarationSyntax)
        {
            WalkContainer(member, namespaceStack, outerStack, types, usings);
            return;
        }
        if (member is TypeDeclarationSyntax td)
        {
            types.Add(MakeTypeEntry(td.Identifier.Text, namespaceStack, outerStack));
            outerStack.Add(td.Identifier.Text);
            foreach (var inner in td.Members) WalkMember(inner, namespaceStack, outerStack, types, usings);
            outerStack.RemoveAt(outerStack.Count - 1);
            return;
        }
        if (member is EnumDeclarationSyntax ed)
        {
            types.Add(MakeTypeEntry(ed.Identifier.Text, namespaceStack, outerStack));
        }
        // Anything else (a method, field, property, ...) can't contain a
        // nested type or namespace in ordinary top-level code - not
        // descended into further.
    }

    private static TypeEntry MakeTypeEntry(string simpleName, List<string> namespaceStack, List<string> outerStack)
    {
        string ns = string.Join(".", namespaceStack);
        string fqcn = simpleName;
        for (int i = outerStack.Count - 1; i >= 0; i--)
        {
            fqcn = outerStack[i] + "+" + fqcn;
        }
        if (ns.Length > 0)
        {
            fqcn = ns + "." + fqcn;
        }
        return new TypeEntry { Namespace = ns, Fqcn = fqcn };
    }

    private static UsingEntry MakeUsingEntry(UsingDirectiveSyntax u)
    {
        string target = u.Name != null ? u.Name.ToString() : "";
        bool isGlobal = !u.GlobalKeyword.IsKind(SyntaxKind.None);
        bool isStatic = !u.StaticKeyword.IsKind(SyntaxKind.None);
        string kind = u.Alias != null ? "alias" : (isStatic ? "static" : "plain");
        return new UsingEntry { Kind = kind, Target = target, Global = isGlobal };
    }

    // CollectIdentifiers walks every token in the tree, recording every
    // plain identifier token found - except inside a using directive
    // (whose own target text isn't a real code reference) or a namespace
    // declaration's own dotted name (same reasoning) - the exact bug
    // FastciJavaParse.java's own visitImport/visitPackage overrides exist
    // to avoid, applied here by simply never descending into either.
    private static void CollectIdentifiers(SyntaxNode node, SortedSet<string> identifiers)
    {
        if (node is UsingDirectiveSyntax)
        {
            return;
        }
        foreach (var token in node.ChildTokens())
        {
            if (token.IsKind(SyntaxKind.IdentifierToken))
            {
                identifiers.Add(token.Text);
            }
        }
        foreach (var child in node.ChildNodes())
        {
            if (node is BaseNamespaceDeclarationSyntax nsDecl && ReferenceEquals(child, nsDecl.Name))
            {
                continue;
            }
            CollectIdentifiers(child, identifiers);
        }
    }

    private static bool HasTestAttribute(SyntaxNode node)
    {
        if (node is AttributeSyntax attr)
        {
            string name = SimpleName(attr.Name);
            if (name.EndsWith("Attribute", StringComparison.Ordinal))
            {
                name = name.Substring(0, name.Length - "Attribute".Length);
            }
            if (TestAttributeNames.Contains(name))
            {
                return true;
            }
        }
        foreach (var child in node.ChildNodes())
        {
            if (HasTestAttribute(child))
            {
                return true;
            }
        }
        return false;
    }

    private static string SimpleName(NameSyntax name)
    {
        if (name is QualifiedNameSyntax q)
        {
            return SimpleName(q.Right);
        }
        if (name is SimpleNameSyntax s)
        {
            return s.Identifier.Text;
        }
        return name.ToString();
    }

    private static string JsonArray(List<string> items)
    {
        var sb = new StringBuilder("[");
        for (int i = 0; i < items.Count; i++)
        {
            if (i > 0) sb.Append(",");
            sb.Append(JsonString(items[i]));
        }
        sb.Append("]");
        return sb.ToString();
    }

    private static string JsonString(string s)
    {
        var sb = new StringBuilder("\"");
        foreach (char c in s)
        {
            switch (c)
            {
                case '"':
                    sb.Append("\\\"");
                    break;
                case '\\':
                    sb.Append("\\\\");
                    break;
                case '\n':
                    sb.Append("\\n");
                    break;
                case '\r':
                    sb.Append("\\r");
                    break;
                case '\t':
                    sb.Append("\\t");
                    break;
                default:
                    if (c < 0x20)
                    {
                        sb.Append("\\u").Append(((int)c).ToString("x4"));
                    }
                    else
                    {
                        sb.Append(c);
                    }
                    break;
            }
        }
        sb.Append("\"");
        return sb.ToString();
    }
}
