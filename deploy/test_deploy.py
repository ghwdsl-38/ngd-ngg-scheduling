"""Offline deployment contract tests. No cluster, daemon, credentials or network required."""
import copy
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from deploy import check_files, install_apis, no_placeholder, render
from render import ROOT, bootstrap, compose, kubernetes, load


class DeploymentTest(unittest.TestCase):
    def setUp(self):
        self.c = load(ROOT / "deploy/config.example.json")
        self.c["namespace"] = "another-namespace"

    def item(self, kind, name, c=None):
        return next(x for x in kubernetes(c or self.c)["items"] if x["kind"] == kind and x["metadata"]["name"] == name)

    def test_incluster_and_namespace_propagation(self):
        prc = self.item("Deployment", "prc")
        pod = prc["spec"]["template"]["spec"]
        container = pod["containers"][0]
        self.assertTrue(pod["automountServiceAccountToken"])
        self.assertIn("--leader-elect=true", container["args"])
        self.assertIn("--demand-refresh-interval=15s", container["args"])
        self.assertIn("--max-concurrent-refreshes=5", container["args"])
        self.assertIn("another-namespace.svc", next(e["value"] for e in container["env"] if e["name"] == "ALGORITHM_URL"))
        self.assertEqual(prc["spec"]["strategy"]["type"], "Recreate")
        for item in kubernetes(self.c)["items"]:
            self.assertEqual(item["metadata"]["namespace"], "another-namespace")

    def test_kubeconfig_prc_has_no_sa_dependency_and_readable_secret(self):
        self.c["kubernetes"]["prcAuth"] = {"mode": "kubeconfig", "secretName": "prc-config", "subject": {"kind": "User", "name": "prc-user"}}
        pod = self.item("Deployment", "prc")["spec"]["template"]["spec"]
        self.assertFalse(pod["automountServiceAccountToken"])
        self.assertIn("--leader-elect=false", pod["containers"][0]["args"])
        self.assertEqual(pod["securityContext"]["fsGroup"], 65532)
        self.assertEqual(pod["volumes"][0]["secret"]["secretName"], "prc-config")
        binding = next(i for i in bootstrap(self.c)["items"] if i["kind"] == "ClusterRoleBinding" and i["metadata"]["name"].endswith("-prc"))
        self.assertEqual(binding["subjects"][0]["name"], "prc-user")

    def test_prc_refresh_configuration_propagates_to_kubernetes_and_compose(self):
        self.c["prc"] = {"demandRefreshSeconds": 45, "maxConcurrentRefreshes": 12}
        args = self.item("Deployment", "prc")["spec"]["template"]["spec"]["containers"][0]["args"]
        self.assertIn("--demand-refresh-interval=45s", args)
        self.assertIn("--max-concurrent-refreshes=12", args)
        command = compose(self.c)["services"]["prc"]["command"]
        self.assertIn("--demand-refresh-interval=45s", command)
        self.assertIn("--max-concurrent-refreshes=12", command)

    def test_old_config_without_prc_section_uses_refresh_defaults(self):
        raw = json.loads((ROOT / "deploy/config.example.json").read_text())
        del raw["prc"]
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "config.json"
            path.write_text(json.dumps(raw))
            config = load(path)
        self.assertEqual(config["prc"], {
            "demandRefreshSeconds": 15,
            "maxConcurrentRefreshes": 5,
        })

    def test_invalid_prc_refresh_configuration_is_rejected(self):
        for key, value in (("demandRefreshSeconds", 0), ("maxConcurrentRefreshes", -1),
                           ("maxConcurrentRefreshes", True), ("demandRefreshSeconds", 1.5)):
            with self.subTest(key=key, value=value):
                raw = json.loads((ROOT / "deploy/config.example.json").read_text())
                raw["prc"][key] = value
                with tempfile.TemporaryDirectory() as directory:
                    path = Path(directory) / "config.json"
                    path.write_text(json.dumps(raw))
                    with self.assertRaises(ValueError):
                        load(path)

    def test_lldp_preserves_sysfs_and_raw_socket_capability(self):
        pod = self.item("DaemonSet", "lldp-agent")["spec"]["template"]["spec"]
        self.assertTrue(pod["hostNetwork"])
        self.assertEqual(pod["volumes"][0]["hostPath"]["path"], "/sys")
        self.assertEqual(pod["securityContext"]["runAsUser"], 0)
        self.assertEqual(pod["containers"][0]["securityContext"]["capabilities"], {"drop": ["ALL"], "add": ["NET_RAW"]})
        self.assertIn("--sys-class-net=/host-sys/class/net", pod["containers"][0]["args"])
        self.assertIn("--timeout=65s", pod["containers"][0]["args"])
        self.assertIn("--interval=180s", pod["containers"][0]["args"])
        self.assertIn("--interfaces=bond0", pod["containers"][0]["args"])
        self.assertFalse(any(a.startswith("--mode") for a in pod["containers"][0]["args"]))

    def test_kubeconfig_lldp(self):
        self.c["kubernetes"]["lldpAuth"] = {"mode": "kubeconfig", "secretName": "node-identity", "subject": {"kind": "User", "name": "lldp-user"}}
        pod = self.item("DaemonSet", "lldp-agent")["spec"]["template"]["spec"]
        self.assertFalse(pod["automountServiceAccountToken"])
        self.assertEqual(pod["volumes"][1]["secret"]["secretName"], "node-identity")

    def test_missing_external_subject_rejected(self):
        self.c["external"]["prcSubject"] = None
        with self.assertRaises(ValueError):
            bootstrap(self.c, external=True)

    def test_bootstrap_subjects_are_not_deployment_operator(self):
        self.c["external"]["prcSubject"] = {"kind": "User", "name": "runtime-prc"}
        self.c["external"]["lldpSubject"] = {"kind": "ServiceAccount", "name": "runtime-lldp", "namespace": "identities"}
        docs = bootstrap(self.c, external=True)["items"]
        self.assertFalse(any(x["kind"] == "ServiceAccount" for x in docs))
        subjects = [i["subjects"][0] for i in docs if i["kind"] == "ClusterRoleBinding"]
        self.assertEqual(subjects[1]["namespace"], "identities")
        self.assertEqual(subjects[0]["name"], "runtime-prc")

    def test_prometheus_secret_does_not_read_local_credentials(self):
        self.c["prometheus"].update(secretName="prom-auth", tokenFile="nonexistent-token", caFile="nonexistent-ca")
        alg = self.item("Deployment", "ngd-ngg-algorithm")
        pod = alg["spec"]["template"]["spec"]
        self.assertEqual(pod["volumes"][-1]["secret"]["items"], [{"key": "token", "path": "token"}, {"key": "ca.crt", "path": "ca.crt"}])
        self.assertNotIn("nonexistent-token", json.dumps(alg))
        self.c["prometheus"]["secretName"] = ""
        with self.assertRaises(ValueError):
            kubernetes(self.c)

    def test_config_change_triggers_algorithm_rollout(self):
        with tempfile.TemporaryDirectory() as d:
            file = Path(d) / "topology.yaml"
            file.write_text("version: v1")
            self.c["topologyFile"] = str(file)
            a = self.item("Deployment", "ngd-ngg-algorithm")["spec"]["template"]["metadata"]["annotations"]
            file.write_text("version: v2")
            b = self.item("Deployment", "ngd-ngg-algorithm")["spec"]["template"]["metadata"]["annotations"]
            self.assertNotEqual(a, b)

    def test_external_runtime_communication_and_singleton(self):
        services = compose(self.c)["services"]
        self.assertEqual(services["prc"]["environment"]["ALGORITHM_URL"], "http://algorithm:8080")
        self.assertIn("--leader-elect=false", services["prc"]["command"])
        self.assertEqual(services["algorithm"]["ports"][0]["host_ip"], "127.0.0.1")
        self.assertEqual(services["prc"]["depends_on"]["algorithm"]["condition"], "service_healthy")
        self.assertFalse(services["prc"]["volumes"][0]["bind"]["create_host_path"])

    def test_node_uses_host_network_and_correct_identity(self):
        self.c["lldp"]["nodeName"] = "worker-007"
        node = compose(self.c, node=True)["services"]["lldp"]
        self.assertEqual(node["network_mode"], "host")
        self.assertEqual(node["environment"]["NODE_NAME"], "worker-007")
        self.assertEqual(node["cap_drop"], ["ALL"])
        self.assertEqual(node["volumes"][0]["source"], "/sys")

    def test_missing_mount_fails_before_start(self):
        with self.assertRaises(ValueError):
            check_files(self.c, "external")

    def test_render_escapes_compose_interpolation(self):
        self.c["prometheus"]["url"] = "http://prometheus/$value"
        with tempfile.TemporaryDirectory() as d:
            out = render(self.c, "external", Path(d))
            self.assertIn("$$value", out.read_text())

    def test_placeholder_is_rejected_for_execution(self):
        with self.assertRaises(ValueError):
            no_placeholder(self.c["images"])

    def test_bootstrap_uses_new_crds_and_explicit_context(self):
        self.c["context"] = "target-cluster"
        with tempfile.TemporaryDirectory() as d, patch("deploy.tool", return_value="kubectl"), patch("deploy.run") as run:
            install_apis(self.c, False, Path(d))
            calls = [list(map(str, call.args[0])) for call in run.call_args_list]
            self.assertEqual(len(calls), 5)
            self.assertTrue(all(x[:3] == ["kubectl", "--context", "target-cluster"] for x in calls))
            self.assertIn("paas-schedbridge-master-new", calls[0][-1])
            self.assertIn("--for=condition=Established", calls[2])


if __name__ == "__main__":
    unittest.main()
