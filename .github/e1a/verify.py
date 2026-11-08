#!/usr/bin/env python3
"""Fixed-source E1A controller and fail-closed evidence verifier. No dependencies.

self-test/check do not invoke Docker. run/collect-cleanup require ci.sh's GitHub
push admission too. Synthetic self-tests validate the verifier, NOT PostgreSQL.
The source fixtures, not text markers alone, implement the SQL assertions.
"""
import copy
import csv
import hashlib
import io
import json
import os
import platform
import re
import secrets
import shutil
import signal
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

SOURCE = "21fa6cd6bf8cf6d0de37953d39eed243680ec359"
TREE = "53d259004b87eef5ed6a39cf544385ca635be23d"
INITIAL_CONTROL = "31b65b4f6afd379e11625480b57d7341467acc68"
PREFIX = "TestExtensionIntegration"
PASS = {PREFIX + n for n in (
    "AdmissionT0 BusinessPreservation CHECKLiteralRegression CHECKMapping "
    "ConcurrentApply HardCrash LedgerMetadataT0 MetadataV2 OrdinaryCLIT0 "
    "ReceiptRollbackT0 RecoveryConnectFailureT0 RecoveryValidation Resume "
    "TargetGuardsT0 TransportCloseT0 UnconfirmedCommit UnsupportedT0").split()}
SKIP = {PREFIX + "RestartChild", PREFIX + "CrashChild"}
AUTH = "TestExtensionAuthPreflight"
SUBTESTS = {PREFIX + "CHECKLiteralRegression/ledger_checksum_hex",
            PREFIX + "CHECKLiteralRegression/ledger_profile_clean",
            PREFIX + "HardCrash/before_commit", PREFIX + "HardCrash/after_commit"}
OFFLINE = {"TestExtension" + n for n in (
    "CHECKQuotedRegression CHECKUnsupportedForms AddressConstructorGuard "
    "AuthFailureClassification Manifest Prefix Options ApprovalAndSQLContracts "
    "JSONDuplicates Snapshots CHECKDefinitions RecoveryFaults RestartPrefix "
    "ReleaseApproval MetadataContract CatalogAdmission BaselineLineage").split()}
PROBES = {"TestExtensionHarnessProbeA", "TestExtensionHarnessProbeB"}
IMAGES = {
    "pg": "docker.io/library/postgres@sha256:02fb3245e038f33536cf13bc6c4e59f21d07b0cea3c1fe1c07c6e8aa8f083bcc",
    "go": "docker.io/library/golang@sha256:025eefc1e8dbe63201734a72aaa0a55d7a077f2d8410432f8ae2f3e939f32871"}
REQUIRED_EXITS = {
    "tools", "source", "resources_build", "pull_pg", "pull_go", "image_pg",
    "image_go", "compose_config", "build", "resources_runtime", "start_pg",
    "identity", "compiled", "offline", "harness", "suite"}
PUBLIC = {
    "provenance.json", "source-manifest.csv", "images.json", "build.log",
    "postgres-identity.json", "compiled-tests.txt", "offline.log", "harness.log",
    "tests.log", "auth-hba.log", "test-results.csv", "exit-codes.json",
    "docker-resources.json", "cleanup.json", "commands.json", "resources.json", "runtime.log",
    "pull_pg.log", "pull_go.log", "tools.log", "compose_version.log", "failure.log"}
CONTROL_PATHS = {".github/workflows/e1a-fixed-source.yml", ".github/e1a/ci.sh",
                 ".github/e1a/verify.py"}


def need(condition, reason):
    if not condition:
        raise ValueError(reason)


def strict_bytes(value):
    """Compose UnitBytes JSON: nonnegative integer or canonical ASCII decimal."""
    if type(value) is int:
        need(value >= 0, "negative byte count")
        return value
    need(type(value) is str and re.fullmatch(r"0|[1-9][0-9]*", value) is not None,
         "byte count must be an integer or unsigned decimal string")
    return int(value)


def validate_memory(service, name):
    need(name in ("pg", "tests"), "unexpected memory-limit service")
    need("mem_limit" in service and "memswap_limit" in service,
         "missing memory limit: " + name)
    memory = strict_bytes(service["mem_limit"])
    swap = strict_bytes(service["memswap_limit"])
    need(memory == (3 if name == "pg" else 1) * 1024**3 and swap == memory,
         "incorrect memory limit: " + name)


def verify_source_identity(head, tree, status):
    need(head == SOURCE, "source SHA")
    need(tree == TREE, "source tree")
    need(not status, "dirty source")


def verify_control_ancestry(workflow, hotfix_line, initial_line):
    need(re.fullmatch(r"[0-9a-f]{40}", workflow) is not None and
         workflow not in (SOURCE, INITIAL_CONTROL), "workflow SHA")
    # Exactly one parent at BOTH links; shallow/missing ancestry fails closed.
    need(hotfix_line.split() == [workflow, INITIAL_CONTROL], "hotfix parent/merge")
    need(initial_line.split() == [INITIAL_CONTROL, SOURCE], "initial control parent/merge")


def digest(data):
    return hashlib.sha256(data).hexdigest()


def redact(text, password):
    if password:
        text = text.replace(password, "[REDACTED]")
    return re.sub(r"SCRAM-SHA-256\$[^\s\"']+", "[REDACTED-VERIFIER]", text)


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        need(key not in result, "duplicate JSON key")
        result[key] = value
    return result


def read_json(path):
    return json.loads(path.read_text(encoding="utf-8"), object_pairs_hook=unique_object)


def write_json(path, value):
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def outcomes(text, roots):
    result = {}
    for status, name in re.findall(r"^\s*--- (PASS|FAIL|SKIP): (\S+) \([^\n]*\)$", text, re.M):
        need(name not in result, "duplicate test outcome: " + name)
        # Nested names are explicitly admitted only under known suite roots.
        need(name.split("/")[0] in roots, "unknown test: " + name)
        need(status != "FAIL", "failed test: " + name)
        need(status != "SKIP" or name in SKIP, "unexpected SKIP: " + name)
        result[name] = status
    return result


def reports(text):
    result = {}
    for status, name in re.findall(r"^RESULT (\S+) (\S+)$", text, re.M):
        need(name not in result, "duplicate RESULT: " + name)
        result[name] = status
    need("UNFINISHED" not in text, "unfinished test")
    return result


def verify_harness(text, rc):
    need(rc == 1, "negative harness must exit exactly 1")
    need(reports(text) == {"TestExtensionHarnessProbeA": "FAIL",
                           "TestExtensionHarnessProbeB": "NOT_RUN"}, "harness results")
    for marker in ("synthetic harness failure; no PostgreSQL",
                   "INTEGRATION_EXIT=1 LOG_TEE_EXIT=0",
                   "REPORT_AWK_EXIT=0 REPORT_SORT_EXIT=0"):
        need(text.count(marker) == 1, "harness missing/duplicate marker")
    need("MUST_NOT_RUN" not in text and "RESULT PASS" not in text, "harness false PASS")
    need(len(re.findall(r"^--- FAIL: TestExtensionHarnessProbeA \([^\n]*\)$", text, re.M)) == 1 and
         not re.search(r"^=== RUN\s+TestExtensionHarnessProbeB$", text, re.M), "missing actual harness failure/failfast")


def verify_logs(text):
    expected = {**dict.fromkeys(PASS, "PASS"), **dict.fromkeys(SKIP, "SKIP")}
    actual = outcomes(text, PASS | SKIP | {AUTH})
    top = {k: v for k, v in actual.items() if "/" not in k}
    need(top == {**expected, AUTH: "PASS"}, "incomplete/incompatible test outcomes")
    need(reports(text) == expected, "incomplete/incompatible RESULT set")
    need(all(actual.get(n) == "PASS" for n in SUBTESTS), "missing R4/crash subtest")
    for marker in ("AUTH_PREFLIGHT_EXIT=0", "INTEGRATION_EXIT=0 LOG_TEE_EXIT=0",
                   "REPORT_AWK_EXIT=0 REPORT_SORT_EXIT=0"):
        need(text.splitlines().count(marker) == 1, "missing/duplicate successful exit marker")
    auth = re.findall(r"AUTH PASS: correct password accepted; wrong password SQLSTATE=28P01; loopback SCRAM rule=(\d+)", text)
    need(len(auth) == 1 and int(auth[0]) > 0, "missing effective auth evidence")
    hba = re.findall(r"HBA file=(\S+) rules=(\[[^\n]+\])", text)
    need(len(hba) == 1 and hba[0][0].startswith("/"), "missing HBA diagnostics")
    rules = json.loads(hba[0][1], object_pairs_hook=unique_object)
    numbers = [r["rule_number"] for r in rules]
    need(numbers == sorted(set(numbers)), "HBA order/duplicates")
    need(all(r.get("error") is None and (not r["type"].startswith("host") or
             r["auth"] == "scram-sha-256") for r in rules), "unsafe HBA")
    need(any(r["rule_number"] == int(auth[0]) and r["type"] == "host" and
             r["address"] == "127.0.0.1" and r["netmask"] == "255.255.255.255" and
             r["database"] == ["all"] and r["users"] == ["all"] for r in rules), "HBA rule mismatch")
    lock = re.findall(r"observed concurrent backends: holder=(\d+) waiter=(\d+) key=6075995045621812551; holder granted, waiter blocked", text)
    need(len(lock) == 1 and int(lock[0][0]) > 0 and int(lock[0][1]) > 0 and
         lock[0][0] != lock[0][1], "missing actual contention evidence")
    return expected


def verify_bundle(directory, inventory=True):
    d = Path(directory)
    required = PUBLIC - {"failure.log"}
    need(all((d / n).is_file() and not (d / n).is_symlink() for n in required), "missing evidence")
    p = read_json(d / "provenance.json")
    need(p["source_sha"] == SOURCE and p["source_tree"] == TREE, "wrong source identity")
    need(re.fullmatch(r"[0-9a-f]{40}", p["workflow_sha"]) is not None and
         p["workflow_sha"] != SOURCE, "missing separate workflow identity")
    need(p["repository"] == "adamgrff/TREMOR_SCLAD" and p["baseline_verified"] == 22, "provenance")
    rows = list(csv.DictReader((d / "source-manifest.csv").open(encoding="utf-8", newline="")))
    need(len(rows) == 312 and len({r["path"] for r in rows}) == 312 and
         all(re.fullmatch(r"[0-9a-f]{40}", r["blob"]) and re.fullmatch(r"[0-9a-f]{64}", r["sha256"]) for r in rows), "source manifest")
    i = read_json(d / "postgres-identity.json")
    need(all(i.get(k) == v and type(i.get(k)) is type(v) for k, v in {
        "database": "tremor_e1a_test", "user": "e1a_test_admin", "address": "127.0.0.1",
        "port": 5432, "version": 180006, "encoding": "UTF8", "provider": "b",
        "locale": "C.UTF-8", "recovery": False, "schema": True}.items()), "TEST identity mismatch")
    need(re.fullmatch(r"[1-9][0-9]+", i["system_identifier"]) is not None, "cluster identity")
    images = read_json(d / "images.json")
    need(re.fullmatch(r"sha256:[0-9a-f]{64}", images["built_id"]) is not None and
         images["built_platform"] == "linux/amd64", "built image metadata")
    for name, ref in IMAGES.items():
        m = images[name]
        need(m["ref"] == ref and m["os"] == "linux" and m["architecture"] == "amd64" and
             any(x.split("@")[-1] == ref.split("@")[-1] for x in m["digests"]), "image pin/platform mismatch")
    compiled = (d / "compiled-tests.txt").read_text().splitlines()
    need(len(compiled) == len(set(compiled)) and
         {n for n in compiled if n.startswith(PREFIX)} == PASS | SKIP and
         OFFLINE | PROBES | {AUTH} <= set(compiled), "compiled test inventory")
    offline = outcomes((d / "offline.log").read_text(), OFFLINE)
    need({k: v for k, v in offline.items() if "/" not in k} == dict.fromkeys(OFFLINE, "PASS"), "offline results")
    exits = read_json(d / "exit-codes.json")
    need(REQUIRED_EXITS <= exits.keys(), "missing exit codes")
    need(all(type(v) is int and v == (1 if k == "harness_probe" else 0) for k, v in exits.items()), "nonzero/invalid exit")
    need(exits.get("harness_probe") == 1, "missing harness exit")
    commands = read_json(d / "commands.json")
    need(isinstance(commands, list) and commands and
         all(c["exit"] == (1 if c["stage"] == "harness_probe" else 0) for c in commands), "failed command history")
    resources = read_json(d / "resources.json")
    need(all(resources[k]["mem_available"] >= 8 * 1024**3 and resources[k]["docker_disk_free"] >= 8 * 1024**3
             for k in ("resources_build", "resources_runtime")), "resource admission evidence")
    verify_harness((d / "harness.log").read_text(), exits["harness_probe"])
    expected = verify_logs((d / "tests.log").read_text())
    authlog = (d / "auth-hba.log").read_text()
    need("SQLSTATE=28P01" in authlog and "HBA file=" in authlog, "missing auth artifact")
    table = list(csv.DictReader((d / "test-results.csv").open(encoding="utf-8", newline="")))
    need(len(table) == 19 and {r["name"]: r["status"] for r in table} == expected, "results CSV")
    r = read_json(d / "docker-resources.json")
    need(set(r["containers"]) == {"pg", "tests"}, "runtime containers missing")
    for name, c in r["containers"].items():
        need(re.fullmatch(r"[0-9a-f]{64}", c["id"]) is not None and
             c["oom"] is False and type(c["exit"]) is int and c["exit"] == 0 and c["running"] is False and
             c["image"] == images["built_id"] and c["isolated"] is True and
             c["project"] == p["project"] and c["run"] == p["project"], "runtime/OOM/isolation")
    need(re.fullmatch(r"[0-9a-f]{64}", r["network"]["id"]) is not None and
         r["network"]["internal"] is True and r["network"]["project"] == p["project"], "runtime network")
    cleanup = read_json(d / "cleanup.json")
    need(cleanup["complete"] is True and cleanup["remaining"] == [] and
         set(cleanup["removed_containers"]) == {c["id"] for c in r["containers"].values()} and
         cleanup["removed_networks"] == [r["network"]["id"]], "cleanup incomplete")
    verdict = {"status": "PASS", "integration_pass": 17, "fail": 0, "not_run": 0, "child_skip": 2,
               "source_sha": SOURCE, "workflow_sha": p["workflow_sha"]}
    if inventory:
        need(read_json(d / "summary.json") == verdict, "summary disagrees with actual evidence")
        entries = (d / "SHA256SUMS").read_text().splitlines()
        pairs = [line.split("  ", 1) for line in entries]
        names = [v[1] for v in pairs]
        need(len(names) == len(set(names)) and set(names) == {x.name for x in d.iterdir() if x.name != "SHA256SUMS"}, "inventory membership")
        for sha, name in pairs:
            need(Path(name).name == name and digest((d / name).read_bytes()) == sha, "inventory checksum")
    return verdict


class Controller:
    """Private state survives a failed/timeout run for the always() cleanup step."""
    def __init__(self, source):
        need(platform.system() == "Linux" and platform.machine() == "x86_64", "Linux x64 required")
        for k, v in {"GITHUB_ACTIONS": "true", "GITHUB_EVENT_NAME": "push",
                     "GITHUB_REPOSITORY": "adamgrff/TREMOR_SCLAD",
                     "GITHUB_REF": "refs/heads/ci/e1a-fixed-21fa6cd"}.items():
            need(os.environ.get(k) == v, "GitHub admission")
        self.source = Path(source).resolve()
        root = Path(os.environ["RUNNER_TEMP"]).resolve()
        self.private = root / "e1a-private"
        self.public = root / "e1a-evidence"
        need(os.environ["PRIVATE_DIR"] == str(self.private) and
             os.environ["EVIDENCE_DIR"] == str(self.public), "evidence paths")
        need(not self.private.is_symlink() and not self.public.is_symlink(), "unsafe evidence directory")
        need(os.environ["SOURCE_SHA"] == SOURCE and os.environ["SOURCE_TREE"] == TREE and
             os.environ["WORKFLOW_SHA"] == os.environ["GITHUB_SHA"], "workflow/source environment identity")
        self.state = {"exits": {}, "commands": [], "ids": {}, "project": ""}
        self.env = {k: v for k, v in os.environ.items() if not
                    (k.startswith(("PG", "MIGRATION_TEST_", "EXTENSION_TEST_", "E1A_", "COMPOSE_", "DOCKER_")) or k == "DATABASE_URL")}
        self.password = ""

    def save(self):
        write_json(self.private / "state.json", self.state)

    def command(self, key, args, timeout=60, check=True, env=None):
        log = self.private / (key + ".log")
        with log.open("wb") as out:
            proc = subprocess.Popen(args, stdout=out, stderr=subprocess.STDOUT,
                                    env=env or self.env, start_new_session=True)
            try:
                rc = proc.wait(timeout=timeout)
            except subprocess.TimeoutExpired:
                os.killpg(proc.pid, signal.SIGTERM)
                try:
                    proc.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    os.killpg(proc.pid, signal.SIGKILL)
                    proc.wait()
                rc = 124
        self.state["exits"][key] = rc
        self.state["commands"].append({"stage": key, "exit": rc})
        self.save()
        if check:
            need(rc == 0, "stage failed: " + key)
        return rc, log.read_text(encoding="utf-8", errors="replace")

    def json_command(self, key, args):
        return json.loads(self.command(key, args)[1])

    def compose(self, *args):
        return ["docker", "compose", "--env-file", "/dev/null", "--project-name", self.state["project"],
                "--file", str(self.source / "backend/test/e1a/compose.yaml"), *args]

    def resources(self, stage):
        info = Path("/proc/meminfo").read_text()
        available = int(re.search(r"^MemAvailable:\s+(\d+) kB$", info, re.M)[1]) * 1024
        docker_root = self.command(stage + "_docker_root", ["docker", "info", "--format", "{{.DockerRootDir}}"])[1].strip()
        free = shutil.disk_usage(docker_root).free
        need(free >= 8 * 1024**3 and available >= 8 * 1024**3, "RESOURCE_NOT_READY")
        self.state["exits"][stage] = 0
        self.state[stage] = {"mem_available": available, "docker_disk_free": free}
        self.save()

    def source_check(self):
        head = self.command("source_head", ["git", "-C", str(self.source), "rev-parse", "HEAD"])[1].strip()
        tree = self.command("source_tree", ["git", "-C", str(self.source), "rev-parse", "HEAD^{tree}"])[1].strip()
        status = self.command("source_status", ["git", "-C", str(self.source), "status", "--porcelain"])[1].strip()
        verify_source_identity(head, tree, status)
        control = Path(__file__).resolve().parents[2]
        workflow = os.environ["WORKFLOW_SHA"]
        need(re.fullmatch(r"[0-9a-f]{40}", workflow) is not None and workflow != SOURCE, "workflow SHA")
        need(self.command("control_head", ["git", "-C", str(control), "rev-parse", "HEAD"])[1].strip() == workflow, "control SHA")
        hotfix = self.command("control_parent", ["git", "-C", str(control), "rev-list", "--parents", "-n", "1", "HEAD"])[1].strip()
        initial = self.command("control_grandparent", ["git", "-C", str(control), "rev-list", "--parents", "-n", "1", INITIAL_CONTROL])[1].strip()
        verify_control_ancestry(workflow, hotfix, initial)
        need(not self.command("control_status", ["git", "-C", str(control), "status", "--porcelain"])[1].strip(), "dirty control")
        changed = self.command("control_diff", ["git", "-C", str(control), "diff", "--name-status", SOURCE, "HEAD"])[1].splitlines()
        need({tuple(x.split("\t")) for x in changed} == {("A", p) for p in CONTROL_PATHS}, "control scope")
        manifest = read_json(self.source / "backend/migrations/manifest.clean-v2.json")
        need(len(manifest["migrations"]) == 22, "baseline count")
        for m in manifest["migrations"]:
            b = (self.source / "backend/migrations" / m["name"]).read_bytes().replace(b"\r\n", b"\n")
            need(digest(b) == m["sha256"], "baseline checksum")
        entries = self.command("source_entries", ["git", "-C", str(self.source), "ls-tree", "-r", "HEAD"])[1].splitlines()
        with (self.private / "source-manifest.csv").open("w", encoding="utf-8", newline="") as f:
            writer = csv.writer(f); writer.writerow(["path", "blob", "sha256"])
            for entry in entries:
                meta, path = entry.split("\t")
                mode, kind, blob = meta.split()
                need(kind == "blob" and mode == "100644", "unexpected source entry")
                writer.writerow([path, blob, digest((self.source / path).read_bytes())])
        write_json(self.private / "provenance.json", {
            "source_sha": SOURCE, "source_tree": TREE, "workflow_sha": workflow,
            "repository": "adamgrff/TREMOR_SCLAD", "project": self.state["project"],
            "run_id": os.environ["GITHUB_RUN_ID"], "attempt": os.environ["GITHUB_RUN_ATTEMPT"],
            "runner_image": os.environ.get("ImageVersion", "unknown"), "baseline_verified": 22})
        self.state["exits"]["source"] = 0; self.save()

    def run(self):
        self.private.mkdir(mode=0o700)  # Refuse reruns, including a second suite.
        self.public.mkdir(mode=0o700)
        self.state["project"] = "e1a-gha-" + secrets.token_hex(12)
        self.save()
        for tool in ("git", "docker", "python3"):
            need(shutil.which(tool), "missing tool: " + tool)
        self.command("tools", ["docker", "version"])
        self.command("compose_version", ["docker", "compose", "version"])
        need(self.command("engine_platform", ["docker", "info", "--format", "{{.OSType}}/{{.Architecture}}"])[1].strip() in ("linux/x86_64", "linux/amd64"), "Docker platform")
        self.source_check(); self.resources("resources_build")
        need(not self.discover("container") and not self.discover("network") and
             not self.discover("volume"), "project collision")
        need(not self.command("image_collision", ["docker", "image", "ls", "--quiet", "--no-trunc",
                 "tremor-e1a:" + self.state["project"]])[1].strip(), "image tag collision")
        self.state["admitted"] = True; self.save()
        self.password = secrets.token_hex(32)
        (self.private / "password").write_text(self.password, encoding="ascii")
        os.chmod(self.private / "password", 0o600)
        print("::add-mask::" + self.password, flush=True)
        self.configure()
        images = {}
        for name, ref in IMAGES.items():
            self.command("pull_" + name, ["docker", "pull", "--platform", "linux/amd64", ref], timeout=300)
            m = self.json_command("image_" + name, ["docker", "image", "inspect", ref])[0]
            need(m["Os"] == "linux" and m["Architecture"] == "amd64" and
                 any(x.endswith("@" + ref.split("@")[1]) for x in m["RepoDigests"]), "base image mismatch")
            images[name] = {"ref": ref, "os": m["Os"], "architecture": m["Architecture"], "digests": m["RepoDigests"], "id": m["Id"]}
        self.command("compose_config", self.compose("config", "--quiet"))
        self.validate_compose()
        self.source_check()
        self.command("build", self.compose("build", "pg"), timeout=900)
        self.source_check()
        built = self.json_command("built_image", ["docker", "image", "inspect", self.env["E1A_TEST_IMAGE"]])[0]
        self.state["image"] = images["built_id"] = built["Id"]
        images["built_platform"] = built["Os"] + "/" + built["Architecture"]
        need(images["built_platform"] == "linux/amd64", "built platform")
        write_json(self.private / "images.json", images); self.save()
        self.resources("resources_runtime")
        self.command("start_pg", self.compose("up", "--detach", "--no-build", "--pull", "never", "--wait", "--wait-timeout", "90", "pg"), timeout=120)
        pg = self.command("pg_id", self.compose("ps", "--all", "--quiet", "pg"))[1].strip()
        need(re.fullmatch(r"[0-9a-f]{64}", pg) is not None, "PG container ID")
        self.state["ids"]["pg"] = pg; self.save()
        _, details = self.container(pg)
        need(details["isolated"], "PG runtime isolation")
        nets = self.discover("network")
        need(len(nets) == 1, "runtime network count")
        network = self.json_command("admit_network", ["docker", "network", "inspect", nets[0]])[0]
        need(network["Internal"] is True and network["Labels"].get("tremor.e1a.run") == self.state["project"], "runtime network isolation")
        self.identity(pg)
        _, names = self.command("compiled", ["docker", "exec", pg, "/opt/e1a/migrations.test", "-test.list=^TestExtension"])
        (self.private / "compiled-tests.txt").write_text(names, encoding="utf-8")
        listing = names.splitlines()
        need(len(listing) == len(set(listing)) and {n for n in listing if n.startswith(PREFIX)} == PASS | SKIP and
             OFFLINE | PROBES | {AUTH} <= set(listing), "packaging mismatch")
        regex = "^(" + "|".join(sorted(OFFLINE)) + ")$"
        _, offline = self.command("offline", ["docker", "exec", pg, "/opt/e1a/migrations.test", "-test.v", "-test.count=1", "-test.timeout=60s", "-test.run=" + regex], timeout=75)
        need({k: v for k, v in outcomes(offline, OFFLINE).items() if "/" not in k} == dict.fromkeys(OFFLINE, "PASS"), "offline incomplete")
        rc, harness = self.command("harness_probe", ["docker", "exec", "-e", "E1A_OFFLINE_HARNESS_CHILD=yes", pg,
            "/bin/bash", "/opt/e1a/run-suite.sh", "/opt/e1a/migrations.test", "/tmp/e1a-ci-harness", "^TestExtensionHarnessProbe"], check=False)
        verify_harness(harness, rc)
        (self.private / "harness.log").write_text(harness, encoding="utf-8")
        self.state["exits"]["harness"] = 0; self.save()
        self.command("suite", self.compose("up", "--no-build", "--pull", "never", "--no-recreate",
                     "--abort-on-container-exit", "--exit-code-from", "tests"), timeout=720)

    def configure(self):
        self.env.update(E1A_RUN_ID=self.state["project"], E1A_TEST_IMAGE="tremor-e1a:" + self.state["project"],
                        E1A_T1_APPROVED="yes", E1A_TEST_PASSWORD=self.password,
                        E1A_GO_IMAGE=IMAGES["go"], E1A_PG_IMAGE=IMAGES["pg"], PGPASSWORD=self.password)

    def validate_compose(self):
        config = self.json_command("compose_render", self.compose("config", "--format", "json"))
        need(set(config["services"]) == {"pg", "tests"} and set(config["networks"]) == {"isolated"} and
             config["networks"]["isolated"]["internal"] is True, "Compose topology")
        for name, service in config["services"].items():
            need(service["platform"] == "linux/amd64" and service["image"] == self.env["E1A_TEST_IMAGE"] and
                 not service.get("ports") and not service.get("volumes") and not service.get("privileged"), "Compose service isolation")
            validate_memory(service, name)
        pg, tests = config["services"]["pg"], config["services"]["tests"]
        need(Path(pg["build"]["context"]).resolve() == self.source / "backend" and
             pg["build"]["dockerfile"] == "test/e1a/Dockerfile" and
             pg["build"]["args"] == {"GO_IMAGE": IMAGES["go"], "PG_IMAGE": IMAGES["pg"]}, "build context")
        need(tests["network_mode"] == "service:pg" and tests["read_only"] is True and
             tests["environment"]["PGPASSWORD"] == self.password and
             pg["environment"]["POSTGRES_PASSWORD"] == self.password and
             pg["environment"]["POSTGRES_HOST_AUTH_METHOD"] == "scram-sha-256" and
             pg["command"] == ["postgres", "-c", "listen_addresses=127.0.0.1", "-c", "port=5432"], "Compose target")

    def discover(self, kind):
        args = {"container": ["docker", "ps", "--all", "--quiet", "--no-trunc"],
                "network": ["docker", "network", "ls", "--quiet", "--no-trunc"],
                "volume": ["docker", "volume", "ls", "--quiet"]}[kind]
        return self.command("discover_" + kind, args + ["--filter", "label=com.docker.compose.project=" + self.state["project"]])[1].splitlines()

    def identity(self, pg):
        sql = """SELECT json_build_object('database',current_database(),'user',current_user,
        'address',host(inet_server_addr()),'port',inet_server_port(),
        'version',current_setting('server_version_num')::int,'recovery',pg_is_in_recovery(),
        'system_identifier',(SELECT system_identifier::text FROM pg_control_system()),
        'encoding',pg_encoding_to_char(encoding),'provider',datlocprovider::text,
        'locale',datlocale,'schema',EXISTS(SELECT 1 FROM pg_namespace WHERE nspname='public'))
        FROM pg_database WHERE datname=current_database()"""
        _, output = self.command("identity", ["docker", "exec", "-e", "PGPASSWORD", pg, "psql", "-X", "-At",
            "-h", "127.0.0.1", "-p", "5432", "-U", "e1a_test_admin", "-d", "tremor_e1a_test",
            "-v", "ON_ERROR_STOP=1", "-c", sql])
        write_json(self.private / "postgres-identity.json", json.loads(output))
        identity = json.loads(output)
        need(identity["version"] == 180006 and identity["database"] == "tremor_e1a_test" and
             identity["user"] == "e1a_test_admin" and identity["address"] == "127.0.0.1" and
             identity["port"] == 5432 and identity["locale"] == "C.UTF-8" and identity["provider"] == "b" and
             identity["encoding"] == "UTF8" and identity["schema"] is True and identity["recovery"] is False,
             "pre-suite TEST identity")

    def container(self, ident):
        c = self.json_command("inspect_" + ident[:12], ["docker", "inspect", ident])[0]
        labels = c["Config"]["Labels"] or {}
        service = labels.get("com.docker.compose.service")
        need(c["Id"] == ident and labels.get("com.docker.compose.project") == self.state["project"] and
             labels.get("tremor.e1a.run") == self.state["project"] and service in ("pg", "tests") and
             c["Image"] == self.state.get("image"), "container ownership mismatch; refuse deletion")
        previous = self.state["ids"].get(service)
        need(previous in (None, ident), "unexpected second service container")
        self.state["ids"][service] = ident; self.save()
        h = c["HostConfig"]
        expected = {"pg": {"/var/lib/postgresql": "1536m", "/tmp": "128m"},
                    "tests": {"/var/lib/postgresql": "16m", "/tmp": "256m"}}[service]
        tmpfs = h.get("Tmpfs") or {}
        isolated = (not h.get("Privileged") and not h.get("Binds") and
                    all(m["Type"] == "tmpfs" and m["Destination"] in expected for m in c.get("Mounts", [])) and
                    not h.get("PortBindings") and set(tmpfs) == set(expected) and
                    all({"rw", "nosuid", "nodev", "size=" + size} <= set(tmpfs[path].split(",")) for path, size in expected.items()) and
                    h["Memory"] == (3 if service == "pg" else 1) * 1024**3 and h["MemorySwap"] == h["Memory"])
        if service == "tests":
            isolated = isolated and h["ReadonlyRootfs"] and h["NetworkMode"] == "container:" + self.state["ids"].get("pg", "") and "ALL" in h["CapDrop"] and any(v in ("no-new-privileges", "no-new-privileges:true") for v in h["SecurityOpt"])
        else:
            isolated = isolated and h["ShmSize"] == 256 * 1024**2 and set(c["NetworkSettings"]["Networks"]) == {self.state["project"] + "_isolated"}
        return service, {"id": ident, "image": c["Image"], "project": self.state["project"],
            "run": labels["tremor.e1a.run"], "oom": c["State"]["OOMKilled"],
            "exit": c["State"]["ExitCode"], "running": c["State"]["Running"], "isolated": bool(isolated)}

    def collect(self):
        self.private.mkdir(mode=0o700, exist_ok=True)
        self.public.mkdir(mode=0o700, exist_ok=True)
        saved = self.private / "state.json"
        if saved.is_file():
            self.state = read_json(saved)
        password = self.private / "password"
        if password.is_file():
            self.password = password.read_text(encoding="ascii")
            print("::add-mask::" + self.password, flush=True)
        cleanup = {"complete": False, "remaining": [], "removed_containers": [], "removed_networks": [], "errors": []}
        runtime = {"containers": {}, "network": {}}
        try:
            need(re.fullmatch(r"e1a-gha-[0-9a-f]{24}", self.state["project"]) is not None, "missing valid private run state")
            need(self.state.get("admitted") is True, "project was never admitted; refuse resource deletion")
            self.configure()
            # Discover partial creations too; validate both labels and exact image
            # before every deletion. No volume, image or global prune operation.
            for ident in sorted(self.discover("container"), key=lambda value: value == self.state["ids"].get("pg")):
                try:
                    service, details = self.container(ident)
                    runtime["containers"][service] = details
                    _, log = self.command("logs_" + service, ["docker", "logs", ident])
                    (self.private / ("tests.log" if service == "tests" else "runtime.log")).write_text(log, encoding="utf-8")
                    if details["running"]:
                        self.command("stop_" + service, ["docker", "stop", "--time", "15", ident], timeout=30)
                    self.container(ident)  # Revalidate ownership after stop.
                    self.command("remove_" + service, ["docker", "rm", ident])
                    cleanup["removed_containers"].append(ident)
                except (ValueError, OSError, KeyError) as exc:
                    cleanup["errors"].append(type(exc).__name__)
            for ident in self.discover("network"):
                try:
                    n = self.json_command("inspect_network", ["docker", "network", "inspect", ident])[0]
                    need(n["Id"] == ident and n["Name"] == self.state["project"] + "_isolated" and
                         n["Labels"].get("com.docker.compose.project") == self.state["project"] and
                         n["Labels"].get("tremor.e1a.run") == self.state["project"], "network ownership mismatch")
                    runtime["network"] = {"id": ident, "internal": n["Internal"], "project": self.state["project"]}
                    self.command("remove_network", ["docker", "network", "rm", ident])
                    cleanup["removed_networks"].append(ident)
                except (ValueError, OSError, KeyError) as exc:
                    cleanup["errors"].append(type(exc).__name__)
            cleanup["remaining"] = self.discover("container") + self.discover("network") + self.discover("volume")
            cleanup["complete"] = not cleanup["errors"] and not cleanup["remaining"]
        except (ValueError, OSError, KeyError) as exc:
            cleanup["errors"].append(type(exc).__name__)
        write_json(self.private / "docker-resources.json", runtime)
        write_json(self.private / "cleanup.json", cleanup)
        write_json(self.private / "exit-codes.json", self.state["exits"])
        write_json(self.private / "commands.json", self.state["commands"])
        write_json(self.private / "resources.json", {k: self.state.get(k, {}) for k in ("resources_build", "resources_runtime")})
        logpath = self.private / "tests.log"
        text = ""
        if logpath.is_file():
            text = logpath.read_text(encoding="utf-8")
            (self.private / "auth-hba.log").write_text("\n".join(x for x in text.splitlines() if "HBA file=" in x or "AUTH " in x or "AUTH_PREFLIGHT_EXIT=" in x) + "\n", encoding="utf-8")
        table = io.StringIO(); writer = csv.writer(table); writer.writerow(["name", "status"])
        observed = re.findall(r"^RESULT (\S+) (\S+)$", text, re.M)
        for status, name in observed:
            writer.writerow([name, status])
        for name in sorted((PASS | SKIP) - {name for _, name in observed}):
            writer.writerow([name, "NOT_RUN"])
        (self.private / "test-results.csv").write_text(table.getvalue(), encoding="utf-8")
        # Only explicitly selected files; never raw inspect, env, state/password.
        for name in PUBLIC:
            path = self.private / name
            if path.is_file():
                text = path.read_text(encoding="utf-8", errors="replace")
                text = redact(text, self.password)
                (self.public / name).write_text(text, encoding="utf-8")
        try:
            summary = verify_bundle(self.public, inventory=False)
        except (ValueError, OSError, KeyError, TypeError) as exc:
            summary = {"status": "FAIL/INCOMPLETE", "reason": str(exc), "source_sha": SOURCE}
        write_json(self.public / "summary.json", summary)
        lines = [digest(p.read_bytes()) + "  " + p.name for p in sorted(self.public.iterdir()) if p.name != "SHA256SUMS"]
        (self.public / "SHA256SUMS").write_text("\n".join(lines) + "\n", encoding="utf-8")
        if summary["status"] == "PASS":
            try:
                verify_bundle(self.public)  # Verify the final public inventory too.
            except (ValueError, OSError, KeyError, TypeError) as exc:
                summary = {"status": "FAIL/INCOMPLETE", "reason": str(exc), "source_sha": SOURCE}
                write_json(self.public / "summary.json", summary)
                lines = [digest(p.read_bytes()) + "  " + p.name for p in sorted(self.public.iterdir()) if p.name != "SHA256SUMS"]
                (self.public / "SHA256SUMS").write_text("\n".join(lines) + "\n", encoding="utf-8")
        if password.is_file():
            password.unlink()
        return 0 if summary["status"] == "PASS" else 1


def synthetic(directory):
    """Deliberately synthetic offline verifier input; never PostgreSQL evidence."""
    d = Path(directory)
    for name in PUBLIC - {"failure.log"}:
        (d / name).write_text("synthetic\n", encoding="utf-8")
    write_json(d / "provenance.json", {"source_sha": SOURCE, "source_tree": TREE,
        "workflow_sha": "a" * 40, "repository": "adamgrff/TREMOR_SCLAD", "baseline_verified": 22, "project": "synthetic"})
    f = io.StringIO(); w = csv.writer(f); w.writerow(["path", "blob", "sha256"])
    for index in range(312):
        w.writerow([str(index), "a" * 40, "b" * 64])
    (d / "source-manifest.csv").write_text(f.getvalue(), encoding="utf-8")
    write_json(d / "postgres-identity.json", {"database": "tremor_e1a_test", "user": "e1a_test_admin",
        "address": "127.0.0.1", "port": 5432, "version": 180006, "encoding": "UTF8", "provider": "b",
        "locale": "C.UTF-8", "recovery": False, "schema": True, "system_identifier": "1234567"})
    images = {k: {"ref": v, "os": "linux", "architecture": "amd64", "digests": [v]} for k, v in IMAGES.items()}
    images["built_id"] = "sha256:" + "c" * 64
    images["built_platform"] = "linux/amd64"
    write_json(d / "images.json", images)
    (d / "compiled-tests.txt").write_text("\n".join(sorted(PASS | SKIP | OFFLINE | PROBES | {AUTH})) + "\n")
    (d / "offline.log").write_text("\n".join("--- PASS: " + n + " (0.00s)" for n in sorted(OFFLINE)) + "\n")
    harness = "synthetic harness failure; no PostgreSQL\n--- FAIL: TestExtensionHarnessProbeA (0.00s)\nRESULT FAIL TestExtensionHarnessProbeA\nRESULT NOT_RUN TestExtensionHarnessProbeB\nINTEGRATION_EXIT=1 LOG_TEE_EXIT=0\nREPORT_AWK_EXIT=0 REPORT_SORT_EXIT=0\n"
    (d / "harness.log").write_text(harness)
    write_json(d / "exit-codes.json", {**dict.fromkeys(REQUIRED_EXITS, 0), "harness_probe": 1})
    write_json(d / "commands.json", [{"stage": "suite", "exit": 0}, {"stage": "harness_probe", "exit": 1}])
    write_json(d / "resources.json", {k: {"mem_available": 9 * 1024**3, "docker_disk_free": 9 * 1024**3}
                                       for k in ("resources_build", "resources_runtime")})
    rules = [{"rule_number": 2, "type": "host", "database": ["all"], "users": ["all"],
              "address": "127.0.0.1", "netmask": "255.255.255.255", "auth": "scram-sha-256", "error": None}]
    auth = "HBA file=/synthetic/pg_hba.conf rules=" + json.dumps(rules) + "\nAUTH PASS: correct password accepted; wrong password SQLSTATE=28P01; loopback SCRAM rule=2\n"
    text = auth + "\n".join("--- " + ("SKIP" if n in SKIP else "PASS") + ": " + n + " (0.00s)" for n in sorted(PASS | SKIP | SUBTESTS | {AUTH})) + "\n"
    text += "\n".join("RESULT " + ("SKIP" if n in SKIP else "PASS") + " " + n for n in sorted(PASS | SKIP)) + "\n"
    text += "AUTH_PREFLIGHT_EXIT=0\nINTEGRATION_EXIT=0 LOG_TEE_EXIT=0\nREPORT_AWK_EXIT=0 REPORT_SORT_EXIT=0\nobserved concurrent backends: holder=10 waiter=11 key=6075995045621812551; holder granted, waiter blocked\n"
    (d / "tests.log").write_text(text); (d / "auth-hba.log").write_text(auth)
    f = io.StringIO(); w = csv.writer(f); w.writerow(["name", "status"])
    w.writerows((n, "SKIP" if n in SKIP else "PASS") for n in sorted(PASS | SKIP))
    (d / "test-results.csv").write_text(f.getvalue())
    ids = {"pg": "1" * 64, "tests": "2" * 64}
    containers = {n: {"id": ids[n], "image": images["built_id"], "project": "synthetic", "run": "synthetic", "oom": False,
                       "exit": 0, "running": False, "isolated": True} for n in ("pg", "tests")}
    write_json(d / "docker-resources.json", {"containers": containers, "network": {"id": "3" * 64, "internal": True, "project": "synthetic"}})
    write_json(d / "cleanup.json", {"complete": True, "remaining": [], "removed_containers": list(ids.values()), "removed_networks": ["3" * 64]})


class VerifierTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(); self.addCleanup(self.temp.cleanup)
        self.d = Path(self.temp.name); synthetic(self.d)

    def check(self):
        return verify_bundle(self.d, inventory=False)

    def test_byte_limits(self):
        for name, size in (("pg", 3221225472), ("tests", 1073741824)):
            for memory in (size, str(size)):
                for swap in (size, str(size)):
                    with self.subTest(service=name, memory=memory, swap=swap):
                        self.assertEqual(strict_bytes(memory), size)
                        validate_memory({"mem_limit": memory, "memswap_limit": swap}, name)
            for field in ("mem_limit", "memswap_limit"):
                invalid = [True, False, float(size), None, -size, str(-size), "3g", "1 GiB",
                           str(size) + "x", " " + str(size), str(size) + "\n", "", "0" + str(size),
                           "٣٢٢١٢٢٥٤٧٢", size - 1, 0]
                for value in invalid:
                    with self.subTest(service=name, field=field, invalid=value):
                        service = {"mem_limit": size, "memswap_limit": size, field: value}
                        with self.assertRaises(ValueError):
                            validate_memory(service, name)
                service = {"mem_limit": size, "memswap_limit": size}
                del service[field]
                with self.assertRaises(ValueError):
                    validate_memory(service, name)

    def test_control_chain(self):
        workflow = "a" * 40
        hotfix = workflow + " " + INITIAL_CONTROL
        initial = INITIAL_CONTROL + " " + SOURCE
        verify_control_ancestry(workflow, hotfix, initial)
        for h, i in ((workflow + " " + SOURCE, initial),
                     (workflow + " " + "b" * 40, initial),
                     (hotfix, INITIAL_CONTROL + " " + "b" * 40),
                     (hotfix + " " + SOURCE, initial),
                     (hotfix, initial + " " + "b" * 40),
                     (workflow, initial), (hotfix, INITIAL_CONTROL),
                     (hotfix, "b" * 40 + " " + SOURCE)):
            with self.subTest(hotfix=h, initial=i), self.assertRaises(ValueError):
                verify_control_ancestry(workflow, h, i)

    def test_fixed_source_guard(self):
        verify_source_identity(SOURCE, TREE, "")
        for head, tree, status in (("b" * 40, TREE, ""), (SOURCE, "b" * 40, ""),
                                   (SOURCE, TREE, " M backend/internal/migrations/extension_runner.go")):
            with self.assertRaises(ValueError):
                verify_source_identity(head, tree, status)
        c = object.__new__(Controller); c.source = Path("/synthetic-source")
        with mock.patch.object(c, "command", side_effect=[(0, SOURCE), (0, TREE), (0, "dirty")]):
            with self.assertRaisesRegex(ValueError, "dirty source"):
                c.source_check()

    def test_compose_json_contract(self):
        c = object.__new__(Controller)
        c.source = Path("/synthetic-source").resolve()
        c.state = {"project": "e1a-offline-hotfix"}
        c.env = {"E1A_TEST_IMAGE": "tremor-e1a:offline"}; c.password = "offline-only"
        config = {"networks": {"isolated": {"internal": True}}, "services": {}}
        for name, size in (("pg", 3221225472), ("tests", 1073741824)):
            config["services"][name] = {"platform": "linux/amd64", "image": c.env["E1A_TEST_IMAGE"],
                "mem_limit": str(size), "memswap_limit": str(size)}
        pg, tests = config["services"]["pg"], config["services"]["tests"]
        pg.update(build={"context": str(c.source / "backend"), "dockerfile": "test/e1a/Dockerfile",
                         "args": {"GO_IMAGE": IMAGES["go"], "PG_IMAGE": IMAGES["pg"]}},
                  environment={"POSTGRES_PASSWORD": c.password, "POSTGRES_HOST_AUTH_METHOD": "scram-sha-256"},
                  command=["postgres", "-c", "listen_addresses=127.0.0.1", "-c", "port=5432"])
        tests.update(network_mode="service:pg", read_only=True, environment={"PGPASSWORD": c.password})
        # The former Python comparison rejects the exact UnitBytes JSON form.
        self.assertNotEqual(pg["mem_limit"], 3 * 1024**3)
        with mock.patch.object(c, "json_command", return_value=config):
            c.validate_compose()
        mutations = {
            "extra_service": lambda v: v["services"].update(extra={}),
            "external_network": lambda v: v["networks"]["isolated"].update(internal=False),
            "published_port": lambda v: v["services"]["pg"].update(ports=[{"published": 5432}]),
            "bind_mount": lambda v: v["services"]["pg"].update(volumes=[{"type": "bind"}]),
            "privileged": lambda v: v["services"]["pg"].update(privileged=True),
            "platform": lambda v: v["services"]["pg"].update(platform="linux/arm64"),
            "wrong_limit": lambda v: v["services"]["pg"].update(mem_limit="3221225471"),
            "swap_mismatch": lambda v: v["services"]["tests"].update(memswap_limit="3221225472"),
            "missing_swap": lambda v: v["services"]["pg"].pop("memswap_limit"),
            "network_mode": lambda v: v["services"]["tests"].update(network_mode="host"),
            "writable_tests": lambda v: v["services"]["tests"].update(read_only=False),
            "wrong_context": lambda v: v["services"]["pg"]["build"].update(context="/another-source"),
        }
        for name, mutation in mutations.items():
            altered = copy.deepcopy(config); mutation(altered)
            with self.subTest(mutation=name), mock.patch.object(c, "json_command", return_value=altered):
                with self.assertRaises(ValueError):
                    c.validate_compose()

    def edit_json(self, name, fn):
        p = self.d / name; value = read_json(p); fn(value); write_json(p, value)

    def replace(self, old, new):
        p = self.d / "tests.log"; p.write_text(p.read_text().replace(old, new))

    def test_complete_synthetic(self):
        self.assertEqual(self.check()["status"], "PASS")

    def test_negative_matrix(self):
        cases = {
            "missing_test": lambda: self.replace("--- PASS: " + PREFIX + "Resume (0.00s)\n", ""),
            "failed_test": lambda: self.replace("--- PASS: " + PREFIX + "Resume", "--- FAIL: " + PREFIX + "Resume"),
            "not_run": lambda: self.replace("RESULT PASS " + PREFIX + "Resume", "RESULT NOT_RUN " + PREFIX + "Resume"),
            "unexpected_skip": lambda: self.replace("--- PASS: " + PREFIX + "Resume", "--- SKIP: " + PREFIX + "Resume"),
            "duplicate_result": lambda: self.replace("RESULT PASS " + PREFIX + "Resume\n", ("RESULT PASS " + PREFIX + "Resume\n") * 2),
            "duplicate_outcome": lambda: self.replace("--- PASS: " + PREFIX + "Resume (0.00s)\n", ("--- PASS: " + PREFIX + "Resume (0.00s)\n") * 2),
            "unknown_test": lambda: self.replace("RESULT PASS " + PREFIX + "Resume", "RESULT PASS TestUnknown"),
            "wrong_source": lambda: self.edit_json("provenance.json", lambda v: v.update(source_sha="0" * 40)),
            "wrong_tree": lambda: self.edit_json("provenance.json", lambda v: v.update(source_tree="0" * 40)),
            "missing_auth": lambda: self.replace("AUTH_PREFLIGHT_EXIT=0", ""),
            "wrong_sqlstate": lambda: self.replace("SQLSTATE=28P01", "SQLSTATE=08006"),
            "missing_r4": lambda: self.replace("--- PASS: " + PREFIX + "CHECKLiteralRegression/ledger_profile_clean (0.00s)\n", ""),
            "missing_crash": lambda: self.replace("--- PASS: " + PREFIX + "HardCrash/after_commit (0.00s)\n", ""),
            "missing_contention": lambda: self.replace("holder granted, waiter blocked", "not observed"),
            "compose_zero_incomplete": lambda: self.replace("RESULT PASS " + PREFIX + "Resume\n", ""),
            "oom": lambda: self.edit_json("docker-resources.json", lambda v: v["containers"]["pg"].update(oom=True)),
            "cleanup_failure": lambda: self.edit_json("cleanup.json", lambda v: v.update(complete=False)),
            "missing_evidence": lambda: (self.d / "images.json").unlink(),
            "missing_exit": lambda: self.edit_json("exit-codes.json", lambda v: v.pop("build")),
            "bad_tee": lambda: self.replace("LOG_TEE_EXIT=0", "LOG_TEE_EXIT=1"),
            "bad_isolation": lambda: self.edit_json("docker-resources.json", lambda v: v["containers"]["tests"].update(isolated=False)),
            "missing_resource_gate": lambda: self.edit_json("resources.json", lambda v: v.pop("resources_runtime")),
            "prior_command_failure": lambda: self.edit_json("commands.json", lambda v: v.append({"stage": "build", "exit": 137})),
        }
        for name, mutation in cases.items():
            with self.subTest(name=name):
                synthetic(self.d); mutation()
                with self.assertRaises((ValueError, OSError, KeyError)):
                    self.check()
                print("expected FAIL: " + name)

    def test_negative_harness_contract(self):
        log = (self.d / "harness.log").read_text()
        verify_harness(log, 1)
        for bad, rc in ((log, 0), (log.replace("LOG_TEE_EXIT=0", "LOG_TEE_EXIT=1"), 1),
                        (log.replace("NOT_RUN", "PASS"), 1),
                        (log.replace("synthetic harness failure; no PostgreSQL", ""), 1)):
            with self.assertRaises(ValueError):
                verify_harness(bad, rc)

    def test_inventory(self):
        d = self.d
        write_json(d / "summary.json", self.check())
        (d / "SHA256SUMS").write_text("\n".join(digest(p.read_bytes()) + "  " + p.name for p in sorted(d.iterdir())) + "\n")
        verify_bundle(d)
        (d / "build.log").write_text("tampered")
        with self.assertRaises(ValueError):
            verify_bundle(d)

    def test_redaction(self):
        password = "disposable-" + secrets.token_hex(8)
        output = redact(password + " SCRAM-SHA-256$4096:salt$stored:server", password)
        self.assertNotIn(password, output)
        self.assertNotIn("SCRAM-SHA-256$", output)

    def test_partial_cleanup_without_docker(self):
        # Construct a controller without platform admission; mock ALL Docker
        # discovery/commands. This is an offline cleanup-state-machine test.
        c = object.__new__(Controller)
        c.private = self.d / "private"; c.public = self.d / "public"
        c.private.mkdir(); c.public.mkdir()
        c.password = ""; c.env = {}; c.state = {"project": "e1a-gha-" + "1" * 24,
            "exits": {}, "commands": [], "ids": {}, "admitted": True}
        write_json(c.private / "state.json", c.state)
        with mock.patch.object(c, "discover", return_value=[]) as discover:
            self.assertEqual(c.collect(), 1)  # No suite => INCOMPLETE, not PASS.
            self.assertGreater(discover.call_count, 0)
        self.assertTrue(read_json(c.public / "cleanup.json")["complete"])
        self.assertEqual(read_json(c.public / "summary.json")["status"], "FAIL/INCOMPLETE")
        c.state["admitted"] = False; write_json(c.private / "state.json", c.state)
        with mock.patch.object(c, "discover", side_effect=AssertionError("must not touch resources")):
            self.assertEqual(c.collect(), 1)
        self.assertFalse(read_json(c.public / "cleanup.json")["complete"])

    def test_foreign_container_refused(self):
        c = object.__new__(Controller)
        c.state = {"project": "own-project", "image": "own-image", "ids": {}}
        foreign = {"Id": "id", "Image": "own-image", "Config": {"Labels": {
            "com.docker.compose.project": "another-project", "tremor.e1a.run": "another-project",
            "com.docker.compose.service": "pg"}}}
        with mock.patch.object(c, "json_command", return_value=[foreign]):
            with self.assertRaisesRegex(ValueError, "ownership mismatch"):
                c.container("id")

    def test_second_run_refused_before_tools(self):
        c = object.__new__(Controller)
        c.private = self.d / "existing-private"; c.private.mkdir()
        with self.assertRaises(FileExistsError):
            c.run()  # Refuses before tool discovery, Docker or SQL.

    def test_successful_publication_without_docker(self):
        c = object.__new__(Controller)
        c.private = self.d / "private"; c.public = self.d / "public"
        c.private.mkdir(); c.public.mkdir(); synthetic(c.private)
        c.env = {}; c.password = ""
        project = "e1a-gha-" + "1" * 24
        resources = read_json(c.private / "docker-resources.json")
        network_id = resources["network"]["id"]
        for item in resources["containers"].values():
            item.update(project=project, run=project)
        provenance = read_json(c.private / "provenance.json")
        provenance["project"] = project; write_json(c.private / "provenance.json", provenance)
        c.state = {"project": project, "admitted": True, "image": "sha256:" + "c" * 64,
                   "ids": {k: v["id"] for k, v in resources["containers"].items()},
                   "exits": read_json(c.private / "exit-codes.json"),
                   "commands": read_json(c.private / "commands.json"),
                   **read_json(c.private / "resources.json")}
        write_json(c.private / "state.json", c.state)
        password = secrets.token_hex(32)
        (c.private / "password").write_text(password)
        (c.private / "build.log").write_text(password + " SCRAM-SHA-256$4096:salt$stored:server")
        (c.private / "inspect-private.log").write_text(password)
        tests = (c.private / "tests.log").read_text()
        seen = set()

        def discover(kind):
            if kind in seen:
                return []
            seen.add(kind)
            return {"container": list(c.state["ids"].values()), "network": [network_id], "volume": []}[kind]

        def container(ident):
            name = next(k for k, v in c.state["ids"].items() if v == ident)
            return name, resources["containers"][name]

        def command(key, _args):
            return 0, tests if key == "logs_tests" else "synthetic PostgreSQL log"

        network = {"Id": network_id, "Name": project + "_isolated", "Internal": True,
                   "Labels": {"com.docker.compose.project": project, "tremor.e1a.run": project}}
        with mock.patch.object(c, "discover", side_effect=discover), \
             mock.patch.object(c, "container", side_effect=container), \
             mock.patch.object(c, "command", side_effect=command), \
             mock.patch.object(c, "json_command", return_value=[network]), \
             mock.patch("sys.stdout", new=io.StringIO()):
            self.assertEqual(c.collect(), 0)
        self.assertEqual(verify_bundle(c.public)["status"], "PASS")
        self.assertFalse((c.public / "inspect-private.log").exists())
        self.assertFalse((c.private / "password").exists())
        for path in c.public.iterdir():
            self.assertNotIn(password, path.read_text())
            self.assertNotIn("SCRAM-SHA-256$", path.read_text())


def main():
    if len(sys.argv) == 2 and sys.argv[1] == "self-test":
        suite = unittest.defaultTestLoader.loadTestsFromTestCase(VerifierTests)
        return 0 if unittest.TextTestRunner(verbosity=2).run(suite).wasSuccessful() else 1
    need(len(sys.argv) == 3, "usage: verify.py {check|run|collect-cleanup} PATH")
    mode, path = sys.argv[1:]
    if mode == "check":
        print(json.dumps(verify_bundle(path))); return 0
    need(mode in ("run", "collect-cleanup"), "unknown mode")
    controller = Controller(path)
    if mode == "collect-cleanup":
        return controller.collect()
    try:
        controller.run()
        return 0
    except (ValueError, OSError, KeyError, TypeError) as exc:
        # Full failure text is private until redaction, never streamed to Actions.
        if controller.private.is_dir():
            (controller.private / "failure.log").write_text(str(exc) + "\n", encoding="utf-8")
        print("E1A run failed; collect-cleanup will retain sanitized diagnostics", file=sys.stderr)
        return 1


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (ValueError, OSError, KeyError, TypeError) as error:
        print("E1A incomplete: " + type(error).__name__, file=sys.stderr)
        sys.exit(1)
