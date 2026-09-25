#!/usr/bin/env python3
"""Tests for add_record.py, run against real git repositories in a temp dir:

    python3 docs/catalog/test_add_record.py
"""
import json, os, shutil, subprocess, sys, tempfile, unittest

HERE = os.path.dirname(os.path.abspath(__file__))
TOOL = os.path.join(HERE, "add_record.py")


def rec(rid, cls, **extra):
    r = {"id": rid, "class": cls, "name": rid, "body": extra.pop("body", ""), "relationships": extra.pop("relationships", [])}
    r.update(extra)
    return r


class Repo:
    def __init__(self):
        self.root = tempfile.mkdtemp(prefix="catalog-")
        self.cat = os.path.join(self.root, "docs", "catalog")
        os.makedirs(os.path.join(self.cat, "systems"))
        shutil.copy(TOOL, self.cat)
        self.git("init", "-q", "-b", "main")
        self.git("config", "user.email", "t@example.com")
        self.git("config", "user.name", "t")

    def git(self, *args, ok=True):
        out = subprocess.run(["git", *args], cwd=self.root, capture_output=True, text=True)
        if ok and out.returncode != 0:
            raise AssertionError(f"git {args}: {out.stdout}{out.stderr}")
        return out

    def tool(self, *args):
        return subprocess.run([sys.executable, os.path.join(self.cat, "add_record.py"), *args], cwd=self.root, capture_output=True, text=True)

    def write_catalog(self, records):
        with open(os.path.join(self.cat, "catalog.json"), "w", encoding="utf-8") as f:
            f.write(json.dumps({"generated_at": "x", "records": records}, indent=2, ensure_ascii=False) + "\n")
        self.tool("sort")

    def records(self):
        with open(os.path.join(self.cat, "catalog.json"), encoding="utf-8") as f:
            return json.load(f)["records"]

    def write(self, rel, text):
        with open(os.path.join(self.cat, rel), "w", encoding="utf-8") as f:
            f.write(text)

    def read(self, rel):
        with open(os.path.join(self.cat, rel), encoding="utf-8") as f:
            return f.read()

    def commit(self, msg):
        self.git("add", "-A")
        self.git("commit", "-q", "-m", msg)


BASE = [rec("CERB-CAP-100", "capability"), rec("CERB-DEC-200", "decision"), rec("CERB-GAP-300", "gap"), rec("CERB-TOOL-400", "tool")]


class AddRecordTest(unittest.TestCase):
    def setUp(self):
        self.r = Repo()
        self.r.write_catalog(list(BASE))
        self.r.commit("base")

    def tearDown(self):
        shutil.rmtree(self.r.root)

    def test_add_allocates_the_next_global_number_and_sorts(self):
        path = os.path.join(self.r.root, "new.json")
        with open(path, "w") as f:
            json.dump({"class": "gap", "name": "new gap", "body": "", "relationships": []}, f)
        out = self.r.tool("add", path, "--no-fetch")
        self.assertEqual(out.returncode, 0, out.stderr)
        self.assertEqual(out.stdout.strip(), "CERB-GAP-401")
        ids = [x["id"] for x in self.r.records()]
        self.assertEqual(ids, ["CERB-CAP-100", "CERB-DEC-200", "CERB-GAP-300", "CERB-GAP-401", "CERB-TOOL-400"])

    def test_different_classes_merge_without_a_conflict(self):
        self.r.git("checkout", "-q", "-b", "mine")
        self.r.write_catalog(self.r.records() + [rec("CERB-TOOL-401", "tool")])
        self.r.commit("mine: a tool")
        self.r.git("checkout", "-q", "main")
        self.r.write_catalog(self.r.records() + [rec("CERB-GAP-402", "gap")])
        self.r.commit("main: a gap")
        self.r.git("checkout", "-q", "mine")
        self.assertEqual(self.r.git("rebase", "main", ok=False).returncode, 0, "a gap and a tool conflicted")

    def test_resolve_renumbers_a_collision_and_follows_the_references(self):
        self.r.write("systems/web.md", '---\nid: "CERB-CAP-100"\n---\n# Web\n\nBody.\n')
        self.r.write_catalog([dict(r, body="Body.") if r["id"] == "CERB-CAP-100" else r for r in self.r.records()])
        self.r.commit("doc")
        self.r.git("checkout", "-q", "-b", "mine")
        self.r.write("systems/web.md", '---\nid: "CERB-CAP-100"\n---\n# Web\n\nBody.\n\nSee CERB-GAP-401.\n')
        self.r.write_catalog(self.r.records() + [
            rec("CERB-GAP-401", "gap", name="mine"),
            rec("CERB-TOOL-402", "tool", relationships=[{"type": "relates_to", "target": "CERB-GAP-401"}]),
        ])
        self.r.commit("mine: gap 401 and tool 402")
        self.r.git("checkout", "-q", "main")
        self.r.write_catalog(self.r.records() + [rec("CERB-GAP-401", "gap", name="theirs")])
        self.r.commit("main: gap 401")
        self.r.git("checkout", "-q", "mine")
        self.assertNotEqual(self.r.git("rebase", "main", ok=False).returncode, 0, "expected a conflict")

        out = self.r.tool("resolve")
        self.assertEqual(out.returncode, 0, out.stdout + out.stderr)
        self.assertIn("renumbered CERB-GAP-401 -> CERB-GAP-403", out.stdout)
        by_id = {x["id"]: x for x in self.r.records()}
        self.assertEqual(by_id["CERB-GAP-401"]["name"], "theirs")
        self.assertEqual(by_id["CERB-GAP-403"]["name"], "mine")
        self.assertEqual(by_id["CERB-TOOL-402"]["relationships"][0]["target"], "CERB-GAP-403")
        self.assertIn("See CERB-GAP-403.", self.r.read("systems/web.md"))
        self.assertIn("See CERB-GAP-403.", by_id["CERB-CAP-100"]["body"])
        self.r.git("add", "-A")
        env = dict(os.environ, GIT_EDITOR="true")
        done = subprocess.run(["git", "rebase", "--continue"], cwd=self.r.root, capture_output=True, text=True, env=env)
        self.assertEqual(done.returncode, 0, done.stdout + done.stderr)

    def test_resolve_takes_the_side_that_changed_and_refuses_a_real_conflict(self):
        self.r.git("checkout", "-q", "-b", "mine")
        self.r.write_catalog([dict(r, name="edited by me") if r["id"] == "CERB-GAP-300" else r for r in self.r.records()] + [rec("CERB-TOOL-401", "tool")])
        self.r.commit("mine")
        self.r.git("checkout", "-q", "main")
        self.r.write_catalog([dict(r, name="edited by main") if r["id"] == "CERB-DEC-200" else r for r in self.r.records()] + [rec("CERB-TOOL-402", "tool")])
        self.r.commit("main")
        self.r.git("checkout", "-q", "mine")
        self.r.git("rebase", "main", ok=False)
        out = self.r.tool("resolve")
        self.assertEqual(out.returncode, 0, out.stdout + out.stderr)
        by_id = {x["id"]: x for x in self.r.records()}
        self.assertEqual(by_id["CERB-GAP-300"]["name"], "edited by me")
        self.assertEqual(by_id["CERB-DEC-200"]["name"], "edited by main")
        self.assertIn("CERB-TOOL-401", by_id)
        self.assertIn("CERB-TOOL-402", by_id)
        self.r.git("rebase", "--abort", ok=False)

        self.r.git("checkout", "-q", "-b", "clash", "main")
        self.r.git("reset", "-q", "--hard", "HEAD~1")
        self.r.write_catalog([dict(r, name="clash") if r["id"] == "CERB-DEC-200" else r for r in self.r.records()] + [rec("CERB-TOOL-405", "tool")])
        self.r.commit("clash")
        self.r.git("rebase", "main", ok=False)
        out = self.r.tool("resolve")
        self.assertNotEqual(out.returncode, 0)
        self.assertIn("CERB-DEC-200", out.stderr)


if __name__ == "__main__":
    unittest.main()
