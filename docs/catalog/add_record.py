#!/usr/bin/env python3
"""Edit docs/catalog/catalog.json without colliding with another branch that is
editing it too.

    python3 docs/catalog/add_record.py add record.json   # allocate an id, insert, write
    python3 docs/catalog/add_record.py resolve           # settle a merge/rebase conflict
    python3 docs/catalog/add_record.py sort              # put records in canonical order
    python3 docs/catalog/add_record.py sync              # capability bodies from systems/*.md

Why it exists: more than one agent writes catalog records at once. Ids are
numbered globally across classes, so two branches that each take "the next
number" take the same one. And records used to be appended at the tail, so two
branches' new records always touched the same lines and conflicted even when
their ids did not.

Two rules fix that:

  * **Canonical order.** Records are sorted by class (capability, decision,
    gap, tool), then by number. A new gap lands at the end of the gap block
    and a new tool at the end of the tool block, so branches that add
    different classes touch different lines and merge cleanly. validate.py
    enforces the order.
  * **Allocate late.** `add` takes the next number above every id in this
    checkout *and* on origin/main (fetched first), so run it after rebasing
    onto current main, just before you push. When two branches still take
    the same number, `resolve` renumbers yours.

`resolve` is for a conflict in catalog.json during `git rebase` or
`git merge`. It reads the three versions git staged — the merge base, the
branch being merged in, and yours — and merges them record by record:

  * a record only one side changed takes that side's version;
  * a record you added keeps its id unless the other side took the id or
    its number, in which case it is renumbered, and the new id is written
    into your other new records and into any systems/*.md the other side
    does not already use the old id in;
  * a record both sides changed differently is a real conflict and is left
    for you, unless it is a capability that differs only in its body, which
    `sync` rebuilds from its systems/*.md anyway.

Then it syncs capability bodies, sorts, writes, and `git add`s the file. It
prints every renumbering, because commit messages and PR descriptions that
name the old id are yours to fix.

The output keeps main's serialization: indent 2, non-ASCII kept as is, and a
trailing newline.
"""
import json, os, re, subprocess, sys, glob

HERE = os.path.dirname(os.path.abspath(__file__))
CATALOG = os.path.join(HERE, "catalog.json")
SYSTEMS = os.path.join(HERE, "systems")
REL = "docs/catalog/catalog.json"

CLASS_ORDER = ["capability", "decision", "gap", "tool"]
PREFIX = {"capability": "CAP", "tool": "TOOL", "gap": "GAP", "decision": "DEC"}
ID_RE = re.compile(r"^CERB-([A-Z]+)-(\d+)$")


def die(msg):
    print(msg, file=sys.stderr)
    sys.exit(1)


def git(*args, check=True):
    # From the top of the work tree: REL and the :<stage>:<path> forms are
    # paths from there.
    top = subprocess.run(["git", "rev-parse", "--show-toplevel"], cwd=HERE, capture_output=True, text=True).stdout.strip() or HERE
    out = subprocess.run(["git", *args], cwd=top, capture_output=True, text=True)
    if check and out.returncode != 0:
        die(f"git {' '.join(args)}: {out.stderr.strip()}")
    return out.stdout if out.returncode == 0 else None


def number(rid):
    m = ID_RE.match(rid or "")
    return int(m.group(2)) if m else None


def sort_key(rec):
    cls = rec.get("class")
    return (CLASS_ORDER.index(cls) if cls in CLASS_ORDER else len(CLASS_ORDER), number(rec.get("id")) or 0, rec.get("id") or "")


def load(text):
    doc = json.loads(text)
    if not isinstance(doc, dict) or not isinstance(doc.get("records"), list):
        die("catalog.json must be an object with a `records` array")
    return doc


def dump(doc):
    doc["records"].sort(key=sort_key)
    return json.dumps(doc, indent=2, ensure_ascii=False) + "\n"


def write(doc):
    with open(CATALOG, "w", encoding="utf-8") as f:
        f.write(dump(doc))


def read_local():
    with open(CATALOG, encoding="utf-8") as f:
        return load(f.read())


def numbers_in(doc):
    return {n for n in (number(r.get("id")) for r in doc["records"]) if n is not None}


def main_catalog(fetch=True):
    if fetch:
        git("fetch", "-q", "origin", "main", check=False)
    text = git("show", f"origin/main:{REL}", check=False)
    return load(text) if text else None


def next_free(*docs, reserved=()):
    used = set(reserved)
    for doc in docs:
        if doc is not None:
            used |= numbers_in(doc)
    return max(used, default=0) + 1


def capability_body(md_text):
    """The body validate.py compares: the prose after the frontmatter, less
    its leading heading."""
    parts = md_text.split("---\n", 2)
    if len(parts) < 3:
        return md_text.strip()
    return re.sub(r"^#\s+.*\n+", "", parts[2].strip(), count=1).strip()


def norm(text):
    return re.sub(r"\s+", " ", text or "").strip()


def sync(doc):
    by_id = {r["id"]: r for r in doc["records"]}
    changed = []
    for path in sorted(glob.glob(os.path.join(SYSTEMS, "*.md"))):
        with open(path, encoding="utf-8") as f:
            text = f.read()
        m = re.search(r'^id:\s*"?([A-Z0-9-]+)"?\s*$', text, re.M)
        if not m or m.group(1) not in by_id:
            continue
        rec = by_id[m.group(1)]
        body = capability_body(text)
        # Compared as validate.py compares them, so a whitespace-only
        # difference is not rewritten.
        if norm(rec.get("body")) != norm(body):
            rec["body"] = body
            changed.append(rec["id"])
    return changed


def cmd_add(args):
    if not args:
        die("usage: add_record.py add <record.json> [--no-fetch]")
    fetch = "--no-fetch" not in args
    path = [a for a in args if not a.startswith("--")][0]
    with open(path, encoding="utf-8") as f:
        rec = json.load(f)
    cls = rec.get("class")
    if cls not in PREFIX:
        die(f"record class must be one of {sorted(PREFIX)}")
    if cls == "capability":
        die("a capability is a systems/*.md document first: write the document, then add its record with the id in its frontmatter")
    doc = read_local()
    upstream = main_catalog(fetch)
    if upstream is None:
        print("warning: could not read origin/main's catalog; allocating against this checkout only", file=sys.stderr)
    rec["id"] = f"CERB-{PREFIX[cls]}-{next_free(doc, upstream)}"
    doc["records"].append(rec)
    write(doc)
    print(rec["id"])


def cmd_sort(_):
    doc = read_local()
    write(doc)


def cmd_sync(_):
    doc = read_local()
    for rid in sync(doc):
        print(f"synced {rid}")
    write(doc)


def stage(n):
    text = git("show", f":{n}:{REL}", check=False)
    return load(text) if text else {"records": []}


def in_rebase():
    for name in ("rebase-merge", "rebase-apply"):
        p = git("rev-parse", "--git-path", name).strip()
        if not os.path.isabs(p):
            p = os.path.join(git("rev-parse", "--show-toplevel").strip(), p)
        if os.path.isdir(p):
            return True
    return False


def cmd_resolve(_):
    base = stage(1)
    # In a rebase, "ours" (stage 2) is the upstream being rebased onto and
    # "theirs" (stage 3) is your commit. In a merge it is the other way round.
    if in_rebase():
        other, mine = stage(2), stage(3)
    else:
        mine, other = stage(2), stage(3)
    if not mine["records"] or not other["records"]:
        die("catalog.json is not in conflict (no staged versions); nothing to resolve")

    B = {r["id"]: r for r in base["records"]}
    O = {r["id"]: r for r in other["records"]}
    M = {r["id"]: r for r in mine["records"]}
    result = {k: v for k, v in other.items() if k != "records"}
    out = dict(O)
    conflicts, renames = [], {}

    for rid, rec in M.items():
        if rid in B:
            if rec == B[rid] or rec == O.get(rid):
                continue
            if rid not in O or O[rid] == B[rid]:
                out[rid] = rec
                continue
            if rec.get("class") == "capability" and {k: v for k, v in rec.items() if k != "body"} == {k: v for k, v in O[rid].items() if k != "body"}:
                continue  # body only: rebuilt from systems/*.md by sync
            conflicts.append(rid)
            continue
        # Added on your side.
        if rid in O or number(rid) in numbers_in(other):
            cls = rec.get("class")
            new = f"CERB-{PREFIX.get(cls, 'X')}-{next_free(other, mine, reserved=[number(i) for i in renames.values()])}"
            renames[rid] = new
            rec = dict(rec, id=new)
            out[new] = rec
        else:
            out[rid] = rec
    for rid in B:
        if rid not in M and rid in O and O[rid] == B[rid]:
            out.pop(rid, None)  # you deleted it and they did not touch it

    if renames:
        added = [r for rid, r in out.items() if rid not in O]
        for rec in added:
            text = json.dumps(rec, ensure_ascii=False)
            for old, new in renames.items():
                text = text.replace(old, new)
            out[rec["id"]] = json.loads(text)
        for path in glob.glob(os.path.join(SYSTEMS, "*.md")):
            with open(path, encoding="utf-8") as f:
                text = f.read()
            theirs = git("show", f"{'HEAD' if in_rebase() else 'MERGE_HEAD'}:docs/catalog/systems/{os.path.basename(path)}", check=False) or ""
            new_text = text
            for old, new in renames.items():
                if old in text and old not in theirs:
                    new_text = new_text.replace(old, new)
            if new_text != text:
                with open(path, "w", encoding="utf-8") as f:
                    f.write(new_text)
                print(f"renumbered references in {os.path.relpath(path, HERE)}")

    result["records"] = list(out.values())
    for rid in sync(result):
        print(f"synced {rid}")
    write(result)
    for old, new in renames.items():
        print(f"renumbered {old} -> {new}: fix any commit message or PR description that names {old}")
    if conflicts:
        die("both sides changed these records differently; merge them by hand in catalog.json, then `git add` it: " + ", ".join(sorted(conflicts)))
    git("add", REL)
    print("catalog.json resolved and staged; run python3 docs/catalog/validate.py")


COMMANDS = {"add": cmd_add, "resolve": cmd_resolve, "sort": cmd_sort, "sync": cmd_sync}

if __name__ == "__main__":
    if len(sys.argv) < 2 or sys.argv[1] not in COMMANDS:
        die(__doc__)
    COMMANDS[sys.argv[1]](sys.argv[2:])
