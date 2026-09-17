#!/usr/bin/env python3
"""Validate docs/catalog/catalog.json against the record envelope in README.md,
against the contract the leaderboard-explorer actually imposes, and against the
systems/*.md documents.

    python3 docs/catalog/validate.py

Exits non-zero on any error. Run it in a PR that touches the catalog.

Two things it checks that are easy to get wrong by reading README.md alone:

  * `relationships` must be an array of {type, target} OBJECTS. The explorer's
    normalizeCatalog() silently drops bare id strings, and takes the derived
    `referenced_by` graph down with them.
  * `created_at` and `namespace` are required by the explorer but absent from
    the README's envelope table.

The explorer also coerces `capability`/`tool`/`gap` to `note` (only `decision`
is in its CLASS_KEYS) and drops `locus` entirely, and it never reads `data`.
That is why `locus:*`, `verified:*` and `priority:*` are mirrored into `tags`,
which is the one field it both renders and searches -- this script enforces that
mirroring so the rendered view cannot disagree with the record.
"""
import json, os, re, sys, glob

HERE = os.path.dirname(os.path.abspath(__file__))
CATALOG = os.path.join(HERE, "catalog.json")
SYSTEMS = os.path.join(HERE, "systems")

CLASSES = {"capability", "tool", "gap", "decision"}
STATE_LABELS = {"shipped", "partial", "planned", "deferred", "blocked"}
REVIEW = {"draft", "reviewed"}
LOCUS = {"core", "plugin", "vendor"}
VERIFIED = {"live", "test", "unverified"}
PRIORITY = {"must-have", "nice-to-have", "speculative"}
REQUIRED = ["id","class","name","summary","state_field","state_label","review_status",
            "confidence_score","confidence_label","last_reviewed","created_at",
            "namespace","tags","relationships","body","pointer_locator","data"]
PREFIX = {"capability":"CAP","tool":"TOOL","gap":"GAP","decision":"DEC"}

errors = []
def err(m): errors.append(m)

try:
    doc = json.load(open(CATALOG, encoding="utf-8"))
except Exception as e:
    print(f"catalog.json does not parse: {e}", file=sys.stderr); sys.exit(1)

if not isinstance(doc, dict) or not isinstance(doc.get("records"), list):
    print("catalog.json must be an object with a `records` array "
          "(the explorer's normalizeCatalog throws otherwise)", file=sys.stderr)
    sys.exit(1)

records = doc["records"]
ids = [r.get("id") for r in records]
for rid in {i for i in ids if ids.count(i) > 1}:
    err(f"duplicate id {rid}")
idset = set(ids)

for rec in records:
    rid = rec.get("id", "<no id>")
    for f in REQUIRED:
        if f not in rec: err(f"{rid}: missing required field '{f}'")
    cls = rec.get("class")
    if cls not in CLASSES:
        err(f"{rid}: class '{cls}' not one of {sorted(CLASSES)}")
    elif isinstance(rid, str) and f"-{PREFIX[cls]}-" not in rid:
        err(f"{rid}: id prefix disagrees with class '{cls}'")
    if not re.fullmatch(r"CERB-(CAP|TOOL|GAP|DEC)-\d{3}", str(rid) or ""):
        err(f"{rid}: id must match CERB-(CAP|TOOL|GAP|DEC)-NNN")
    if rec.get("state_label") not in STATE_LABELS:
        err(f"{rid}: state_label '{rec.get('state_label')}' invalid")
    if not rec.get("state_field"): err(f"{rid}: state_field is empty")
    if rec.get("review_status") not in REVIEW:
        err(f"{rid}: review_status must be draft|reviewed")
    cs = rec.get("confidence_score")
    if isinstance(cs, bool) or not isinstance(cs, (int, float)) or not 0 <= cs <= 1:
        err(f"{rid}: confidence_score must be a number in 0..1")
    if not rec.get("confidence_label"):
        err(f"{rid}: a confidence_score needs a confidence_label saying why")
    for df in ("last_reviewed", "created_at"):
        if not re.fullmatch(r"\d{4}-\d{2}-\d{2}", str(rec.get(df, ""))):
            err(f"{rid}: {df} must be an ISO date")
    if rec.get("namespace") != "cerberus":
        err(f"{rid}: namespace must be 'cerberus'")
    for f in ("summary", "body", "pointer_locator"):
        if not str(rec.get(f, "")).strip(): err(f"{rid}: {f} is empty")

    tags = rec.get("tags")
    if not isinstance(tags, list):
        err(f"{rid}: tags must be a list"); tags = []
    else:
        if "cerberus" not in tags: err(f"{rid}: tags missing 'cerberus'")
        if f"class:{cls}" not in tags: err(f"{rid}: tags missing 'class:{cls}'")

    rels = rec.get("relationships")
    if not isinstance(rels, list):
        err(f"{rid}: relationships must be a list")
    else:
        for rel in rels:
            if not isinstance(rel, dict):
                err(f"{rid}: relationship {rel!r} is not an object -- the explorer drops it")
                continue
            t = rel.get("target", "")
            if not t: err(f"{rid}: relationship with no target")
            elif str(t).startswith("TBD:"): err(f"{rid}: unresolved TBD target {t!r}")
            elif t not in idset: err(f"{rid}: relationship target '{t}' is not a record id")
            if not rel.get("type"): err(f"{rid}: relationship to {t} has no type")
    if "referenced_by" in rec:
        err(f"{rid}: referenced_by is derived by the explorer and must not be authored")

    locus = rec.get("locus")
    if cls in ("capability", "tool") and locus not in LOCUS:
        err(f"{rid}: locus '{locus}' not one of {sorted(LOCUS)}")
    if locus and f"locus:{locus}" not in tags:
        err(f"{rid}: tags must mirror locus as 'locus:{locus}' -- the explorer drops the field")

    data = rec.get("data")
    if not isinstance(data, dict):
        err(f"{rid}: data must be an object"); data = {}
    if cls == "capability":
        for k in ("owns","does_not_own","vendor","entry_points","surfaces","key_files","adrs"):
            if k not in data: err(f"{rid}: capability data missing '{k}'")
        if not data.get("does_not_own"):
            err(f"{rid}: does_not_own is empty -- it is not filler")
    elif cls == "tool":
        for k in ("capability","invocation","surfaces","destructive","verified"):
            if k not in data: err(f"{rid}: tool data missing '{k}'")
        v = data.get("verified")
        if v not in VERIFIED: err(f"{rid}: verified must be live|test|unverified")
        elif f"verified:{v}" not in tags:
            err(f"{rid}: tags must mirror verified as 'verified:{v}'")
        if not isinstance(data.get("destructive"), bool):
            err(f"{rid}: destructive must be a boolean")
        elif data["destructive"] and "destructive" not in tags:
            err(f"{rid}: a destructive tool must carry the 'destructive' tag")
        pc = data.get("capability")
        if pc and pc not in idset: err(f"{rid}: data.capability '{pc}' is not a record id")
    elif cls == "gap":
        for k in ("capability","priority","blocked_by","unlocks_when","effort"):
            if k not in data: err(f"{rid}: gap data missing '{k}'")
        p = data.get("priority")
        if p not in PRIORITY: err(f"{rid}: gap priority '{p}' invalid")
        elif f"priority:{p}" not in tags:
            err(f"{rid}: tags must mirror priority as 'priority:{p}'")
        if not isinstance(data.get("blocked_by"), list):
            err(f"{rid}: blocked_by must be a list (empty means only effort is missing)")
    elif cls == "decision":
        for k in ("decision","alternatives_considered","rationale","reversible","superseded_by"):
            if k not in data: err(f"{rid}: decision data missing '{k}'")
        if not str(data.get("rationale","")).strip():
            err(f"{rid}: a decision with no rationale -- the reasoning is the point")

# every capability has a document, every document has a capability, and the
# document prose IS the record body. Linked by the document's frontmatter id,
# because pointer_locator names where the implementation lives.
def split(path):
    t = open(path, encoding="utf-8").read()
    if not t.startswith("---"): return None, t.strip()
    parts = t.split("---", 2)
    if len(parts) < 3: return None, t.strip()
    m = re.search(r'^id:\s*"?([A-Za-z0-9-]+)"?\s*$', parts[1], re.M)
    body = re.sub(r"^#\s+.*\n+", "", parts[2].strip(), count=1).strip()
    return (m.group(1) if m else None), body

def norm(s): return re.sub(r"\s+", " ", s or "").strip()

byid = {r["id"]: r for r in records}
seen = {}
for path in sorted(glob.glob(os.path.join(SYSTEMS, "*.md"))):
    base = os.path.basename(path)
    if base.lower() == "readme.md": continue
    rid, body = split(path)
    if not rid:
        err(f"systems/{base}: no 'id:' in YAML frontmatter"); continue
    if rid in seen:
        err(f"systems/{base}: id {rid} already claimed by systems/{seen[rid]}"); continue
    seen[rid] = base
    rec = byid.get(rid)
    if rec is None:
        err(f"systems/{base}: frontmatter id {rid} matches no record"); continue
    if rec.get("class") != "capability":
        err(f"systems/{base}: {rid} is class '{rec.get('class')}', not capability")
    if norm(body) != norm(rec.get("body")):
        err(f"{rid}: body disagrees with systems/{base} -- they are one thing in two formats")

for rec in records:
    if rec.get("class") == "capability" and rec["id"] not in seen:
        err(f"{rec['id']} ({rec.get('name')}): capability with no systems/*.md")

caps = sum(1 for r in records if r.get("class") == "capability")
counts = {}
for r in records: counts[r.get("class")] = counts.get(r.get("class"), 0) + 1
print(f"{len(records)} records: " + ", ".join(f"{k}={v}" for k, v in sorted(counts.items())))
print(f"{caps} capabilities, {len(seen)} documents")
if errors:
    print(f"\n{len(errors)} ERROR(S):", file=sys.stderr)
    for e in errors: print("  !", e, file=sys.stderr)
    sys.exit(1)
print("OK -- envelope, explorer contract and documents all agree.")
