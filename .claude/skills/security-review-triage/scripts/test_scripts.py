import contextlib
import importlib.util
import io
import json
import os
import subprocess
import sys
import tempfile
import threading
import unittest
import unittest.mock
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))


def load(name: str):
    spec = importlib.util.spec_from_file_location(name, HERE / f"{name}.py")
    module = importlib.util.module_from_spec(spec)
    sys.modules[name] = module
    spec.loader.exec_module(module)
    return module


yaml_subset = load("yaml_subset")
infra_scan = load("infra_scan")
advisory_lookup = load("advisory_lookup")


class YamlSubsetTest(unittest.TestCase):
    def parse(self, text: str):
        return list(yaml_subset.documents(text))

    def test_sequence_of_mappings_keeps_line_numbers(self):
        document = self.parse("containers:\n  - name: a\n    image: x\n  - name: b\n")[0]
        self.assertEqual([item["name"] for item in document["containers"]], ["a", "b"])
        self.assertEqual(document["containers"][1].line, 4)

    def test_compact_sequence_under_a_key(self):
        self.assertEqual(self.parse("verbs:\n- get\n- list\nnext: 1\n")[0], {"verbs": ["get", "list"], "next": 1})

    def test_flow_collections_and_quotes(self):
        document = self.parse('a: {drop: ["ALL"], n: 2}\nb: [x, \'y z\', "q: r"]\n')[0]
        self.assertEqual(document["a"], {"drop": ["ALL"], "n": 2})
        self.assertEqual(document["b"], ["x", "y z", "q: r"])

    def test_block_scalar_records_its_first_line(self):
        document = self.parse("run: |\n  echo one\n  echo two\nafter: true\n")[0]
        self.assertEqual(document["run"], "echo one\necho two")
        self.assertEqual(document["run"].first_line, 2)
        self.assertIs(document["after"], True)

    def test_comment_marker_inside_quotes_is_text(self):
        self.assertEqual(self.parse('name: "a # b"  # trailing\n')[0], {"name": "a # b"})

    def test_multiple_documents(self):
        self.assertEqual([doc["kind"] for doc in self.parse("kind: A\n---\nkind: B\n...\n")], ["A", "B"])

    def test_go_template_text_inside_block_scalar_parses(self):
        document = self.parse("summary: >-\n  {{ $labels.instance }} down\n")[0]
        self.assertEqual(document["summary"], "{{ $labels.instance }} down")

    def test_template_control_line_is_an_error(self):
        with self.assertRaises(yaml_subset.YamlError):
            self.parse("spec:\n{{- if .Values.enabled }}\n  replicas: 1\n{{- end }}\n")


class InfraScanTest(unittest.TestCase):
    def scan(self, text: str, name: str = "manifest.yaml"):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(text)
            return infra_scan.run([directory])

    def test_rule_numbers_are_unique(self):
        numbers = [rule.number for rule in infra_scan.RULES.values()]
        self.assertEqual(len(numbers), len(set(numbers)))

    def test_stdin_stream_is_judged(self):
        stream = "kind: Pod\nmetadata: {name: p}\nspec:\n  hostPID: true\n  containers: []\n"
        output = io.StringIO()
        with contextlib.redirect_stdout(output), unittest.mock.patch.object(sys, "stdin", io.StringIO(stream)):
            status = infra_scan.main(["--json", "--fail-on", "warn", "-"])
        self.assertEqual(status, 1)
        self.assertIn("host-namespace", {finding["rule"] for finding in json.loads(output.getvalue())["findings"]})

    def test_secret_values_are_never_printed(self):
        scan = self.scan("kind: Secret\nmetadata: {name: s}\nstringData:\n  password: do-not-print-me\n")
        self.assertTrue(scan.findings)
        self.assertNotIn("do-not-print-me", json.dumps([finding.message for finding in scan.findings]))

    def test_secret_with_resource_names_is_scoped(self):
        scan = self.scan("kind: Role\nmetadata: {name: r}\nrules:\n  - resources: [secrets]\n    resourceNames: [one]\n    verbs: [get]\n")
        self.assertEqual(scan.findings, [])

    def test_sha_pinned_and_local_actions_pass(self):
        workflow = ("on: push\npermissions: {contents: read}\njobs:\n  a:\n    runs-on: x\n    steps:\n"
                    "      - uses: actions/checkout@9c091bb21b7c1c1d1991bb908d89e4e9dddfe3e0\n      - uses: ./local\n")
        self.assertEqual(self.scan(workflow, ".forgejo/workflows/ci.yml").findings, [])

    def test_untrusted_text_through_env_passes(self):
        workflow = ("on: issues\npermissions: {}\njobs:\n  a:\n    runs-on: x\n    steps:\n      - env:\n"
                    "          TITLE: ${{ github.event.issue.title }}\n        run: echo \"$TITLE\"\n")
        self.assertEqual(self.scan(workflow, ".github/workflows/ci.yml").findings, [])

    def test_untrusted_text_in_run_fires_on_its_line(self):
        workflow = ("on: issues\npermissions: {}\njobs:\n  a:\n    runs-on: x\n    steps:\n      - run: |\n"
                    "          true\n          echo \"${{ github.event.issue.body }}\"\n")
        findings = self.scan(workflow, ".github/workflows/ci.yml").findings
        self.assertEqual([(finding.rule, finding.line) for finding in findings], [("ci-untrusted-expression-in-run", 9)])

    def test_audit_policy_not_named_as_enforcement_passes(self):
        self.assertEqual(self.scan("kind: ClusterPolicy\nmetadata: {name: report-labels}\nspec: {validationFailureAction: Audit}\n").findings, [])

    def test_build_argument_with_digest_default_passes(self):
        dockerfile = ("ARG BASE=registry.example.com/node:24@sha256:" + "a" * 64 + "\nFROM ${BASE}\nUSER 1000\n")
        self.assertEqual(self.scan(dockerfile, "Dockerfile").findings, [])


class Handler(BaseHTTPRequestHandler):
    routes: dict = {}

    def log_message(self, *arguments):
        pass

    def respond(self, key: str):
        status, payload, headers = self.routes.get(key, (404, {"message": "no route"}, {}))
        body = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        for name, value in headers.items():
            self.send_header(name, value)
        self.end_headers()
        self.wfile.write(body)

    def do_POST(self):
        length = int(self.headers.get("Content-Length", 0))
        request = json.loads(self.rfile.read(length) or b"{}")
        self.respond("osv-page2" if request.get("page_token") else "osv")

    def do_GET(self):
        self.respond("ghsa-page2" if "page=2" in self.path else "ghsa")


class AdvisoryLookupTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        threading.Thread(target=cls.server.serve_forever, daemon=True).start()
        base = f"http://127.0.0.1:{cls.server.server_port}"
        cls.environment = unittest.mock.patch.dict(os.environ, {"OSV_API_URL": base, "GITHUB_API_URL": base}, clear=False)
        cls.environment.start()
        os.environ.pop("GITHUB_TOKEN", None)
        cls.base = base

    @classmethod
    def tearDownClass(cls):
        cls.environment.stop()
        cls.server.shutdown()

    def lookup(self, sources=("osv", "ghsa"), offline=False):
        return advisory_lookup.lookup_package("npm", "left-pad", "1.0.0", list(sources), offline, 5)

    def test_sources_merge_by_alias_and_follow_pages(self):
        Handler.routes = {
            "osv": (200, {"vulns": [{"id": "GHSA-aaaa-bbbb-cccc", "aliases": ["CVE-2026-0001"], "summary": "first",
                                     "affected": [{"package": {"name": "left-pad"}, "ranges": [{"type": "SEMVER", "events": [{"introduced": "0"}, {"fixed": "1.0.1"}]}]}]}],
                          "next_page_token": "t"}, {}),
            "osv-page2": (200, {"vulns": [{"id": "OSV-2026-9", "summary": "only in osv"}]}, {}),
            "ghsa": (200, [{"ghsa_id": "GHSA-aaaa-bbbb-cccc", "cve_id": "CVE-2026-0001", "summary": "first", "severity": "high",
                            "vulnerabilities": [{"package": {"name": "left-pad"}, "first_patched_version": "1.0.1"}]}],
                     {"Link": f'<{self.base}/advisories?page=2>; rel="next"'}),
            "ghsa-page2": (200, [{"ghsa_id": "GHSA-dddd-eeee-ffff", "summary": "withdrawn one", "withdrawn_at": "2026-01-01T00:00:00Z", "vulnerabilities": []}], {}),
        }
        report = self.lookup()
        self.assertEqual(report["status"], "found")
        by_id = {advisory["ids"][0]: advisory for advisory in report["advisories"]}
        self.assertEqual(by_id["GHSA-aaaa-bbbb-cccc"]["sources"], ["OSV", "GitHub Advisory Database"])
        self.assertEqual(by_id["GHSA-aaaa-bbbb-cccc"]["fixed"], ["1.0.1"])
        self.assertIn("OSV-2026-9", by_id)
        self.assertTrue(by_id["GHSA-dddd-eeee-ffff"]["withdrawn"])
        self.assertTrue(report["verdict"].startswith("2 advisories"))

    def test_a_failed_source_makes_an_empty_answer_inconclusive(self):
        Handler.routes = {"osv": (200, {}, {}), "ghsa": (403, {"message": "rate limit"}, {})}
        report = self.lookup()
        self.assertEqual(report["status"], "inconclusive")
        self.assertIn("rate limited", report["sources"][1]["detail"])
        self.assertIn("did not answer", report["verdict"])

    def test_empty_answers_are_bounded_by_the_sources(self):
        Handler.routes = {"osv": (200, {}, {}), "ghsa": (200, [], {})}
        report = self.lookup()
        self.assertEqual(report["status"], "clean")
        self.assertIn("not 'not affected'", report["verdict"])
        self.assertIn("NVD", report["not_queried"])

    def test_offline_queries_nothing(self):
        Handler.routes = {}
        report = self.lookup(offline=True)
        self.assertEqual({source["status"] for source in report["sources"]}, {"skipped"})
        self.assertEqual(report["status"], "inconclusive")

    def test_unknown_ecosystem_is_a_usage_error(self):
        with contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(advisory_lookup.main(["package", "nope", "x", "1"]), advisory_lookup.EXIT_USAGE)

    def test_sources_file_is_dated(self):
        self.assertRegex(json.loads(advisory_lookup.SOURCES_FILE.read_text())["checked"], r"^\d{4}-\d{2}-\d{2}$")


class GitLookupTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.directory = tempfile.TemporaryDirectory()
        cls.repo = os.path.join(cls.directory.name, "upstream")
        environment = {**os.environ, "GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.invalid",
                       "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@example.invalid"}

        def git(*arguments, cwd=cls.repo):
            return subprocess.run(["git", *arguments], cwd=cwd, env=environment, check=True, capture_output=True, text=True).stdout.strip()

        os.makedirs(cls.repo)
        git("init", "-q", "-b", "main")
        git("-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "base")
        git("tag", "v1.0.0")
        git("-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "fix")
        cls.fix = git("rev-parse", "HEAD")
        git("tag", "v1.0.1")
        git("-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "later")
        cls.shallow = os.path.join(cls.directory.name, "shallow")
        git("clone", "-q", "--depth", "1", f"file://{cls.repo}", cls.shallow, cwd=cls.directory.name)

    @classmethod
    def tearDownClass(cls):
        cls.directory.cleanup()

    def test_tags_containing_the_fix(self):
        report = advisory_lookup.lookup_git(self.repo, self.fix, None)
        self.assertEqual(report["status"], "clean")
        self.assertIn("v1.0.1", report["verdict"])

    def test_shipped_ref_without_the_fix_is_found(self):
        report = advisory_lookup.lookup_git(self.repo, self.fix, "v1.0.0")
        self.assertEqual(report["status"], "found")

    def test_shipped_ref_with_the_fix_is_clean(self):
        self.assertEqual(advisory_lookup.lookup_git(self.repo, self.fix, "v1.0.1")["status"], "clean")

    def test_shallow_clone_missing_the_commit_is_inconclusive(self):
        report = advisory_lookup.lookup_git(self.shallow, self.fix, "HEAD")
        self.assertEqual(report["status"], "inconclusive")
        self.assertIn("shallow", report["verdict"])


if __name__ == "__main__":
    unittest.main()
