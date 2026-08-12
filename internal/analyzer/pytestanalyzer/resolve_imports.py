"""fastci's Python import-graph resolver.

Invoked as `python3 resolve_imports.py <project_dir>`. Prints a JSON object
describing every tracked .py file under project_dir and, for each one, the
other tracked files it imports.

This intentionally never imports/executes any of the target project's own
code (which could have side effects, missing dependencies, or be slow) -
it only parses each file's AST and resolves import targets *statically*
against a registry of every .py file's dotted module name, built by
walking the directory tree ourselves. Relative imports (`from . import x`,
`from ..pkg import y`) are normalized to absolute dotted names with the
stdlib's own `importlib.util.resolve_name`, which is pure string logic and
does not import anything either.

Known limitations (by design, to stay static): dynamic imports
(`importlib.import_module(...)`, `__import__`, plugin/entry-point style
loading) and star re-exports that obscure a symbol's true origin aren't
tracked. An import that can't be resolved to a project file (stdlib,
third-party, or something dynamic) is simply treated as external and
omitted - the caller falls back to a full test run for changes it can't
attribute anywhere else, same as an unresolvable Go or JS/TS file.
"""

import ast
import importlib.util
import json
import os
import sys

SKIP_DIRS = {
    ".git", "__pycache__", "node_modules", ".venv", "venv", "env",
    ".tox", ".mypy_cache", ".pytest_cache", ".ruff_cache", "build",
    "dist", ".eggs", "site-packages",
}


def discover_files(project_dir):
    out = []
    for dirpath, dirnames, filenames in os.walk(project_dir):
        dirnames[:] = [
            d for d in dirnames
            if d not in SKIP_DIRS and not d.endswith(".egg-info")
        ]
        for fn in filenames:
            if fn.endswith(".py"):
                out.append(os.path.join(dirpath, fn))
    return out


def compute_roots(project_dir):
    roots = []
    src = os.path.join(project_dir, "src")
    if os.path.isdir(src):
        roots.append(os.path.abspath(src))
    roots.append(os.path.abspath(project_dir))
    return roots


def module_name_for(path, roots):
    best = None
    for root in roots:
        if path == root or path.startswith(root + os.sep):
            if best is None or len(root) > len(best):
                best = root
    if best is None:
        return None
    rel = os.path.relpath(path, best)
    parts = rel.split(os.sep)
    if parts[-1] == "__init__.py":
        parts = parts[:-1]
    else:
        parts[-1] = parts[-1][:-3]
    parts = [p for p in parts if p]
    if not parts:
        return None
    return ".".join(parts)


def main():
    project_dir = os.path.abspath(sys.argv[1])
    roots = compute_roots(project_dir)
    files = discover_files(project_dir)

    file_module = {}
    registry = {}
    for f in files:
        mod = module_name_for(f, roots)
        if mod:
            file_module[f] = mod
            # First file to claim a dotted name wins; a real conflict here
            # (two files mapping to the same module name) means the
            # project layout is ambiguous in a way we can't resolve
            # statically anyway.
            registry.setdefault(mod, f)

    def resolve_target(dotted):
        if dotted in registry:
            return registry[dotted]
        if "." in dotted:
            parent = dotted.rsplit(".", 1)[0]
            if parent in registry:
                return registry[parent]
        return None

    def relkey(path):
        return os.path.relpath(path, project_dir).replace(os.sep, "/")

    result_files = {}
    for f in files:
        mod = file_module.get(f)
        is_init = os.path.basename(f) == "__init__.py"
        if mod is None:
            package = ""
        elif is_init:
            package = mod
        elif "." in mod:
            package = mod.rsplit(".", 1)[0]
        else:
            package = ""

        try:
            with open(f, "r", encoding="utf-8", errors="replace") as fh:
                tree = ast.parse(fh.read(), filename=f)
        except SyntaxError:
            result_files[relkey(f)] = {"imports": []}
            continue

        targets = []
        for node in ast.walk(tree):
            if isinstance(node, ast.Import):
                for alias in node.names:
                    targets.append(alias.name)
            elif isinstance(node, ast.ImportFrom):
                if node.level and node.level > 0:
                    spec = "." * node.level + (node.module or "")
                    try:
                        targets.append(importlib.util.resolve_name(spec, package))
                    except (ImportError, ValueError):
                        pass
                elif node.module:
                    targets.append(node.module)

        imports = []
        seen = set()
        for t in targets:
            rp = resolve_target(t)
            if rp and rp != f and rp not in seen:
                seen.add(rp)
                imports.append(relkey(rp))

        result_files[relkey(f)] = {"imports": imports}

    json.dump({"files": result_files}, sys.stdout)


if __name__ == "__main__":
    main()
