#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"
SCAN="python3 scripts/infra_scan.py"

$SCAN --fail-on note fixtures/careful > /dev/null || { echo "careful fixture raised findings" >&2; $SCAN --fail-on never fixtures/careful; exit 1; }

if $SCAN fixtures/careless > /dev/null 2>&1; then
  echo "careless fixture passed; it must fail" >&2; exit 1
fi

$SCAN --json --fail-on never fixtures/careful fixtures/careless | python3 -c '
import json, sys
report = json.load(sys.stdin)
unparsed = [entry for entry in report["coverage"]["skipped"] if "not parsed" in entry["reason"] or "template" in entry["reason"]]
if unparsed:
    sys.exit(f"fixtures not parsed: {unparsed}")
careful = [finding for finding in report["findings"] if "/careful/" in finding["path"]]
if careful:
    sys.exit(f"careful fixture fired: {careful}")
found = {finding["rule"] for finding in report["findings"]}
expected = {line.strip() for line in open("fixtures/careless.expect") if line.strip()}
if found != expected:
    sys.exit(f"careless fixture: missing {sorted(expected - found)}, unexpected {sorted(found - expected)}")
if not report["cannot_see"]:
    sys.exit("report has no cannot-see section")
print(f"infra_scan: {len(expected)} rules fire on the careless fixture, none on the careful one")
'

if ! unit_output=$(python3 -m unittest -q scripts/test_scripts.py 2>&1); then
  echo "$unit_output" >&2; exit 1
fi
echo "$unit_output" | grep -E "^(Ran|OK)" | tr '\n' ' '; echo

$SCAN --rules --json | python3 -c '
import json, re, sys
rules = json.load(sys.stdin)
documented = open("checks.md").read()
expected = {line.strip() for line in open("fixtures/careless.expect") if line.strip()}
undocumented = []
for rule in rules:
    name, number = rule["name"], rule["number"]
    if f"`{name}`" not in documented or f"| {number} |" not in documented:
        undocumented.append(name)
untested = sorted({rule["name"] for rule in rules} - expected)
sources = json.load(open("scripts/advisory-sources.json"))
if not re.fullmatch(r"\d{4}-\d{2}-\d{2}", sources.get("checked", "")):
    sys.exit("scripts/advisory-sources.json needs a checked date")
if undocumented or untested:
    sys.exit(f"infra_scan rules undocumented {undocumented}, untested {untested}")
print(f"infra_scan: {len(rules)} rules documented in checks.md and covered by the careless fixture; advisory sources dated")
'
