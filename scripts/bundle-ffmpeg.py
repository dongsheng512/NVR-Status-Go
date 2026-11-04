#!/usr/bin/env python3
"""Bundle ffmpeg/ffprobe into the macOS app, self-contained.

MyGo copies resources/<platform>/ into the bundle: resources/darwin/bin becomes
Contents/Resources/bin, which is the first place util.ToolBinDirs looks. That
alone is not enough for a self-contained app: a Homebrew ffmpeg is a few hundred
KB of dynamic executable whose real weight sits in ~13 dylibs under
/opt/homebrew, referenced by absolute path. Copying only the two executables
would still fail with a dyld error on any machine without that exact Cellar.

So this script copies the dependency closure too, rewrites every load command to
@loader_path/, and re-signs each Mach-O ad-hoc (install_name_tool invalidates the
signature, and arm64 refuses to load unsigned code).

Output is gitignored; the bundle is built locally, never committed.

Usage:
    python3 scripts/bundle-ffmpeg.py            # bundle into resources/darwin/bin
    python3 scripts/bundle-ffmpeg.py --check    # only report what would be bundled
"""

from __future__ import annotations

import argparse
import os
import shutil
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DEFAULT_DEST = os.path.join(ROOT, "resources", "darwin", "bin")

# Anything under these prefixes ships with the OS and must not be copied.
SYSTEM_PREFIXES = ("/usr/lib/", "/System/")

# Fallbacks for when the tool is installed but not on this shell's PATH.
EXTRA_SEARCH = ("/opt/homebrew/bin", "/usr/local/bin", "/opt/local/bin")


def die(msg: str) -> None:
    print(f"error: {msg}", file=sys.stderr)
    raise SystemExit(1)


def which(name: str) -> str:
    found = shutil.which(name)
    if found:
        return os.path.realpath(found)
    for d in EXTRA_SEARCH:
        cand = os.path.join(d, name)
        if os.path.isfile(cand) and os.access(cand, os.X_OK):
            return os.path.realpath(cand)
    die(f"{name} not found; install ffmpeg first (brew install ffmpeg)")


def load_commands(path: str) -> list[str]:
    """Dependencies as reported by otool -L, minus the first (self) line."""
    out = subprocess.run(
        ["otool", "-L", path], capture_output=True, text=True, check=True
    ).stdout
    deps = []
    for line in out.splitlines()[1:]:
        line = line.strip()
        if not line:
            continue
        deps.append(line.split(" (compatibility version")[0].strip())
    return deps


def is_system(dep: str) -> bool:
    return dep.startswith(SYSTEM_PREFIXES) or dep.startswith("@executable_path/")


def collect(roots: list[str]) -> tuple[dict[str, str], dict[str, str]]:
    """BFS the dependency graph.

    Returns (files, refmap):
      files  -- bundled filename -> source path. The filename is the basename of
                the *real* file, because that is what ends up in one directory.
      refmap -- recorded load-command path -> bundled filename. otool reports the
                unversioned symlink name (libavcodec.63.dylib) while the real file
                is libavcodec.63.1.102.dylib, so rewriting has to go through this
                map rather than through basenames.
    """
    files: dict[str, str] = {}
    refmap: dict[str, str] = {}
    seen: set[str] = set()
    queue: list[str] = []

    for root in roots:
        queue.extend(d for d in load_commands(root) if not is_system(d))

    while queue:
        dep = queue.pop(0)
        if not os.path.isfile(dep):
            die(f"dependency not found on disk: {dep}")
        # Homebrew records /opt/homebrew/opt/<formula>/lib/... for some libs and
        # the Cellar path for others; both name the same inode. Compare real
        # paths so that is not mistaken for a basename collision.
        real = os.path.realpath(dep)
        name = os.path.basename(real)
        refmap[dep] = name
        if name in seen:
            continue
        seen.add(name)
        if name in files and files[name] != real:
            die(
                f"two different libraries share the name {name}:\n"
                f"  {files[name]}\n  {real}\n"
                "Bundling them into one directory would be ambiguous."
            )
        files[name] = real
        queue.extend(d for d in load_commands(real) if not is_system(d))

    return files, refmap


def rewrite(path: str, refmap: dict[str, str]) -> None:
    for dep in load_commands(path):
        name = refmap.get(dep)
        if name:
            subprocess.run(
                ["install_name_tool", "-change", dep, f"@loader_path/{name}", path],
                check=True,
                capture_output=True,
            )


def sign(path: str) -> None:
    subprocess.run(
        ["codesign", "--force", "--sign", "-", "--timestamp=none", path],
        check=True,
        capture_output=True,
    )


def absolute_leftovers(path: str) -> list[str]:
    return [
        d
        for d in load_commands(path)
        if not is_system(d) and not d.startswith("@loader_path/")
    ]


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--dest", default=DEFAULT_DEST)
    ap.add_argument("--check", action="store_true", help="report without writing")
    args = ap.parse_args()

    if sys.platform != "darwin":
        die("this script only makes sense on macOS")

    roots = [which("ffmpeg"), which("ffprobe")]
    libs, refmap = collect(roots)
    total = sum(os.path.getsize(p) for p in roots) + sum(
        os.path.getsize(p) for p in libs.values()
    )

    print(f"ffmpeg  : {roots[0]}")
    print(f"ffprobe : {roots[1]}")
    print(f"dylibs  : {len(libs)}")
    print(f"payload : {total / 1e6:.1f} MB")
    if args.check:
        for base in sorted(libs):
            print(f"  {base:<32} {libs[base]}")
        return 0

    if os.path.isdir(args.dest):
        shutil.rmtree(args.dest)
    os.makedirs(args.dest, exist_ok=True)

    # Copy dylibs first: they keep their original absolute ids until rewritten,
    # and rewriting them all in one pass afterwards avoids ordering surprises.
    for base, src in libs.items():
        shutil.copy2(src, os.path.join(args.dest, base))
    for name, src in (("ffmpeg", roots[0]), ("ffprobe", roots[1])):
        shutil.copy2(src, os.path.join(args.dest, name))

    for base in libs:
        target = os.path.join(args.dest, base)
        subprocess.run(
            ["install_name_tool", "-id", f"@loader_path/{base}", target],
            check=True,
            capture_output=True,
        )
        rewrite(target, refmap)
        sign(target)

    for name in ("ffmpeg", "ffprobe"):
        target = os.path.join(args.dest, name)
        os.chmod(target, 0o755)
        rewrite(target, refmap)
        sign(target)

    # Verification is the point: a silently half-rewritten bundle would only fail
    # on a machine without Homebrew, which is exactly where nobody tests.
    problems = 0
    everything = sorted(os.listdir(args.dest))
    for name in everything:
        path = os.path.join(args.dest, name)
        left = absolute_leftovers(path)
        if left:
            problems += 1
            print(f"FAIL {name}: still references {left}", file=sys.stderr)
    if problems:
        die(f"{problems} file(s) still point outside the bundle")

    for name in ("ffmpeg", "ffprobe"):
        res = subprocess.run(
            [os.path.join(args.dest, name), "-version"],
            capture_output=True,
            text=True,
        )
        if res.returncode != 0:
            die(f"{name} does not run from the bundle:\n{res.stderr.strip()}")
        print(f"ok  {name}: {res.stdout.splitlines()[0]}")

    size = sum(
        os.path.getsize(os.path.join(args.dest, f)) for f in everything
    )
    print(f"\nbundled {len(everything)} files, {size / 1e6:.1f} MB -> {args.dest}")
    print("next: go tool mygo build")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
