#!/usr/bin/env python3
"""Scan infrastructure config for security exposure: Kubernetes manifests, Helm values, Kustomize output, runtime config, Dockerfiles and CI workflows.

Usage:
    infra_scan.py [--json] [--fail-on fail|warn|note|never] PATH [PATH ...]
    kubectl kustomize DIR | infra_scan.py -
    infra_scan.py --rules [--json]

PATH is a file or a directory, walked for YAML, TOML and Dockerfiles (.git,
node_modules, vendor and build output skipped). Each finding carries a
numbered rule, a default severity, file:line and a message; the triage
severity (P0-P3) is exposure x blast radius and is decided by whoever reads
the finding, not by the scanner. Files with Go-template lines are skipped as
unrendered; render them first (helm template, kubectl kustomize). Secret
values are never printed. Every report ends with what a file read cannot
see. Exit status 1 when a finding at or above --fail-on exists (default:
fail), 2 on a usage error. The scan reads files only; it never contacts a
cluster, a registry or a forge.
"""

import argparse
import json
import re
import subprocess
import sys
from dataclasses import asdict, dataclass
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from yaml_subset import Mapping, Sequence, Text, YamlError, documents

SEVERITY_RANK = {"fail": 3, "warn": 2, "note": 1, "never": 99}
MAX_FILE_BYTES = 2 * 1024 * 1024
SKIPPED_DIRECTORIES = {".git", "node_modules", "vendor", "dist", "build", ".terraform", "__pycache__", ".venv"}
YAML_SUFFIXES = {".yaml", ".yml"}
TEXT_CONFIG_SUFFIXES = {".toml", ".conf"}
DOCKERFILE_NAME = re.compile(r"^(?:Dockerfile|Containerfile)(?:\..+)?$|\.(?:Dockerfile|Containerfile)$", re.I)
WORKFLOW_DIRECTORIES = (".github/workflows", ".forgejo/workflows", ".gitea/workflows")
GO_TEMPLATE_LINE = re.compile(r"^\s*(?:-\s*)?\{\{")
POLICY_TEST_MARKERS = ("kyverno-test.yaml", "kyverno-test.yml", "chainsaw-test.yaml", "chainsaw-test.yml", ".chainsaw-test.yaml")
KUSTOMIZATION_NAMES = ("kustomization.yaml", "kustomization.yml", "Kustomization")


@dataclass(frozen=True)
class Rule:
    number: str
    name: str
    severity: str
    area: str
    summary: str


RULES = {rule.name: rule for rule in (
    Rule("K01", "privileged-container", "fail", "pod", "a container or values block requests privileged: true"),
    Rule("K02", "host-namespace", "warn", "pod", "hostNetwork, hostPID or hostIPC is true"),
    Rule("K03", "hostpath-volume", "warn", "pod", "a hostPath volume mounts a node path"),
    Rule("K04", "privilege-escalation-allowed", "warn", "pod", "allowPrivilegeEscalation is explicitly true"),
    Rule("K05", "security-context-incomplete", "note", "pod", "a container lacks non-root, no-escalation, drop ALL, read-only root or a seccomp profile"),
    Rule("K06", "dangerous-capability-added", "warn", "pod", "capabilities.add grants ALL, SYS_ADMIN or another host-level capability"),
    Rule("K07", "default-token-automounted", "note", "pod", "a pod on the default service account mounts its API token"),
    Rule("K08", "image-not-digest-pinned", "warn", "image", "an image reference has no @sha256 digest"),
    Rule("K09", "sandbox-override-annotation", "warn", "runtime", "a pod template sets io.katacontainers.* or dev.gvisor.* annotations"),
    Rule("K10", "sandbox-annotation-passthrough", "warn", "runtime", "runtime config forwards pod annotations or allows flag overrides into the sandbox"),
    Rule("R01", "rbac-wildcard", "warn", "rbac", "a Role or ClusterRole grants * verbs or resources"),
    Rule("R02", "rbac-secrets-read", "warn", "rbac", "get, list or watch on all Secrets in scope"),
    Rule("R03", "rbac-dangerous-verb", "warn", "rbac", "escalate, bind, impersonate, pods/exec, pods/attach, nodes/proxy or serviceaccounts/token"),
    Rule("R04", "rbac-broad-binding", "warn", "rbac", "a binding to cluster-admin or to an anonymous or all-authenticated group"),
    Rule("N01", "namespace-without-networkpolicy", "note", "network", "a namespace with workloads has no NetworkPolicy in the scanned set"),
    Rule("A01", "enforcement-policy-in-audit", "warn", "admission", "a policy named as enforcement only audits"),
    Rule("A02", "admission-fail-open", "note", "admission", "an admission webhook or policy fails open"),
    Rule("S01", "plaintext-secret", "warn", "secret", "a Secret carries values in a file that is not SOPS-encrypted"),
    Rule("D01", "dockerfile-base-not-digest-pinned", "warn", "image", "FROM without an @sha256 digest"),
    Rule("D02", "dockerfile-runs-as-root", "warn", "image", "the final stage has no non-root USER"),
    Rule("D03", "dockerfile-remote-add", "warn", "image", "ADD fetches a URL without --checksum"),
    Rule("D04", "dockerfile-secret-in-build-arg", "warn", "secret", "ARG or ENV with a secret-like name, kept in image history"),
    Rule("D05", "dockerfile-pipe-to-shell", "warn", "image", "RUN pipes a download straight into a shell"),
    Rule("C01", "ci-untrusted-checkout", "fail", "ci", "pull_request_target or workflow_run checks out the pull request head"),
    Rule("C02", "ci-permissions-missing", "warn", "ci", "no permissions block at workflow or job level"),
    Rule("C03", "ci-permissions-write-all", "warn", "ci", "permissions: write-all"),
    Rule("C04", "ci-action-not-sha-pinned", "warn", "ci", "uses: an action or reusable workflow by tag or branch"),
    Rule("C05", "ci-secret-echoed", "fail", "ci", "a run script prints a secret expression"),
    Rule("C06", "ci-untrusted-expression-in-run", "fail", "ci", "a run script interpolates attacker-controlled event text"),
)}

CANNOT_SEE = {
    "always": "Drift between these files and the live objects: confirm against the cluster or forge read-only before calling anything closed.",
    "pod": "Mutations at admission, Pod Security labels on the live namespace, chart defaults absent from the scanned values, and the effective securityContext of the running pod.",
    "image": "What a tag resolves to today, and whether signatures or digests are verified at admission.",
    "runtime": "The runtime config actually installed on each node, its version, and whether the runtime's own annotation allowlist filters what containerd forwards.",
    "rbac": "Aggregated ClusterRoles, bindings created outside git, and the effective permission set: check with kubectl auth can-i --list --as.",
    "network": "Policies created by operators or CNI defaults, cluster-wide policies outside the scanned paths, and whether the CNI enforces them: prove it with a probe.",
    "admission": "The live policy object and its reports, whether the engine's webhook is up, and namespace exclusions in the engine's own config.",
    "secret": "Whether a value is real or a placeholder, and whether git history still holds an older one.",
    "ci": "Runner isolation and labels, which secrets the repository and organisation expose, branch protection, and how this forge version applies permissions.",
}

POD_SPEC_PATHS = {
    "Pod": ("spec",),
    "Deployment": ("spec", "template", "spec"),
    "StatefulSet": ("spec", "template", "spec"),
    "DaemonSet": ("spec", "template", "spec"),
    "ReplicaSet": ("spec", "template", "spec"),
    "ReplicationController": ("spec", "template", "spec"),
    "Job": ("spec", "template", "spec"),
    "CronJob": ("spec", "jobTemplate", "spec", "template", "spec"),
    "ScaledJob": ("spec", "jobTargetRef", "template", "spec"),
}
WORKLOAD_BEARING_KINDS = set(POD_SPEC_PATHS) | {"HelmRelease"}
POLICY_KINDS = {"ClusterPolicy", "Policy", "ValidatingPolicy", "ImageValidatingPolicy", "MutatingPolicy", "PolicyException",
                "ValidatingAdmissionPolicy", "ValidatingAdmissionPolicyBinding", "CiliumNetworkPolicy", "CiliumClusterwideNetworkPolicy",
                "NetworkPolicy", "Constraint", "ConstraintTemplate", "CustomResourceDefinition"}
NETWORK_POLICY_KINDS = {"NetworkPolicy", "CiliumNetworkPolicy"}
CLUSTER_NETWORK_POLICY_KINDS = {"CiliumClusterwideNetworkPolicy", "GlobalNetworkPolicy", "AdminNetworkPolicy", "BaselineAdminNetworkPolicy", "ClusterNetworkPolicy"}
DANGEROUS_CAPABILITIES = {"ALL", "SYS_ADMIN", "SYS_PTRACE", "SYS_MODULE", "SYS_RAWIO", "DAC_READ_SEARCH", "NET_ADMIN", "BPF", "PERFMON"}
SANDBOX_ANNOTATION_PREFIXES = ("io.katacontainers.", "dev.gvisor.")
READ_VERBS = {"get", "list", "watch", "*"}
ESCALATION_VERBS = {"escalate", "bind", "impersonate"}
SENSITIVE_SUBRESOURCES = {"pods/exec", "pods/attach", "pods/portforward", "nodes/proxy", "serviceaccounts/token", "pods/ephemeralcontainers"}
BROAD_GROUPS = {"system:anonymous", "system:unauthenticated", "system:authenticated"}
ENFORCEMENT_WORDS = re.compile(r"block|deny|disallow|restrict|require|enforce|prevent|refuse|forbid|reject", re.I)
DIGEST = re.compile(r"@sha256:[0-9a-f]{64}$|@sha256:[0-9a-f]{6,}", re.I)
IMAGE_REFERENCE = re.compile(r"^[a-z0-9][a-z0-9._/-]*(?::[A-Za-z0-9._-]+)?(?:@sha256:[0-9a-fA-F]+)?$")
SECRET_NAME = re.compile(r"(?i)(password|passwd|secret|token|api_?key|private_?key|access_?key|credential)")
POD_ANNOTATION_LIST = re.compile(r"(?m)^\s*(pod_annotations|container_annotations)\s*=\s*\[([^\]]*)\]")
KATA_ENABLE_ANNOTATIONS = re.compile(r"(?m)^\s*enable_annotations\s*=\s*\[([^\]]*)\]")
FLAG_OVERRIDE = re.compile(r"(?mi)^\s*[\"']?allow[-_]flag[-_]override[\"']?\s*[=:]\s*[\"']?true")
FULL_SHA = re.compile(r"^[0-9a-f]{40}$|^[0-9a-f]{64}$")
EXPRESSION = re.compile(r"\$\{\{(.*?)\}\}", re.S)
SECRET_EXPRESSION = re.compile(r"\bsecrets\.")
PRINT_COMMAND = re.compile(r"(?:^|[;&|]\s*|\s)(?:echo|printf|cat\s*<<|tee)\b")
UNTRUSTED_CONTEXT = re.compile(
    r"github\.(?:head_ref|event\.(?:issue\.(?:title|body)|pull_request\.(?:title|body|head\.ref|head\.label|head\.repo\.default_branch)"
    r"|comment\.body|review\.body|review_comment\.body|discussion\.(?:title|body)|pages\.[^ ]*page_name|commits\.[^ ]*(?:message|author\.(?:email|name))"
    r"|head_commit\.(?:message|author\.(?:email|name))|workflow_run\.(?:head_branch|head_commit\.message|display_title)))"
)
HEAD_REFERENCE = re.compile(r"github\.event\.pull_request\.head|github\.head_ref|github\.event\.workflow_run\.head|refs/pull/")
CURL_PIPE_SHELL = re.compile(r"\b(?:curl|wget)\b[^|;&]*\|\s*(?:sudo\s+)?(?:ba|z|da|k)?sh\b")


@dataclass
class Finding:
    rule: str
    number: str
    severity: str
    path: str
    line: int
    message: str


@dataclass
class Document:
    path: str
    value: object
    namespace: str
    partial: bool = False


class Scan:
    def __init__(self):
        self.findings: list[Finding] = []
        self.files = 0
        self.documents = 0
        self.skipped: list[dict] = []
        self.namespace_cache: dict[Path, str] = {}
        self.workload_namespaces: dict[str, tuple[str, int]] = {}
        self.policed_namespaces: set[str] = set()
        self.cluster_network_policy = False
        self.patch_files: set[Path] = set()
        self.patching_directories: set[str] = set()
        self.fixture_directories: set[Path] = set()

    def add(self, rule: str, path: str, line: int, message: str, severity: str = ""):
        definition = RULES[rule]
        self.findings.append(Finding(rule, definition.number, severity or definition.severity, path, line or 1, message))


def get(node, *keys):
    for key in keys:
        if not isinstance(node, dict):
            return None
        node = node.get(key)
    return node


def line_of(node, key=None, fallback=1) -> int:
    if isinstance(node, Mapping):
        if key is not None and key in node.key_lines:
            return node.key_lines[key]
        return node.line
    if isinstance(node, Sequence):
        return node.line
    return fallback


def as_list(value) -> list:
    if isinstance(value, list):
        return list(value)
    if value is None:
        return []
    return [value]


def walk(node, parent=None):
    if isinstance(node, dict):
        for key, value in node.items():
            yield node, key, value
            yield from walk(value, node)
    elif isinstance(node, list):
        for item in node:
            yield from walk(item, node)


def namespace_for_directory(scan: Scan, directory: Path, roots: list[Path]) -> str:
    if directory in scan.namespace_cache:
        return scan.namespace_cache[directory]
    name = ""
    for candidate in sorted(directory.glob("*.y*ml")):
        if candidate.suffix not in YAML_SUFFIXES or candidate.stat().st_size > MAX_FILE_BYTES:
            continue
        text = candidate.read_text(errors="replace")
        if "kind: Namespace" not in text and "namespace:" not in text:
            continue
        try:
            for value in documents(text):
                if get(value, "kind") == "Namespace" and get(value, "metadata", "name"):
                    name = str(get(value, "metadata", "name"))
                elif candidate.name in ("kustomization.yaml", "kustomization.yml") and isinstance(get(value, "namespace"), str):
                    name = name or get(value, "namespace")
        except YamlError:
            continue
        if name:
            break
    if not name and directory.parent != directory and any(directory != root and root in directory.parents for root in roots):
        name = namespace_for_directory(scan, directory.parent, roots)
    scan.namespace_cache[directory] = name
    return name


def check_document(scan: Scan, document: Document):
    value = document.value
    if not isinstance(value, Mapping):
        return
    kind = get(value, "kind")
    if kind == "List" and isinstance(get(value, "items"), list):
        for item in value["items"]:
            check_document(scan, Document(document.path, item, document.namespace, document.partial))
        return
    scan.documents += 1
    namespace = str(get(value, "metadata", "namespace") or document.namespace or "")
    if kind not in POLICY_KINDS:
        check_host_settings(scan, document.path, value)
    if kind in POD_SPEC_PATHS:
        check_workload(scan, document.path, value, kind, document.partial)
    elif kind not in POLICY_KINDS:
        check_value_images(scan, document.path, value)
    if kind in ("Role", "ClusterRole"):
        check_role(scan, document.path, value, kind)
    if kind in ("RoleBinding", "ClusterRoleBinding"):
        check_binding(scan, document.path, value)
    if kind in ("ClusterPolicy", "Policy", "ValidatingPolicy", "ImageValidatingPolicy", "ValidatingAdmissionPolicyBinding"):
        check_policy_mode(scan, document.path, value, kind)
    if kind in ("ValidatingWebhookConfiguration", "MutatingWebhookConfiguration", "ClusterPolicy", "Policy", "ValidatingAdmissionPolicy", "ValidatingPolicy"):
        check_fail_open(scan, document.path, value, kind)
    if kind == "Secret":
        check_secret(scan, document.path, value)
    if kind in WORKLOAD_BEARING_KINDS and namespace and not document.partial:
        scan.workload_namespaces.setdefault(namespace, (document.path, line_of(value)))
    if kind in NETWORK_POLICY_KINDS and namespace:
        scan.policed_namespaces.add(namespace)
    if kind in CLUSTER_NETWORK_POLICY_KINDS:
        scan.cluster_network_policy = True


def check_host_settings(scan: Scan, path: str, root):
    for owner, key, value in walk(root):
        line = line_of(owner, key)
        if key == "privileged" and value is True:
            scan.add("privileged-container", path, line, "privileged: true gives the container every host capability and device")
        elif key in ("hostNetwork", "hostPID", "hostIPC") and value is True:
            scan.add("host-namespace", path, line, f"{key}: true shares the node's namespace with the pod")
        elif key == "hostPath" and isinstance(value, dict) and value.get("path"):
            scan.add("hostpath-volume", path, line, f"hostPath {value.get('path')} mounts a node path into the pod")
        elif key == "allowPrivilegeEscalation" and value is True:
            scan.add("privilege-escalation-allowed", path, line, "allowPrivilegeEscalation: true lets a process gain more privileges than its parent")
        elif key == "capabilities" and isinstance(value, dict):
            added = {str(capability).upper().removeprefix("CAP_") for capability in as_list(value.get("add"))}
            dangerous = sorted(added & DANGEROUS_CAPABILITIES)
            if dangerous:
                scan.add("dangerous-capability-added", path, line_of(value, "add", line), f"capabilities.add grants {', '.join(dangerous)}")


def check_image(scan: Scan, path: str, line: int, image: str, where: str):
    if not isinstance(image, str) or "$" in image or "{{" in image:
        return
    if DIGEST.search(image):
        return
    scan.add("image-not-digest-pinned", path, line, f"{where} image {image} is not pinned by digest")


def check_workload(scan: Scan, path: str, value: Mapping, kind: str, partial: bool):
    spec_path = POD_SPEC_PATHS[kind]
    pod = get(value, *spec_path)
    if not isinstance(pod, Mapping):
        return
    metadata = get(value, *spec_path[:-1], "metadata") if len(spec_path) > 1 else get(value, "metadata")
    annotations = get(metadata, "annotations")
    if isinstance(annotations, Mapping):
        for key in annotations:
            if str(key).startswith(SANDBOX_ANNOTATION_PREFIXES):
                scan.add("sandbox-override-annotation", path, line_of(annotations, key), f"annotation {key} asks the sandbox runtime to change its own configuration")
    pod_context = get(pod, "securityContext") or {}
    if not partial and get(pod, "automountServiceAccountToken") is not False and not get(pod, "serviceAccountName") and not get(pod, "serviceAccount"):
        scan.add("default-token-automounted", path, line_of(pod), f"{kind} {get(value, 'metadata', 'name')} mounts the default service account token")
    containers = [*as_list(get(pod, "initContainers")), *as_list(get(pod, "containers")), *as_list(get(pod, "ephemeralContainers"))]
    for container in containers:
        if not isinstance(container, Mapping):
            continue
        name = container.get("name", "?")
        check_image(scan, path, line_of(container, "image"), container.get("image"), f"container {name}")
        context = container.get("securityContext") or {}
        if partial:
            continue
        missing = []
        run_as_user = context.get("runAsUser", pod_context.get("runAsUser"))
        if not (context.get("runAsNonRoot", pod_context.get("runAsNonRoot")) is True or (isinstance(run_as_user, int) and run_as_user > 0)):
            missing.append("runAsNonRoot")
        if context.get("allowPrivilegeEscalation") is not False and context.get("privileged") is not True:
            missing.append("allowPrivilegeEscalation: false")
        drops = {str(capability).upper() for capability in as_list(get(context, "capabilities", "drop"))}
        if "ALL" not in drops:
            missing.append("capabilities.drop: [ALL]")
        if context.get("readOnlyRootFilesystem") is not True:
            missing.append("readOnlyRootFilesystem: true")
        if not (get(context, "seccompProfile", "type") or get(pod_context, "seccompProfile", "type")):
            missing.append("seccompProfile")
        if missing:
            scan.add("security-context-incomplete", path, line_of(container), f"container {name} lacks {', '.join(missing)}")


def check_value_images(scan: Scan, path: str, root):
    for owner, key, value in walk(root):
        if key != "image":
            continue
        line = line_of(owner, key)
        if isinstance(value, str) and IMAGE_REFERENCE.match(value) and ("/" in value or ":" in value):
            check_image(scan, path, line, value, "values")
        elif isinstance(value, Mapping) and isinstance(value.get("repository"), str):
            tag = str(value.get("tag") or value.get("version") or "")
            if value.get("digest") or DIGEST.search(tag) or "$" in tag or "{{" in tag:
                continue
            if not tag:
                scan.add("image-not-digest-pinned", path, line, f"values image {value['repository']} takes the chart's default tag, pinned only as far as the chart version is", "note")
                continue
            scan.add("image-not-digest-pinned", path, line, f"values image {value['repository']}:{tag} is not pinned by digest")


def check_role(scan: Scan, path: str, value: Mapping, kind: str):
    for rule in as_list(get(value, "rules")):
        if not isinstance(rule, Mapping):
            continue
        verbs = {str(verb) for verb in as_list(rule.get("verbs"))}
        resources = {str(resource) for resource in as_list(rule.get("resources"))}
        line = line_of(rule)
        if "*" in verbs or "*" in resources:
            severity = "fail" if kind == "ClusterRole" and "*" in verbs and "*" in resources else ""
            scan.add("rbac-wildcard", path, line, f"{kind} {get(value, 'metadata', 'name')} grants verbs {sorted(verbs)} on {sorted(resources)}", severity)
            continue
        if "secrets" in resources and verbs & READ_VERBS and not rule.get("resourceNames"):
            severity = "warn" if kind == "ClusterRole" else "note"
            scan.add("rbac-secrets-read", path, line, f"{kind} {get(value, 'metadata', 'name')} can {'/'.join(sorted(verbs & READ_VERBS))} every Secret in scope", severity)
        dangerous = sorted(verbs & ESCALATION_VERBS) + sorted(resources & SENSITIVE_SUBRESOURCES)
        if dangerous:
            scan.add("rbac-dangerous-verb", path, line, f"{kind} {get(value, 'metadata', 'name')} grants {', '.join(dangerous)}")


def check_binding(scan: Scan, path: str, value: Mapping):
    role = get(value, "roleRef", "name")
    name = get(value, "metadata", "name")
    for subject in as_list(get(value, "subjects")):
        if isinstance(subject, Mapping) and subject.get("name") in BROAD_GROUPS:
            scan.add("rbac-broad-binding", path, line_of(subject), f"binding {name} grants {role} to {subject.get('name')}", "fail")
    if role == "cluster-admin":
        scan.add("rbac-broad-binding", path, line_of(value, "roleRef"), f"binding {name} grants cluster-admin")


def enforcement_named(value: Mapping) -> bool:
    annotations = get(value, "metadata", "annotations") or {}
    text = " ".join([str(get(value, "metadata", "name") or ""), str(get(value, "spec", "policyName") or ""),
                     str(annotations.get("policies.kyverno.io/title", "")) if isinstance(annotations, dict) else ""])
    return bool(ENFORCEMENT_WORDS.search(text))


def check_policy_mode(scan: Scan, path: str, value: Mapping, kind: str):
    if not enforcement_named(value):
        return
    name = get(value, "metadata", "name")
    spec = get(value, "spec") or {}
    if kind in ("ClusterPolicy", "Policy"):
        action = str(spec.get("validationFailureAction") or "")
        if action.lower() == "audit":
            scan.add("enforcement-policy-in-audit", path, line_of(spec, "validationFailureAction"), f"{kind} {name} has validationFailureAction Audit; it reports and does not block")
        for rule in as_list(spec.get("rules")):
            validate = get(rule, "validate")
            if isinstance(validate, Mapping) and str(validate.get("failureAction") or "").lower() == "audit":
                scan.add("enforcement-policy-in-audit", path, line_of(validate, "failureAction"), f"{kind} {name} rule {get(rule, 'name')} has failureAction Audit")
        return
    actions = {str(action) for action in as_list(spec.get("validationActions"))}
    if actions and "Deny" not in actions:
        scan.add("enforcement-policy-in-audit", path, line_of(spec, "validationActions"), f"{kind} {name} has validationActions {sorted(actions)} without Deny")


def check_fail_open(scan: Scan, path: str, value: Mapping, kind: str):
    name = get(value, "metadata", "name")
    if kind.endswith("WebhookConfiguration"):
        for webhook in as_list(get(value, "webhooks")):
            if isinstance(webhook, Mapping) and webhook.get("failurePolicy") == "Ignore":
                scan.add("admission-fail-open", path, line_of(webhook, "failurePolicy"), f"webhook {webhook.get('name')} in {name} has failurePolicy Ignore")
        return
    spec = get(value, "spec") or {}
    if isinstance(spec, Mapping) and spec.get("failurePolicy") == "Ignore" and enforcement_named(value):
        scan.add("admission-fail-open", path, line_of(spec, "failurePolicy"), f"{kind} {name} has failurePolicy Ignore; requests pass while the engine is down")


def check_secret(scan: Scan, path: str, value: Mapping):
    if "sops" in value:
        return
    keys = []
    for field_name in ("data", "stringData"):
        block = value.get(field_name)
        if not isinstance(block, Mapping):
            continue
        for key, item in block.items():
            text = str(item if item is not None else "")
            if not text or text.startswith("ENC[") or re.fullmatch(r"\$\{[^}]+\}", text.strip()):
                continue
            keys.append(str(key))
    if not keys:
        return
    line = line_of(value, "data" if "data" in value else "stringData")
    name = get(value, "metadata", "name")
    listed = ", ".join(sorted(keys)[:5])
    if ignored_by_git(path):
        scan.add("plaintext-secret", path, line, f"Secret {name} has plaintext values for {listed} on this disk; git ignores the file, so it is a local copy, not a commit (values not shown)", "note")
        return
    scan.add("plaintext-secret", path, line, f"Secret {name} has plaintext values for {listed} in a file that is not SOPS-encrypted (values not shown)")


def ignored_by_git(path: str) -> bool:
    if path == "stdin":
        return False
    location = Path(path)
    try:
        result = subprocess.run(["git", "-C", str(location.parent), "check-ignore", "-q", location.name], capture_output=True, timeout=10)
    except (OSError, subprocess.SubprocessError):
        return False
    return result.returncode == 0


def check_runtime_text(scan: Scan, path: str, text: str):
    for match in POD_ANNOTATION_LIST.finditer(text):
        entries = match.group(2)
        if "*" in entries or "io.katacontainers" in entries or "dev.gvisor" in entries:
            line = text.count("\n", 0, match.start(1)) + 1
            scan.add("sandbox-annotation-passthrough", path, line, f"containerd {match.group(1)} forwards {entries.strip()} to the sandbox runtime")
    for match in KATA_ENABLE_ANNOTATIONS.finditer(text):
        if match.group(1).strip():
            line = text.count("\n", 0, match.start()) + 1
            scan.add("sandbox-annotation-passthrough", path, line, f"Kata enable_annotations accepts {match.group(1).strip()} from pods")
    for match in FLAG_OVERRIDE.finditer(text):
        line = text.count("\n", 0, match.start()) + 1
        scan.add("sandbox-annotation-passthrough", path, line, "gVisor allows pod annotations to override runsc flags")


def is_workflow(path: Path) -> bool:
    text = path.as_posix()
    return path.suffix in YAML_SUFFIXES and any(f"{directory}/" in text for directory in WORKFLOW_DIRECTORIES)


def triggers(workflow: Mapping) -> set[str]:
    on = workflow.get("on", workflow.get(True))
    if isinstance(on, str):
        return {on}
    if isinstance(on, list):
        return {str(item) for item in on}
    if isinstance(on, dict):
        return {str(key) for key in on}
    return set()


def run_lines(step: Mapping):
    script = step.get("run")
    if not isinstance(script, str):
        return
    start = script.first_line if isinstance(script, Text) and script.first_line else line_of(step, "run")
    for offset, line in enumerate(script.splitlines()):
        yield start + offset if isinstance(script, Text) and script.first_line else start, line


def check_workflow(scan: Scan, path: str, workflow):
    if not isinstance(workflow, Mapping) or not isinstance(workflow.get("jobs"), Mapping):
        return
    scan.documents += 1
    events = triggers(workflow)
    privileged_trigger = bool(events & {"pull_request_target", "workflow_run"})
    forge = "forgejo" if "/.forgejo/" in f"/{path}" or "/.gitea/" in f"/{path}" else "github"
    top_permissions = workflow.get("permissions")
    if top_permissions == "write-all":
        scan.add("ci-permissions-write-all", path, line_of(workflow, "permissions"), "workflow token has write-all")
    for job_name, job in workflow["jobs"].items():
        if not isinstance(job, Mapping):
            continue
        job_permissions = job.get("permissions")
        if job_permissions == "write-all":
            scan.add("ci-permissions-write-all", path, line_of(job, "permissions"), f"job {job_name} token has write-all")
        if top_permissions is None and job_permissions is None:
            severity = "note" if forge == "forgejo" else ""
            scan.add("ci-permissions-missing", path, line_of(workflow["jobs"], job_name), f"job {job_name} runs with the default token permissions", severity)
        if isinstance(job.get("uses"), str):
            check_uses(scan, path, line_of(job, "uses"), job["uses"])
        for step in as_list(job.get("steps")):
            if not isinstance(step, Mapping):
                continue
            uses = step.get("uses")
            if isinstance(uses, str):
                check_uses(scan, path, line_of(step, "uses"), uses)
                reference = str(get(step, "with", "ref") or "")
                if privileged_trigger and re.search(r"(?:^|/)actions/checkout@", uses) and HEAD_REFERENCE.search(reference):
                    scan.add("ci-untrusted-checkout", path, line_of(step, "uses"),
                             f"job {job_name} runs on {', '.join(sorted(events & {'pull_request_target', 'workflow_run'}))} and checks out the pull request head")
            for number, line in run_lines(step):
                expressions = EXPRESSION.findall(line)
                if "add-mask" not in line and PRINT_COMMAND.search(line) and any(SECRET_EXPRESSION.search(expression) for expression in expressions):
                    scan.add("ci-secret-echoed", path, number, f"job {job_name} prints a secret expression in a run script")
                for expression in expressions:
                    if UNTRUSTED_CONTEXT.search(expression):
                        scan.add("ci-untrusted-expression-in-run", path, number, f"job {job_name} interpolates {expression.strip()} into a shell script")


def check_uses(scan: Scan, path: str, line: int, uses: str):
    if uses.startswith("./") or uses.startswith("docker://"):
        if uses.startswith("docker://") and not DIGEST.search(uses):
            scan.add("ci-action-not-sha-pinned", path, line, f"{uses} is a container action without a digest")
        return
    if "@" not in uses:
        scan.add("ci-action-not-sha-pinned", path, line, f"{uses} has no ref at all")
        return
    reference = uses.rsplit("@", 1)[1]
    if not FULL_SHA.match(reference):
        scan.add("ci-action-not-sha-pinned", path, line, f"{uses} is pinned to {reference}, which can move")


def dockerfile_instructions(text: str):
    pending = ""
    start = 0
    for number, line in enumerate(text.splitlines(), start=1):
        stripped = line.strip()
        if not pending and (not stripped or stripped.startswith("#")):
            continue
        if pending and stripped.startswith("#"):
            continue
        if not pending:
            start = number
        if stripped.endswith("\\"):
            pending += stripped[:-1] + " "
            continue
        full = (pending + stripped).strip()
        pending = ""
        instruction, _, arguments = full.partition(" ")
        yield start, instruction.upper(), arguments.strip()
    if pending:
        instruction, _, arguments = pending.strip().partition(" ")
        yield start, instruction.upper(), arguments.strip()


def check_dockerfile(scan: Scan, path: str, text: str):
    stages: set[str] = set()
    final_from_line = 0
    final_image = ""
    final_user = None
    arguments_seen: dict[str, str] = {}
    for line, instruction, arguments in dockerfile_instructions(text):
        if instruction == "FROM":
            words = [word for word in arguments.split() if not word.startswith("--")]
            image = words[0] if words else ""
            if len(words) >= 3 and words[1].lower() == "as":
                stages.add(words[2].lower())
            final_from_line, final_image, final_user = line, image, None
            if image.lower() in stages or image.lower() == "scratch":
                continue
            if "$" in image:
                variable = re.search(r"\$\{?([A-Za-z_][A-Za-z0-9_]*)", image)
                default = arguments_seen.get(variable.group(1), "") if variable else ""
                if not DIGEST.search(default):
                    scan.add("dockerfile-base-not-digest-pinned", path, line, f"FROM {image} comes from a build argument whose default has no digest")
                continue
            if not DIGEST.search(image):
                scan.add("dockerfile-base-not-digest-pinned", path, line, f"FROM {image} is not pinned by digest")
        elif instruction == "USER":
            final_user = arguments.split()[0] if arguments else ""
        elif instruction == "ADD" and re.search(r"(?:^|\s)https?://", arguments) and "--checksum" not in arguments:
            scan.add("dockerfile-remote-add", path, line, "ADD downloads a URL with no --checksum")
        elif instruction in ("ARG", "ENV"):
            for name, value in parse_assignments(instruction, arguments):
                if instruction == "ARG" and (value or name not in arguments_seen):
                    arguments_seen[name] = value
                if SECRET_NAME.search(name) and not name.upper().endswith(("_FILE", "_PATH", "_URL")):
                    if instruction == "ARG" or value:
                        scan.add("dockerfile-secret-in-build-arg", path, line, f"{instruction} {name} keeps a secret-like value in the image history (value not shown)")
        elif instruction == "RUN" and CURL_PIPE_SHELL.search(arguments):
            scan.add("dockerfile-pipe-to-shell", path, line, "RUN pipes a download into a shell without verifying it")
    if final_from_line and final_image.lower() != "scratch":
        user = (final_user or "").split(":")[0]
        if final_user is None and re.search(r"nonroot|distroless.*:nonroot|chainguard", final_image, re.I):
            return
        if final_user is None or user in ("root", "0", ""):
            scan.add("dockerfile-runs-as-root", path, final_from_line, f"final stage {final_image} runs as {'root' if final_user else 'the image default user, often root'}")


def parse_assignments(instruction: str, arguments: str):
    if instruction == "ENV" and "=" not in arguments.split(" ", 1)[0]:
        name, _, value = arguments.partition(" ")
        yield name, value.strip()
        return
    for token in re.findall(r"""([A-Za-z_][A-Za-z0-9_]*)(?:=("[^"]*"|'[^']*'|\S*))?""", arguments):
        yield token[0], token[1].strip("\"'")


def scan_text(scan: Scan, path: Path, label: str, text: str, roots: list[Path]):
    scan.files += 1
    if DOCKERFILE_NAME.search(path.name):
        check_dockerfile(scan, label, text)
        return
    check_runtime_text(scan, label, text)
    if path.suffix not in YAML_SUFFIXES and label != "stdin":
        return
    try:
        values = list(documents(text))
    except YamlError as error:
        templated = any(GO_TEMPLATE_LINE.match(line) for line in text.splitlines())
        scan.skipped.append({"path": label, "reason": "unrendered Go template; render it first" if templated else f"not parsed: {error}"})
        return
    if is_workflow(path):
        for value in values:
            check_workflow(scan, label, value)
        return
    namespace = namespace_for_directory(scan, path.parent, roots) if label != "stdin" else ""
    partial = path in scan.patch_files
    for value in values:
        check_document(scan, Document(label, value, namespace, partial))


def policy_fixture_directory(path: Path, root: Path):
    for directory in [path.parent, *path.parent.parents]:
        if any((directory / marker).exists() for marker in POLICY_TEST_MARKERS):
            return directory
        if directory == root or root not in directory.parents:
            return None
    return None


def collect_patches(scan: Scan, root: Path):
    candidates = [root] if root.is_file() else [path for name in KUSTOMIZATION_NAMES for path in root.rglob(name)]
    for kustomization in candidates:
        if kustomization.name not in KUSTOMIZATION_NAMES or any(part in SKIPPED_DIRECTORIES for part in kustomization.parts):
            continue
        try:
            values = list(documents(kustomization.read_text(errors="replace")))
        except (YamlError, OSError):
            continue
        for value in values:
            if not isinstance(value, Mapping):
                continue
            references = [entry.get("path") if isinstance(entry, dict) else entry
                          for field_name in ("patches", "patchesStrategicMerge", "patchesJson6902")
                          for entry in as_list(value.get(field_name))]
            references = [reference for reference in references if isinstance(reference, str) and "\n" not in reference]
            if references or value.get("patches") or value.get("patchesStrategicMerge"):
                scan.patching_directories.add(str(kustomization.parent))
            for reference in references:
                scan.patch_files.add((kustomization.parent / reference).resolve())


def candidate_files(scan: Scan, root: Path):
    if root.is_file():
        return [root]
    found = []
    for path in sorted(root.rglob("*")):
        if any(part in SKIPPED_DIRECTORIES for part in path.relative_to(root).parts[:-1]):
            continue
        if not path.is_file() or path.stat().st_size > MAX_FILE_BYTES:
            continue
        if path.suffix in YAML_SUFFIXES or path.suffix in TEXT_CONFIG_SUFFIXES or DOCKERFILE_NAME.search(path.name):
            found.append(path)
    ignored = git_ignored(root, found)
    if ignored:
        scan.skipped.append({"path": str(root), "reason": f"{len(ignored)} git-ignored files (generated output or local copies); pass one explicitly to scan it"})
    return [path for path in found if path not in ignored]


def git_ignored(root: Path, paths: list[Path]) -> set[Path]:
    if not paths:
        return set()
    try:
        result = subprocess.run(["git", "-C", str(root), "check-ignore", "--stdin"], input="\n".join(str(path.resolve()) for path in paths),
                                capture_output=True, text=True, timeout=60)
    except (OSError, subprocess.SubprocessError):
        return set()
    if result.returncode not in (0, 1):
        return set()
    listed = {Path(line).resolve() for line in result.stdout.splitlines() if line}
    return {path for path in paths if path.resolve() in listed}


def run(paths: list[str]) -> Scan:
    scan = Scan()
    roots = [Path(path).resolve() for path in paths if path != "-"]
    for raw in paths:
        if raw == "-":
            scan_text(scan, Path("stdin.yaml"), "stdin", sys.stdin.read(), roots)
            continue
        root = Path(raw)
        if not root.exists():
            scan.skipped.append({"path": raw, "reason": "does not exist"})
            continue
        collect_patches(scan, root.resolve())
        for path in candidate_files(scan, root):
            fixture = policy_fixture_directory(path.resolve(), root.resolve())
            if fixture is not None:
                if fixture not in scan.fixture_directories:
                    scan.fixture_directories.add(fixture)
                    scan.skipped.append({"path": str(fixture), "reason": "policy test fixtures (kyverno or chainsaw test); deliberately non-compliant"})
                continue
            label = str(path)
            scan_text(scan, path.resolve(), label, path.read_text(errors="replace"), roots)
    if not scan.cluster_network_policy:
        for namespace, (path, line) in sorted(scan.workload_namespaces.items()):
            if namespace not in scan.policed_namespaces:
                scan.add("namespace-without-networkpolicy", path, line, f"namespace {namespace} runs workloads and no NetworkPolicy for it was found")
    scan.findings.sort(key=lambda finding: (-SEVERITY_RANK[finding.severity], finding.number, finding.path, finding.line))
    return scan


def cannot_see(scan: Scan) -> list[str]:
    areas = {RULES[finding.rule].area for finding in scan.findings}
    areas |= {"pod", "rbac", "network", "admission"} if scan.documents else set()
    lines = [CANNOT_SEE["always"]]
    lines += [CANNOT_SEE[area] for area in ("pod", "image", "runtime", "rbac", "network", "admission", "secret", "ci") if area in areas]
    if scan.cluster_network_policy:
        lines.append("A cluster-wide network policy is in the scanned set, so per-namespace coverage was not judged.")
    if scan.patching_directories:
        lines.append(f"{len(scan.patching_directories)} kustomizations apply patches: findings on a base describe it unpatched, and patch files are judged only for what they add. Scan kubectl kustomize output for the merged objects.")
    return lines


def print_text(scan: Scan):
    print("Default severity per rule; the triage priority is exposure x blast radius, decided by the reader.\n")
    if scan.findings:
        print("| Rule | Severity | Where | Finding |")
        print("| --- | --- | --- | --- |")
        grouped: dict[tuple[str, str, str], list[Finding]] = {}
        for finding in scan.findings:
            grouped.setdefault((finding.rule, finding.path, finding.severity), []).append(finding)
        for (rule, path, severity), items in grouped.items():
            first = items[0]
            more = f" (+{len(items) - 1} more at lines {', '.join(str(item.line) for item in items[1:6])}{'...' if len(items) > 6 else ''})" if len(items) > 1 else ""
            print(f"| {first.number} {rule} | {severity} | {path}:{first.line} | {first.message.replace('|', '/')}{more} |")
    else:
        print("No findings from these rules.")
    counts = {severity: sum(1 for finding in scan.findings if finding.severity == severity) for severity in ("fail", "warn", "note")}
    print(f"\nCoverage: {scan.files} files, {scan.documents} documents judged; {counts['fail']} fail, {counts['warn']} warn, {counts['note']} note.")
    for skipped in scan.skipped:
        print(f"Skipped: {skipped['path']}: {skipped['reason']}")
    print("\nWhat a file read cannot see:")
    for line in cannot_see(scan):
        print(f"- {line}")


def print_rules(as_json: bool):
    if as_json:
        print(json.dumps([asdict(rule) for rule in RULES.values()], indent=2))
        return
    for rule in RULES.values():
        print(f"{rule.number}  {rule.name:<34} {rule.severity:<5} {rule.summary}")


def main(argv=None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("paths", nargs="*", help="files or directories; - reads one YAML stream from stdin")
    parser.add_argument("--json", action="store_true")
    parser.add_argument("--fail-on", choices=("fail", "warn", "note", "never"), default="fail")
    parser.add_argument("--rules", action="store_true", help="list the rules and exit")
    options = parser.parse_args(argv)
    if options.rules:
        print_rules(options.json)
        return 0
    if not options.paths:
        parser.error("give at least one PATH")
    scan = run(options.paths)
    if options.json:
        print(json.dumps({
            "findings": [asdict(finding) for finding in scan.findings],
            "coverage": {"files": scan.files, "documents": scan.documents, "skipped": scan.skipped, "patching_kustomizations": len(scan.patching_directories)},
            "cannot_see": cannot_see(scan),
        }, indent=2))
    else:
        print_text(scan)
    threshold = SEVERITY_RANK[options.fail_on]
    return 1 if any(SEVERITY_RANK[finding.severity] >= threshold for finding in scan.findings) else 0


if __name__ == "__main__":
    sys.exit(main())
