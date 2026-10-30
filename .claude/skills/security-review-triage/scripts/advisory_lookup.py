#!/usr/bin/env python3
"""Look up published advisories for a package version, and check whether a shipped git ref contains a fix commit.

Usage:
    advisory_lookup.py package ECOSYSTEM NAME VERSION [--source osv|ghsa ...] [--offline] [--timeout S] [--json]
    advisory_lookup.py git --repo PATH --fix-commit SHA [--shipped REF] [--json]

package queries OSV and the GitHub Advisory Database over HTTPS (this is the
only network access, and only when run) and prints a coverage table: which
source answered, which failed and which were never queried. "No advisories"
is always bounded by the sources that answered. ECOSYSTEM is one of the keys
in advisory-sources.json (npm, pypi, go, maven, cargo, ...). Set GITHUB_TOKEN
to lift the GitHub rate limit; OSV_API_URL and GITHUB_API_URL override the
endpoints. --offline queries nothing and reports every source as skipped.

git reads a local clone only: is it shallow, does it hold the fix commit,
which tags contain it, and does --shipped (the deployed tag or commit)
contain it.

Exit status: 0 nothing found and every selected source answered; 1 at least
one advisory, or the shipped ref lacks the fix; 3 inconclusive (a source
failed, offline, shallow clone, missing commit); 2 usage error.
"""

import argparse
import json
import os
import subprocess
import sys
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import asdict, dataclass, field
from pathlib import Path

SOURCES_FILE = Path(__file__).resolve().parent / "advisory-sources.json"
USER_AGENT = "security-review-triage-advisory-lookup"
OSV_PAGE_LIMIT = 10
GHSA_PAGE_LIMIT = 5
GHSA_PAGE_SIZE = 100
TAG_PREVIEW = 8
EXIT_CLEAN = 0
EXIT_FOUND = 1
EXIT_USAGE = 2
EXIT_INCONCLUSIVE = 3


@dataclass
class SourceResult:
    key: str
    name: str
    status: str
    detail: str = ""
    count: int = 0


@dataclass
class Advisory:
    ids: list
    summary: str
    severity: str
    fixed: list
    sources: list = field(default_factory=list)
    url: str = ""
    withdrawn: bool = False


def load_sources() -> dict:
    return json.loads(SOURCES_FILE.read_text())


def request_json(url: str, timeout: float, body: dict | None = None, headers: dict | None = None):
    data = json.dumps(body).encode() if body is not None else None
    request = urllib.request.Request(url, data=data, method="POST" if body is not None else "GET")
    request.add_header("User-Agent", USER_AGENT)
    request.add_header("Accept", "application/json")
    if body is not None:
        request.add_header("Content-Type", "application/json")
    for name, value in (headers or {}).items():
        request.add_header(name, value)
    with urllib.request.urlopen(request, timeout=timeout) as response:
        return json.loads(response.read().decode("utf-8")), response.headers.get("Link", "")


def failure_detail(error: Exception) -> str:
    if isinstance(error, urllib.error.HTTPError):
        error.close()
        hint = " (rate limited: set GITHUB_TOKEN)" if error.code in (403, 429) else ""
        return f"HTTP {error.code}{hint}"
    if isinstance(error, urllib.error.URLError):
        return f"unreachable: {error.reason}"
    if isinstance(error, TimeoutError):
        return "timed out"
    if isinstance(error, json.JSONDecodeError):
        return "answered with something that is not JSON"
    return f"{type(error).__name__}: {error}"


def osv_fixed_versions(vulnerability: dict, name: str) -> list:
    fixed = []
    for affected in vulnerability.get("affected", []):
        package = affected.get("package", {})
        if package.get("name") and package.get("name") != name:
            continue
        for affected_range in affected.get("ranges", []):
            if affected_range.get("type") == "GIT":
                continue
            fixed += [event["fixed"] for event in affected_range.get("events", []) if "fixed" in event]
    return sorted(set(fixed))


def osv_severity(vulnerability: dict) -> str:
    database = vulnerability.get("database_specific") or {}
    if isinstance(database.get("severity"), str):
        return database["severity"].lower()
    vectors = [entry.get("score", "") for entry in vulnerability.get("severity", [])]
    return vectors[0] if vectors else ""


def query_osv(ecosystem: str, name: str, version: str, timeout: float, config: dict) -> tuple[list, str]:
    base = os.environ.get(config["override_env"], "")
    endpoint = base.rstrip("/") + "/v1/query" if base else config["endpoint"]
    advisories = []
    body = {"package": {"name": name, "ecosystem": ecosystem}, "version": version}
    for _ in range(OSV_PAGE_LIMIT):
        payload, _ = request_json(endpoint, timeout, body=body)
        for vulnerability in payload.get("vulns", []):
            advisories.append(Advisory(
                ids=[vulnerability["id"], *vulnerability.get("aliases", [])],
                summary=vulnerability.get("summary") or (vulnerability.get("details") or "")[:120],
                severity=osv_severity(vulnerability),
                fixed=osv_fixed_versions(vulnerability, name),
                url=f"https://osv.dev/vulnerability/{vulnerability['id']}",
                withdrawn=bool(vulnerability.get("withdrawn")),
            ))
        token = payload.get("next_page_token")
        if not token:
            return advisories, ""
        body = {**body, "page_token": token}
    return advisories, f"stopped after {OSV_PAGE_LIMIT} pages"


def next_link(link_header: str) -> str:
    for part in link_header.split(","):
        if 'rel="next"' in part:
            return part.split(";")[0].strip().strip("<>")
    return ""


def query_ghsa(ecosystem: str, name: str, version: str, timeout: float, config: dict) -> tuple[list, str]:
    base = os.environ.get(config["override_env"], "")
    endpoint = base.rstrip("/") + "/advisories" if base else config["endpoint"]
    query = urllib.parse.urlencode({"ecosystem": ecosystem, "affects": f"{name}@{version}", "per_page": GHSA_PAGE_SIZE})
    url = f"{endpoint}?{query}"
    headers = {"X-GitHub-Api-Version": "2022-11-28"}
    if os.environ.get("GITHUB_TOKEN"):
        headers["Authorization"] = f"Bearer {os.environ['GITHUB_TOKEN']}"
    advisories = []
    for _ in range(GHSA_PAGE_LIMIT):
        payload, link = request_json(url, timeout, headers=headers)
        for item in payload:
            fixed = sorted({
                vulnerability.get("first_patched_version") if isinstance(vulnerability.get("first_patched_version"), str)
                else (vulnerability.get("first_patched_version") or {}).get("identifier", "")
                for vulnerability in item.get("vulnerabilities", [])
                if (vulnerability.get("package") or {}).get("name") == name
            } - {"", None})
            advisories.append(Advisory(
                ids=[identifier for identifier in (item.get("ghsa_id"), item.get("cve_id")) if identifier],
                summary=item.get("summary", ""),
                severity=item.get("severity", ""),
                fixed=fixed,
                url=item.get("html_url", ""),
                withdrawn=bool(item.get("withdrawn_at")),
            ))
        url = next_link(link)
        if not url:
            return advisories, ""
    return advisories, f"stopped after {GHSA_PAGE_LIMIT} pages"


QUERIES = {"osv": query_osv, "ghsa": query_ghsa}


def merge(found: dict[str, list]) -> list:
    merged: list[Advisory] = []
    for source, advisories in found.items():
        for advisory in advisories:
            match = next((existing for existing in merged if set(existing.ids) & set(advisory.ids)), None)
            if match is None:
                advisory.sources = [source]
                merged.append(advisory)
                continue
            match.ids = list(dict.fromkeys([*match.ids, *advisory.ids]))
            match.sources = list(dict.fromkeys([*match.sources, source]))
            match.fixed = sorted(set(match.fixed) | set(advisory.fixed))
            match.severity = match.severity or advisory.severity
            match.summary = match.summary or advisory.summary
            match.withdrawn = match.withdrawn and advisory.withdrawn
    return merged


def lookup_package(ecosystem: str, name: str, version: str, selected: list, offline: bool, timeout: float) -> dict:
    data = load_sources()
    mapping = data["ecosystems"].get(ecosystem.lower())
    if mapping is None:
        raise ValueError(f"unknown ecosystem {ecosystem}; known: {', '.join(sorted(data['ecosystems']))}")
    results: list[SourceResult] = []
    found: dict[str, list] = {}
    for key in selected:
        config = data["sources"][key]
        if offline:
            results.append(SourceResult(key, config["name"], "skipped", "offline"))
            continue
        try:
            advisories, note = QUERIES[key](mapping[key], name, version, timeout, config)
        except Exception as error:
            results.append(SourceResult(key, config["name"], "failed", failure_detail(error)))
            continue
        found[config["name"]] = advisories
        results.append(SourceResult(key, config["name"], "answered", note, len(advisories)))
    unselected = [data["sources"][key]["name"] for key in data["sources"] if key not in selected]
    advisories = merge(found)
    active = [advisory for advisory in advisories if not advisory.withdrawn]
    answered = [result.name for result in results if result.status == "answered"]
    not_answered = [result.name for result in results if result.status != "answered"]
    if active:
        verdict = (f"{len(active)} advisories list {name} {version} as affected ({', '.join(answered)} answered). "
                   "Listed is not affected: confirm the shipped artifact and that the vulnerable code is reachable.")
        status = "found"
    elif not answered:
        verdict = "Inconclusive: no source answered, so nothing can be said about this version."
        status = "inconclusive"
    elif not_answered:
        verdict = (f"No advisory found in {', '.join(answered)}; {', '.join(not_answered)} did not answer. "
                   "Inconclusive until it does.")
        status = "inconclusive"
    else:
        verdict = (f"No advisory found for {name} {version} in {', '.join(answered)}. "
                   "That is not 'not affected': it says nothing about the sources below.")
        status = "clean"
    return {
        "package": {"ecosystem": ecosystem, "name": name, "version": version},
        "checked": data["checked"],
        "sources": [asdict(result) for result in results],
        "not_queried": unselected + data["not_queried"],
        "advisories": [asdict(advisory) for advisory in advisories],
        "verdict": verdict,
        "status": status,
    }


def git(repo: str, *arguments: str) -> subprocess.CompletedProcess:
    return subprocess.run(["git", "-C", repo, *arguments], capture_output=True, text=True, timeout=120)


def lookup_git(repo: str, fix_commit: str, shipped: str | None) -> dict:
    checks = []
    if git(repo, "rev-parse", "--git-dir").returncode != 0:
        return {"checks": [{"check": "repository", "result": f"{repo} is not a git repository"}], "status": "inconclusive",
                "verdict": "Inconclusive: no repository to read."}
    shallow = git(repo, "rev-parse", "--is-shallow-repository").stdout.strip() == "true"
    checks.append({"check": "clone depth", "result": "shallow" if shallow else "full history"})
    present = git(repo, "cat-file", "-e", f"{fix_commit}^{{commit}}").returncode == 0
    checks.append({"check": "fix commit", "result": "present" if present else "not in this clone"})
    if not present:
        hint = " The clone is shallow: fetch with --unshallow --tags." if shallow else " Fetch the upstream branch and tags."
        return {"checks": checks, "status": "inconclusive", "verdict": f"Inconclusive: {fix_commit} is not in this clone.{hint}"}
    tags = [tag for tag in git(repo, "tag", "--contains", fix_commit, "--sort=v:refname").stdout.splitlines() if tag]
    preview = ", ".join(tags[:TAG_PREVIEW]) + (f" and {len(tags) - TAG_PREVIEW} more" if len(tags) > TAG_PREVIEW else "")
    checks.append({"check": "tags containing the fix", "result": preview or "none yet"})
    if shipped is None:
        status = "inconclusive" if shallow else "clean"
        verdict = f"First tag with the fix: {tags[0]}." if tags else "No tag contains the fix yet; any released version predates it."
        if shallow:
            verdict += " The clone is shallow, so the tag list may be incomplete."
        return {"checks": checks, "status": status, "verdict": verdict}
    if git(repo, "rev-parse", "--verify", f"{shipped}^{{commit}}").returncode != 0:
        checks.append({"check": "shipped ref", "result": f"{shipped} not found"})
        return {"checks": checks, "status": "inconclusive", "verdict": f"Inconclusive: {shipped} is not in this clone."}
    contained = git(repo, "merge-base", "--is-ancestor", fix_commit, shipped).returncode == 0
    checks.append({"check": f"{shipped} contains the fix", "result": "yes" if contained else "no"})
    if contained:
        return {"checks": checks, "status": "clean", "verdict": f"{shipped} includes the fix commit."}
    if shallow:
        return {"checks": checks, "status": "inconclusive", "verdict": f"{shipped} does not show the fix, but the clone is shallow: unshallow before concluding."}
    return {"checks": checks, "status": "found",
            "verdict": f"{shipped} does not include the fix commit: affected when the vulnerable code is present and reachable in that build."}


def print_package(report: dict):
    package = report["package"]
    print(f"Advisory lookup for {package['ecosystem']} {package['name']} {package['version']} (sources file checked {report['checked']})\n")
    print("| Source | Status | Advisories | Detail |")
    print("| --- | --- | --- | --- |")
    for source in report["sources"]:
        count = str(source["count"]) if source["status"] == "answered" else "-"
        print(f"| {source['name']} | {source['status']} | {count} | {source['detail']} |")
    print(f"\nNot queried: {', '.join(report['not_queried'])}.")
    if report["advisories"]:
        print("\n| ID | Aliases | Severity | Fixed in | Listed by | Summary |")
        print("| --- | --- | --- | --- | --- | --- |")
        for advisory in report["advisories"]:
            summary = advisory["summary"].replace("|", "/").replace("\n", " ")[:100]
            severity = "withdrawn" if advisory["withdrawn"] else advisory["severity"] or "-"
            print(f"| {advisory['ids'][0]} | {', '.join(advisory['ids'][1:]) or '-'} | {severity} | "
                  f"{', '.join(advisory['fixed']) or 'no fix listed'} | {', '.join(advisory['sources'])} | {summary} |")
    print(f"\nVerdict: {report['verdict']}")


def print_git(report: dict):
    print("| Check | Result |")
    print("| --- | --- |")
    for check in report["checks"]:
        print(f"| {check['check']} | {check['result']} |")
    print(f"\nVerdict: {report['verdict']}")


STATUS_EXIT = {"clean": EXIT_CLEAN, "found": EXIT_FOUND, "inconclusive": EXIT_INCONCLUSIVE}


def main(argv=None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    commands = parser.add_subparsers(dest="command", required=True)
    package = commands.add_parser("package", help="query advisory databases for one package version")
    package.add_argument("ecosystem")
    package.add_argument("name")
    package.add_argument("version")
    package.add_argument("--source", action="append", choices=sorted(QUERIES), help="repeat to pick sources; default all")
    package.add_argument("--offline", action="store_true")
    package.add_argument("--timeout", type=float, default=15.0)
    package.add_argument("--json", action="store_true")
    repository = commands.add_parser("git", help="check a local clone for a fix commit")
    repository.add_argument("--repo", required=True)
    repository.add_argument("--fix-commit", required=True)
    repository.add_argument("--shipped")
    repository.add_argument("--json", action="store_true")
    options = parser.parse_args(argv)
    if options.command == "package":
        try:
            report = lookup_package(options.ecosystem, options.name, options.version, options.source or list(QUERIES), options.offline, options.timeout)
        except ValueError as error:
            print(error, file=sys.stderr)
            return EXIT_USAGE
        if options.json:
            print(json.dumps(report, indent=2))
        else:
            print_package(report)
    else:
        report = lookup_git(options.repo, options.fix_commit, options.shipped)
        if options.json:
            print(json.dumps(report, indent=2))
        else:
            print_git(report)
    return STATUS_EXIT[report["status"]]


if __name__ == "__main__":
    sys.exit(main())
