#!/usr/bin/env python3
"""Deployment CLI. render/check are offline; only explicit apply/up/bootstrap mutate systems."""
import argparse
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import time
import urllib.request

from render import ROOT, bootstrap, compose, kubernetes, load, local_path

HERE = Path(__file__).resolve().parent


def run(args, **kwargs):
    # Never invoke a shell: user configuration is data, never executable text.
    return subprocess.run([str(x) for x in args], check=True, **kwargs)


def save(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n")
    return path


def tool(name):
    found = shutil.which(name)
    bundled = ROOT.parent / ".tools" / "bin" / name
    if found:
        return found
    if bundled.is_file():
        return str(bundled)
    raise ValueError(f"missing command: {name}")


def kube(c):
    context = c.get("context", "")
    if not context or "REPLACE_" in context:
        raise ValueError("set context to the deployment operator's target kubeconfig context")
    return [tool("kubectl"), "--context", context]


def no_placeholder(value):
    text = json.dumps(value)
    if "REPLACE_" in text or "registry.example.com" in text:
        raise ValueError("replace REPLACE_* and registry.example.com placeholders before deployment")


def check_files(c, target):
    """Check mount sources without reading or printing authentication contents."""
    if target == "node":
        paths = [c["lldp"]["kubeconfig"]]
    else:
        paths = [c["topologyFile"]]
        if target == "external":
            paths.append(c["external"]["prcKubeconfig"])
            paths.extend(c["prometheus"][k] for k in ("tokenFile", "caFile") if c["prometheus"][k])
        if c["prometheus"]["metricsConfigFile"]:
            paths.append(c["prometheus"]["metricsConfigFile"])
    for path in paths:
        p = local_path(c, path)
        if not p.is_file():
            raise ValueError(f"mount source must be an existing regular file: {p}")
    # UID 65532 in PRC/Algorithm must have read access; document chmod/chgrp setup.


def render(c, target, directory):
    if target == "kubernetes":
        save(directory / "bootstrap.json", bootstrap(c))
        return save(directory / "kubernetes.json", kubernetes(c))
    name = "node.compose.json" if target == "node" else "compose.json"
    value = compose(c, node=target == "node")
    # Compose interpolates dollar signs, even inside JSON string values.
    value = json.loads(json.dumps(value).replace("$", "$$"))
    return save(directory / name, value)


def install_apis(c, external, directory):
    client = kube(c)
    manifest = bootstrap(c, external=external)
    no_placeholder(manifest)
    path = save(directory / "bootstrap.json", manifest)
    crds = ROOT / "docs/paas-schedbridge-master-new/crd-deploy"
    for resource in ("nodegroupdemand", "nodegroupgrant"):
        run(client + ["apply", "-f", crds / f"{resource}-crd.yaml"])
    for resource in ("nodegroupdemands", "nodegroupgrants"):
        run(client + ["wait", "--for=condition=Established", f"crd/{resource}.scheduling.platform.example.io", "--timeout=60s"])
    run(client + ["apply", "-f", path])


def require_secrets(c):
    names = set(c["kubernetes"]["imagePullSecrets"])
    names.update(a["secretName"] for a in (c["kubernetes"]["prcAuth"], c["kubernetes"]["lldpAuth"]) if a["mode"] == "kubeconfig")
    if c["prometheus"]["tokenFile"] or c["prometheus"]["caFile"]:
        names.add(c["prometheus"]["secretName"])
    for name in sorted(names):
        run(kube(c) + ["-n", c["namespace"], "get", "secret", name, "-o", "name"])


def verify_external(c, wait=0):
    address = c["external"]["bindAddress"]
    address = {"0.0.0.0": "127.0.0.1", "::": "::1"}.get(address, address)
    host = f"[{address}]" if ":" in address else address
    for key, path in (("algorithmPort", "/healthz"), ("prcPort", "/healthz"),
                      ("algorithmPort", "/internal/v1/cache/status")):
        url = f"http://{host}:{c['external'][key]}{path}"
        deadline = time.monotonic() + wait
        while True:
            try:
                with urllib.request.urlopen(url, timeout=5) as response:
                    print(key, path, response.read().decode())
                break
            except OSError:
                if time.monotonic() >= deadline:
                    raise
                time.sleep(1)


def main():
    parser = argparse.ArgumentParser(description="NGD-NGG deployment: edit one JSON config, render and deploy")
    parser.add_argument("target", choices=["init", "images", "kubernetes", "external", "node"])
    parser.add_argument("action", nargs="?", choices=["render", "check", "bootstrap", "up", "status", "logs", "down", "build", "push"], default="render")
    parser.add_argument("--config", type=Path, default=HERE / "config.local.json")
    parser.add_argument("--node-name", help="override lldp.nodeName on a Worker")
    args = parser.parse_args()
    if args.target == "init":
        # Refuse to overwrite an operator's existing configuration.
        with args.config.open("x") as out:
            out.write((HERE / "config.example.json").read_text())
        print(f"Created {args.config}. Edit images, topology, Prometheus and deployment credentials.")
        return
    c = load(args.config)
    if args.node_name:
        c["lldp"]["nodeName"] = args.node_name
    # Each config has its own output directory; configurations cannot overwrite one another.
    directory = args.config.resolve().parent / "generated" / args.config.stem / args.target
    if args.target == "images":
        if args.action not in ("build", "push"):
            raise ValueError("images supports build or push")
        no_placeholder(c["images"])
        if args.action == "push":
            for image in c["images"].values():
                run([tool("docker"), "push", image])
        else:
            for component, script, envkey in (("prc", "04-build-prc.sh", "PRC_IMAGE"),
                                             ("algorithm", "04b-build-algorithm.sh", "ALGORITHM_IMAGE"),
                                             ("lldp", "04d-build-lldp-agent.sh", "LLDP_AGENT_IMAGE")):
                env = dict(os.environ, **{envkey: c["images"][component]})
                run(["bash", ROOT / "scripts" / script], env=env)
        return
    if args.action == "bootstrap":
        if args.target == "node":
            raise ValueError("bootstrap runs once on the management server, not on Workers")
        install_apis(c, args.target == "external", directory)
        return
    path = render(c, args.target, directory)
    print(f"Generated: {path}", flush=True)
    if args.action == "render":
        return
    if args.action == "check":
        check_files(c, args.target)
        if args.target != "kubernetes":
            run([tool("docker"), "compose", "-f", path, "config", "--quiet"])
        print("Configuration and referenced files checked; no services started.")
        return
    if args.target == "kubernetes":
        client = kube(c)
        if args.action == "up":
            check_files(c, args.target)
            no_placeholder(kubernetes(c))
            require_secrets(c)
            # Schema/admission validation before applying any workload.
            run(client + ["apply", "--dry-run=server", "-f", path])
            run(client + ["apply", "-f", path])
            for workload in ("daemonset/lldp-agent", "deployment/ngd-ngg-algorithm", "deployment/prc"):
                run(client + ["-n", c["namespace"], "rollout", "status", workload, "--timeout=300s"])
        elif args.action == "status":
            run(client + ["-n", c["namespace"], "get", "deploy,ds,pod,svc", "-o", "wide"])
        elif args.action == "logs":
            for workload in ("daemonset/lldp-agent", "deployment/ngd-ngg-algorithm", "deployment/prc"):
                run(client + ["-n", c["namespace"], "logs", workload, "--tail=100"])
        elif args.action == "down":
            # Deliberately leaves CRDs, NGD/NGG, namespace, credentials and RBAC intact.
            run(client + ["delete", "-f", path, "--ignore-not-found"])
        else:
            raise ValueError("unsupported Kubernetes action")
    else:
        command = [tool("docker"), "compose", "-f", path]
        if args.action == "up":
            check_files(c, args.target)
            no_placeholder(compose(c, args.target == "node"))
            run(command + ["config", "--quiet"])
            run(command + ["up", "-d", "--force-recreate", "--wait", "--wait-timeout", "300"])
            if args.target == "external":
                verify_external(c, wait=30)
                print("PRC process started. Use logs and an NGD→NGG request to verify business readiness.")
        elif args.action == "status":
            run(command + ["ps"])
            if args.target == "external":
                verify_external(c)
        elif args.action == "logs":
            run(command + ["logs", "--tail=100"])
        elif args.action == "down":
            run(command + ["down"])
        else:
            raise ValueError("unsupported Compose action")


if __name__ == "__main__":
    try:
        main()
    except (ValueError, KeyError, OSError, subprocess.CalledProcessError) as error:
        print(f"ERROR: {error}", file=sys.stderr)
        sys.exit(1)
