"""Generate Kubernetes/Compose documents from one non-secret JSON config.

Only standard-library dependencies. JSON is accepted by kubectl and Compose.
No credentials are embedded in generated documents; only references/paths.
"""
import copy
import hashlib
import ipaddress
import json
from pathlib import Path
import re

ROOT = Path(__file__).resolve().parent.parent
GROUP = "scheduling.platform.example.io"


def load(path):
    path = Path(path).resolve()
    config = json.loads(path.read_text())
    config["_base"] = path.parent
    if not re.fullmatch(r"[a-z0-9](?:[-a-z0-9]{0,61}[a-z0-9])?", config["namespace"]):
        raise ValueError("namespace must be a DNS label of at most 63 characters")
    for name in ("prc", "algorithm", "lldp"):
        if not config["images"].get(name):
            raise ValueError(f"images.{name} is required")
    # Keep older local configs usable while making both values explicit in the
    # distributed example and every newly initialized config.local.json.
    prc = config.setdefault("prc", {})
    prc.setdefault("demandRefreshSeconds", 15)
    prc.setdefault("maxConcurrentRefreshes", 5)
    for key in ("demandRefreshSeconds", "maxConcurrentRefreshes"):
        if isinstance(prc[key], bool) or not isinstance(prc[key], int) or prc[key] <= 0:
            raise ValueError(f"prc.{key} must be a positive integer")
    for name in ("prcAuth", "lldpAuth"):
        auth = config["kubernetes"][name]
        if auth["mode"] not in ("incluster", "kubeconfig"):
            raise ValueError(f"{name}.mode must be incluster or kubeconfig")
        if auth["mode"] == "kubeconfig" and not auth["secretName"]:
            raise ValueError(f"{name}.secretName is required for kubeconfig mode")
    for key in ("refreshSeconds", "staleSeconds", "timeoutSeconds"):
        if config["prometheus"][key] <= 0:
            raise ValueError(f"prometheus.{key} must be positive")
    ipaddress.ip_address(config["external"]["bindAddress"])
    for key in ("algorithmPort", "prcPort"):
        if not 1 <= config["external"][key] <= 65535:
            raise ValueError(f"external.{key} must be a port")
    if config["external"]["algorithmPort"] == config["external"]["prcPort"]:
        raise ValueError("external ports must be distinct")
    lldp = config["lldp"]
    # Keep older private config.local.json files readable while emitting only
    # the lldp-new-3-compatible command-line flags.
    lldp.setdefault("timeoutSeconds", lldp.get("listenSeconds", 65))
    lldp.setdefault("intervalSeconds", lldp.get("resyncSeconds", 180))
    lldp.setdefault("interfaces", "bond0")
    for key in ("timeoutSeconds", "intervalSeconds"):
        if lldp[key] <= 0:
            raise ValueError(f"lldp.{key} must be positive")
    return config


def local_path(c, path):
    p = Path(path).expanduser()
    return p.resolve() if p.is_absolute() else (c["_base"] / p).resolve()


def obj(kind, name, ns=None, **fields):
    versions = {"Deployment": "apps/v1", "DaemonSet": "apps/v1",
                "ClusterRole": "rbac.authorization.k8s.io/v1",
                "ClusterRoleBinding": "rbac.authorization.k8s.io/v1"}
    metadata = {"name": name}
    if ns:
        metadata["namespace"] = ns
    return {"apiVersion": versions.get(kind, "v1"), "kind": kind,
            "metadata": metadata, **fields}


def auth_subject(auth, component, ns):
    if auth["mode"] == "incluster":
        return {"kind": "ServiceAccount", "name": component, "namespace": ns}
    value = copy.deepcopy(auth.get("subject"))
    if not value or value.get("kind") not in ("User", "Group", "ServiceAccount") or not value.get("name"):
        raise ValueError(f"{component}: specify the actual kubeconfig authenticated subject")
    if value["kind"] == "ServiceAccount":
        if not value.get("namespace"):
            raise ValueError("ServiceAccount subject needs namespace")
    else:
        value["apiGroup"] = "rbac.authorization.k8s.io"
    return value


def bootstrap(c, external=False):
    ns = c["namespace"]
    docs = [obj("Namespace", ns)]
    for name, authkey in (("prc", "prcAuth"), ("lldp-agent", "lldpAuth")):
        auth = c["kubernetes"][authkey]
        if external:
            key = "prcSubject" if name == "prc" else "lldpSubject"
            auth = {"mode": "kubeconfig", "subject": c["external"][key]}
        subject = auth_subject(auth, name, ns)
        if not external:
            docs.append(obj("ServiceAccount", name, ns))
        if name == "prc":
            rules = [
                {"apiGroups": [""], "resources": ["nodes", "pods"], "verbs": ["get", "list", "watch"]},
                {"apiGroups": [GROUP], "resources": ["nodegroupdemands"], "verbs": ["get", "list", "watch"]},
                {"apiGroups": [GROUP], "resources": ["nodegroupdemands/status"], "verbs": ["get", "update", "patch"]},
                {"apiGroups": [GROUP], "resources": ["nodegroupgrants"], "verbs": ["get", "list", "watch", "create", "update", "patch", "delete"]},
                {"apiGroups": [GROUP], "resources": ["nodegroupgrants/status"], "verbs": ["get", "update", "patch"]},
                {"apiGroups": ["coordination.k8s.io"], "resources": ["leases"], "verbs": ["get", "list", "watch", "create", "update", "patch"]},
            ]
        else:
            rules = [{"apiGroups": [""], "resources": ["nodes"], "verbs": ["get", "patch"]}]
        role = f"{ns}-{name}"
        docs.append(obj("ClusterRole", role, rules=rules))
        docs.append(obj("ClusterRoleBinding", role, roleRef={
            "apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": role}, subjects=[subject]))
    return {"apiVersion": "v1", "kind": "List", "items": docs}


def envlist(env):
    return [{"name": k, "value": str(v)} for k, v in env.items()]


def prometheus_env(c, kubernetes=False):
    p = c["prometheus"]
    env = {"PROMETHEUS_URL": p["url"], "PROMETHEUS_REFRESH_SECONDS": str(p["refreshSeconds"]),
           "PROMETHEUS_STALE_SECONDS": str(p["staleSeconds"]),
           "PROMETHEUS_REQUEST_TIMEOUT_SECONDS": str(p["timeoutSeconds"]),
           "PROMETHEUS_NODE_LABEL": p["nodeLabel"], "PROMETHEUS_TLS_SERVER_NAME": p["tlsServerName"],
           "TOPOLOGY_CONFIG_FILE": "/etc/ngd-ngg/topology/topology.yaml"}
    # In Kubernetes, secretName refers to a Secret with optional token/ca.crt keys.
    for key, var, dest in (("tokenFile", "PROMETHEUS_BEARER_TOKEN_FILE", "token"),
                           ("caFile", "PROMETHEUS_CA_FILE", "ca.crt")):
        if p[key]:
            if kubernetes and not p["secretName"]:
                raise ValueError("prometheus.secretName is required when tokenFile/caFile is enabled")
            env[var] = f"/etc/ngd-ngg/prometheus/{dest}"
    if p["metricsConfigFile"]:
        env["PROMETHEUS_METRICS_CONFIG_FILE"] = "/etc/ngd-ngg/metrics/metrics.json"
    return env


def lldp_args(c, sysfs):
    l = c["lldp"]
    return [f"--timeout={l['timeoutSeconds']}s", f"--count={l.get('count', 0)}",
            f"--interval={l['intervalSeconds']}s",
            f"--sys-class-net={sysfs}", f"--interfaces={l['interfaces']}"]


def prc_args(c, leader_election):
    p = c["prc"]
    return [f"--leader-elect={str(leader_election).lower()}", "--health-probe-bind-address=:8081",
            f"--demand-refresh-interval={p['demandRefreshSeconds']}s",
            f"--max-concurrent-refreshes={p['maxConcurrentRefreshes']}"]


def k8s_auth(pod, container, auth):
    pod["automountServiceAccountToken"] = auth["mode"] == "incluster"
    if auth["mode"] == "kubeconfig":
        container["env"].append({"name": "KUBECONFIG", "value": "/etc/ngd-ngg/auth/kubeconfig"})
        container.setdefault("volumeMounts", []).append({"name": "kubeconfig", "mountPath": "/etc/ngd-ngg/auth", "readOnly": True})
        pod.setdefault("volumes", []).append({"name": "kubeconfig", "secret": {
            "secretName": auth["secretName"], "defaultMode": 0o440,
            "items": [{"key": "kubeconfig", "path": "kubeconfig"}]}})


def kubernetes(c):
    """Workloads only: bootstrap installs CRDs and RBAC separately."""
    ns = c["namespace"]
    topology = local_path(c, c["topologyFile"]).read_text()
    data = {"topology.yaml": topology}
    metrics = c["prometheus"]["metricsConfigFile"]
    if metrics:
        data["metrics.json"] = local_path(c, metrics).read_text()
    digest = hashlib.sha256(json.dumps(data, sort_keys=True).encode()).hexdigest()
    docs = [obj("ConfigMap", "ngd-ngg-algorithm-topology", ns, data=data)]
    docs.append(obj("Service", "ngd-ngg-algorithm", ns, spec={
        "selector": {"app": "ngd-ngg-algorithm"},
        "ports": [{"name": "http", "port": 8080, "targetPort": 8080}]}))
    for component, name in (("algorithm", "ngd-ngg-algorithm"), ("prc", "prc"), ("lldp", "lldp-agent")):
        labels = {"app": "ngd-ngg-lldp-agent" if component == "lldp" else "ngd-ngg-" + component}
        container = {"name": component, "image": c["images"][component], "imagePullPolicy": "IfNotPresent",
                     "env": [], "resources": c["kubernetes"]["resources"][component],
                     "securityContext": {"allowPrivilegeEscalation": False, "capabilities": {"drop": ["ALL"]}}}
        pod = {"containers": [container], "securityContext": {"runAsUser": 65532, "runAsGroup": 65532, "fsGroup": 65532},
               "automountServiceAccountToken": False,
               "imagePullSecrets": [{"name": x} for x in c["kubernetes"]["imagePullSecrets"]]}
        template = {"metadata": {"labels": labels}, "spec": pod}
        kind = "Deployment"
        if component == "algorithm":
            container["env"] = envlist(prometheus_env(c, True))
            template["metadata"]["annotations"] = {"ngd-ngg/config-hash": digest}
            pod["volumes"] = [{"name": "topology", "configMap": {"name": "ngd-ngg-algorithm-topology"}}]
            container["volumeMounts"] = [{"name": "topology", "mountPath": "/etc/ngd-ngg/topology", "readOnly": True}]
            if metrics:
                container["volumeMounts"].append({"name": "topology", "mountPath": "/etc/ngd-ngg/metrics", "readOnly": True})
            p = c["prometheus"]
            if p["tokenFile"] or p["caFile"]:
                keys = [dest for key, dest in (("tokenFile", "token"), ("caFile", "ca.crt")) if p[key]]
                pod["volumes"].append({"name": "prometheus-auth", "secret": {"secretName": p["secretName"],
                    "defaultMode": 0o440, "items": [{"key": k, "path": k} for k in keys]}})
                container["volumeMounts"].append({"name": "prometheus-auth", "mountPath": "/etc/ngd-ngg/prometheus", "readOnly": True})
        elif component == "prc":
            auth = c["kubernetes"]["prcAuth"]
            pod["serviceAccountName"] = "prc"
            # Without projected SA namespace, current Manager cannot discover Lease namespace.
            container["args"] = prc_args(c, auth["mode"] == "incluster")
            container["env"] = envlist({"CLUSTER_ID": c["clusterId"], "ALGORITHM_URL": f"http://ngd-ngg-algorithm.{ns}.svc:8080"})
            k8s_auth(pod, container, auth)
        else:
            kind = "DaemonSet"
            pod["serviceAccountName"] = "lldp-agent"
            pod.update(hostNetwork=True, dnsPolicy="ClusterFirstWithHostNet",
                       nodeSelector=c["kubernetes"]["workerNodeSelector"], tolerations=c["kubernetes"]["workerTolerations"])
            # Whole sysfs preserves class/net symlinks to ../../devices.
            pod["volumes"] = [{"name": "sysfs", "hostPath": {"path": "/sys", "type": "Directory"}}]
            pod["securityContext"] = {"runAsUser": 0, "runAsGroup": 0}
            container["securityContext"].update(readOnlyRootFilesystem=True, capabilities={"drop": ["ALL"], "add": ["NET_RAW"]})
            container["volumeMounts"] = [{"name": "sysfs", "mountPath": "/host-sys", "readOnly": True}]
            container["args"] = lldp_args(c, "/host-sys/class/net")
            container["env"] = [{"name": "NODE_NAME", "valueFrom": {"fieldRef": {"fieldPath": "spec.nodeName"}}}]
            k8s_auth(pod, container, c["kubernetes"]["lldpAuth"])
        if component != "lldp":
            port = 8080 if component == "algorithm" else 8081
            for probe, route in (("livenessProbe", "/healthz"), ("readinessProbe", "/readyz")):
                container[probe] = {"httpGet": {"path": route, "port": port}, "periodSeconds": 5, "initialDelaySeconds": 5}
            container["startupProbe"] = {"httpGet": {"path": "/healthz", "port": port}, "periodSeconds": 5, "failureThreshold": 60}
        spec = {"selector": {"matchLabels": labels}, "template": template}
        if kind == "Deployment":
            spec.update(replicas=1, strategy={"type": "Recreate"})
        else:
            spec["updateStrategy"] = {"type": "RollingUpdate", "rollingUpdate": {"maxUnavailable": 1}}
        docs.append(obj(kind, name, ns, spec=spec))
    return {"apiVersion": "v1", "kind": "List", "items": docs}


def bind(c, source, dest):
    return {"type": "bind", "source": str(local_path(c, source)), "target": dest,
            "read_only": True, "bind": {"create_host_path": False}}


def compose(c, node=False):
    if node:
        name = c["lldp"]["nodeName"]
        if not name:
            raise ValueError("lldp.nodeName is required")
        return {"name": "ngd-ngg-node", "services": {"lldp": {
            "image": c["images"]["lldp"], "restart": "unless-stopped", "network_mode": "host",
            "user": "0:0", "read_only": True, "cap_drop": ["ALL"], "cap_add": ["NET_RAW"],
            "security_opt": ["no-new-privileges:true"],
            "environment": {"NODE_NAME": name, "KUBECONFIG": "/etc/ngd-ngg/auth/kubeconfig"},
            "command": lldp_args(c, "/host-sys/class/net"),
            "volumes": [bind(c, "/sys", "/host-sys"), bind(c, c["lldp"]["kubeconfig"], "/etc/ngd-ngg/auth/kubeconfig")]}}}
    e = c["external"]
    alg = {"image": c["images"]["algorithm"], "restart": "unless-stopped", "init": True,
           "environment": prometheus_env(c), "cap_drop": ["ALL"], "security_opt": ["no-new-privileges:true"],
           "volumes": [bind(c, c["topologyFile"], "/etc/ngd-ngg/topology/topology.yaml")],
           "ports": [{"target": 8080, "published": str(e["algorithmPort"]), "host_ip": e["bindAddress"]}],
           "healthcheck": {"test": ["CMD", "python3", "-c", "import urllib.request; urllib.request.urlopen('http://127.0.0.1:8080/healthz', timeout=3).read()"],
                           "interval": "5s", "timeout": "4s", "retries": 20, "start_period": "10s"}}
    for key, dest in (("tokenFile", "/etc/ngd-ngg/prometheus/token"), ("caFile", "/etc/ngd-ngg/prometheus/ca.crt"),
                      ("metricsConfigFile", "/etc/ngd-ngg/metrics/metrics.json")):
        if c["prometheus"][key]:
            alg["volumes"].append(bind(c, c["prometheus"][key], dest))
    prc = {"image": c["images"]["prc"], "restart": "unless-stopped", "read_only": True,
           "cap_drop": ["ALL"], "security_opt": ["no-new-privileges:true"],
           "depends_on": {"algorithm": {"condition": "service_healthy"}},
           "environment": {"KUBECONFIG": "/etc/ngd-ngg/auth/kubeconfig", "ALGORITHM_URL": "http://algorithm:8080", "CLUSTER_ID": c["clusterId"]},
           "command": prc_args(c, False),
           "volumes": [bind(c, e["prcKubeconfig"], "/etc/ngd-ngg/auth/kubeconfig")],
           "ports": [{"target": 8081, "published": str(e["prcPort"]), "host_ip": e["bindAddress"]}]}
    return {"name": e["projectName"], "services": {"algorithm": alg, "prc": prc}}
