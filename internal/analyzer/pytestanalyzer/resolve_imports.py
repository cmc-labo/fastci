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

Known limitations (by design, to stay static): a call to
`importlib.import_module(...)` or `__import__(...)` is *detected* - the
containing file is flagged "dynamic": true in the output regardless of
whether the call's argument happens to be a literal - but its target isn't
resolved to a specific edge, so the caller (pytestanalyzer.go) treats the
whole file as always possibly affected rather than trying to attribute
individual changes to it. Plugin/entry-point style loading through some
other indirection, and star re-exports that obscure a symbol's true origin,
aren't detected at all.

Per-file AST parsing is cached across runs in
`<project_dir>/.fastci-cache/pytest-imports.json`, keyed by each file's
mtime and size: a file whose (mtime, size) hasn't changed since the cache
was written reuses its previous result instead of being re-parsed. This
cache is only trusted when the *set* of tracked files is identical to what
it was when written (tracked via a fingerprint of every file's relative
path) - adding, removing, or renaming any .py file anywhere in the project
invalidates the whole cache for that run (falling back to a full reparse,
same as having no cache at all) rather than risk reusing a resolution that
a changed registry could have made stale. mtime+size is a fast check, not a
content hash: a file edited twice within the same mtime tick that also
happens to land on the exact same byte size (rare) could be missed - if
that's a concern for a given project, delete `.fastci-cache/` to force a
full reparse.
"""

import ast
import importlib.util
import json
import os
import sys
import tempfile

SKIP_DIRS = {
    ".git", "__pycache__", "node_modules", ".venv", "venv", "env",
    ".tox", ".mypy_cache", ".pytest_cache", ".ruff_cache", "build",
    "dist", ".eggs", "site-packages", ".fastci-cache",
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


def cache_path(project_dir):
    return os.path.join(project_dir, ".fastci-cache", "pytest-imports.json")


def load_cache(project_dir, fingerprint):
    """Returns the cached {relkey: {"mtime": ..., "size": ..., "imports":
    ..., "dynamic": ...}} map if a cache file exists, is valid JSON, and was
    written against the same set of tracked files - {} otherwise (no cache,
    corrupt cache, or a file was added/removed/renamed since it was
    written)."""
    try:
        with open(cache_path(project_dir), "r", encoding="utf-8") as fh:
            data = json.load(fh)
    except (OSError, ValueError):
        return {}
    if not isinstance(data, dict) or data.get("fingerprint") != fingerprint:
        return {}
    files = data.get("files")
    return files if isinstance(files, dict) else {}


def save_cache(project_dir, fingerprint, entries):
    path = cache_path(project_dir)
    try:
        os.makedirs(os.path.dirname(path), exist_ok=True)
        # A cache directory that isn't excluded from version control by the
        # project itself would otherwise get swept into `git add -A` -
        # this makes that a non-issue regardless.
        gitignore = os.path.join(os.path.dirname(path), ".gitignore")
        if not os.path.exists(gitignore):
            with open(gitignore, "w", encoding="utf-8") as fh:
                fh.write("*\n")
        fd, tmp = tempfile.mkstemp(dir=os.path.dirname(path))
        with os.fdopen(fd, "w", encoding="utf-8") as fh:
            json.dump({"fingerprint": fingerprint, "files": entries}, fh)
        os.replace(tmp, path)
    except OSError:
        pass  # best-effort - a write failure just means no speedup next run.


def parse_file(f, package):
    """Parses f's AST and returns its raw (unresolved) import targets and
    whether it contains a dynamic import call. Never imports/executes f -
    see the module docstring."""
    try:
        with open(f, "r", encoding="utf-8", errors="replace") as fh:
            tree = ast.parse(fh.read(), filename=f)
    except SyntaxError:
        return [], False

    targets = []
    dynamic = False
    for node in ast.walk(tree):
        if isinstance(node, ast.Import):
            for alias in node.names:
                targets.append(alias.name)
        elif isinstance(node, ast.ImportFrom):
            base = None
            if node.level and node.level > 0:
                spec = "." * node.level + (node.module or "")
                try:
                    base = importlib.util.resolve_name(spec, package)
                except (ImportError, ValueError):
                    pass
            elif node.module:
                base = node.module
            if base:
                targets.append(base)
                # `from pkg import name` is ambiguous from the AST alone:
                # name may be a submodule (pkg/name.py) or just a symbol
                # defined in pkg's __init__.py. Also try it as a
                # submodule - if it isn't one, resolve_target simply
                # won't find it in the registry, so this only ever adds
                # a real edge, never a wrong one.
                for alias in node.names:
                    targets.append(base + "." + alias.name)
        elif isinstance(node, ast.Call):
            func = node.func
            # importlib.import_module(...) / import_module(...) (however
            # imported) and bare/builtins.__import__(...) - the argument
            # isn't inspected, even a literal is treated as dynamic; see
            # the module docstring for why.
            if isinstance(func, ast.Name) and func.id in ("__import__", "import_module"):
                dynamic = True
            elif isinstance(func, ast.Attribute) and func.attr in ("import_module", "__import__"):
                dynamic = True
    return targets, dynamic


def main():
    project_dir = os.path.abspath(sys.argv[1])
    roots = compute_roots(project_dir)
    files = discover_files(project_dir)

    def relkey(path):
        return os.path.relpath(path, project_dir).replace(os.sep, "/")

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

    # The fingerprint only needs to capture the *set* of tracked files -
    # module_name_for is a pure function of relkey and roots (themselves
    # derived only from project_dir), so an unchanged file set can only
    # resolve to the same registry.
    fingerprint_input = "\n".join(sorted(relkey(f) for f in files))
    cache = load_cache(project_dir, fingerprint_input)

    result_files = {}
    cache_entries = {}
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

        key = relkey(f)
        try:
            st = os.stat(f)
        except OSError:
            st = None

        cached = cache.get(key)
        if st is not None and cached is not None and cached.get("mtime") == st.st_mtime_ns and cached.get("size") == st.st_size:
            imports = cached.get("imports", [])
            dynamic = cached.get("dynamic", False)
        else:
            targets, dynamic = parse_file(f, package)
            imports = []
            seen = set()
            for t in targets:
                rp = resolve_target(t)
                if rp and rp != f and rp not in seen:
                    seen.add(rp)
                    imports.append(relkey(rp))

        result_files[key] = {"imports": imports, "dynamic": dynamic}
        if st is not None:
            cache_entries[key] = {
                "mtime": st.st_mtime_ns,
                "size": st.st_size,
                "imports": imports,
                "dynamic": dynamic,
            }

    save_cache(project_dir, fingerprint_input, cache_entries)
    json.dump({"files": result_files}, sys.stdout)


if __name__ == "__main__":
    main()
