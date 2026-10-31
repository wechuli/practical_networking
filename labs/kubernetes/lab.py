import argparse
import json
from pathlib import Path
import subprocess
import time
import urllib.error
import urllib.request
import uuid


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--context", required=True)
    parser.add_argument("--mode", choices=("policy", "ingress", "mesh"),
                        default="policy")
    parser.add_argument("--ingress-class", default="traefik")
    parser.add_argument("--controller-namespace", default="traefik")
    parser.add_argument("--controller-service", default="traefik")
    arguments = parser.parse_args()
    namespace = "course-" + uuid.uuid4().hex[:10]
    directory = Path(__file__).parent
    evidence = Path("artifacts") / namespace
    evidence.mkdir(parents=True)
    base = ["kubectl", "--context", arguments.context,
            "--request-timeout=100s"]

    def command(*parts, input=None, check=True, timeout=120):
        return subprocess.run(
            [*base, *parts], input=input, capture_output=True, text=True,
            check=check, timeout=timeout,
        )

    def scoped(*parts, **options):
        return command("-n", namespace, *parts, **options)

    def probe(client, expected=0):
        result = scoped(
            "exec", client, "-c", "client", "--", "curl", "--noproxy", "*",
            "--fail", "--silent", "--show-error", "--max-time", "3",
            "-w", "\nHTTP_STATUS:%{http_code}\n", "http://web/", check=False,
        )
        passed = result.returncode == expected
        if expected == 22:
            passed = passed and "HTTP_STATUS:403" in result.stdout
        return passed, result

    def wait_probe(client, expected):
        deadline = time.monotonic() + 30
        while time.monotonic() < deadline:
            passed, result = probe(client, expected)
            if passed:
                return
            time.sleep(0.2)
        raise AssertionError(
            f"{client}: expected curl exit {expected}, got "
            f"{result.returncode}: {result.stderr}"
        )

    command("get", "nodes")
    (evidence / "version.json").write_text(
        command("version", "-o", "json").stdout
    )
    if arguments.mode == "mesh":
        subprocess.run(["linkerd", "--context", arguments.context, "check"],
                       check=True, timeout=180)
    command("create", "namespace", namespace)
    forward = None
    try:
        manifest = (directory / "resources.yaml").read_text()
        if arguments.mode == "mesh":
            manifest = subprocess.run(
                ["linkerd", "--context", arguments.context, "inject", "-"],
                input=manifest, text=True, capture_output=True,
                check=True, timeout=30,
            ).stdout
        scoped("apply", "-f", "-", input=manifest)
        scoped("rollout", "status", "deployment/web", "--timeout=90s")
        scoped("wait", "--for=condition=Ready", "pod/allowed", "pod/denied",
               "--timeout=90s")
        for client in ("allowed", "denied"):
            wait_probe(client, 0)
        for resource in ("pods", "services", "endpointslices"):
            (evidence / f"{resource}.json").write_text(
                scoped("get", resource, "-o", "json").stdout
            )
        if arguments.mode == "policy":
            scoped("apply", "-f", str(directory / "policy.yaml"))
            wait_probe("denied", 28)
            wait_probe("allowed", 0)
            print("PASS: CNI enforcement proven by denied and allowed clients")
        elif arguments.mode == "mesh":
            scoped("apply", "-f", str(directory / "mesh-policy.yaml"))
            wait_probe("denied", 22)
            wait_probe("allowed", 0)
            metrics = subprocess.run(
                ["linkerd", "--context", arguments.context, "diagnostics",
                 "proxy-metrics", "-n", namespace, "deploy/web"],
                check=True, capture_output=True, text=True, timeout=30,
            ).stdout
            (evidence / "proxy-metrics.txt").write_text(metrics)
            print("PASS: mesh identity permits allowed and rejects denied")
            print("Inspect saved proxy TLS/identity metrics for this version")
        else:
            ingress = {
                "apiVersion": "networking.k8s.io/v1", "kind": "Ingress",
                "metadata": {"name": "web"},
                "spec": {
                    "ingressClassName": arguments.ingress_class,
                    "rules": [{"host": "course.test", "http": {"paths": [{
                        "path": "/", "pathType": "Prefix", "backend": {
                            "service": {"name": "web", "port": {"number": 80}}
                        },
                    }]}}],
                },
            }
            scoped("apply", "-f", "-", input=json.dumps(ingress))
            with (evidence / "port-forward.log").open("w+") as logfile:
                forward = subprocess.Popen(
                    [*base, "-n", arguments.controller_namespace,
                     "port-forward", "--address", "127.0.0.1",
                     "service/" + arguments.controller_service, ":80"],
                    stdout=logfile, stderr=logfile, text=True,
                )
                deadline = time.monotonic() + 30
                while time.monotonic() < deadline:
                    logfile.seek(0)
                    lines = logfile.read().splitlines()
                    bound = [line for line in lines
                             if line.startswith("Forwarding from 127.0.0.1:")]
                    if bound:
                        address = "http://" + bound[0].split()[2]
                        break
                    if forward.poll() is not None:
                        raise RuntimeError("Controller port-forward failed")
                    time.sleep(0.05)
                else:
                    raise TimeoutError("Controller port-forward not ready")
                opener = urllib.request.build_opener(
                    urllib.request.ProxyHandler({})
                )
                while time.monotonic() < deadline:
                    try:
                        request = urllib.request.Request(
                            address, headers={"Host": "course.test"}
                        )
                        with opener.open(request, timeout=3) as response:
                            assert response.status == 200
                            assert b"nginx" in response.read().lower()
                            break
                    except urllib.error.HTTPError as error:
                        if error.code != 404:
                            raise
                        time.sleep(0.2)
                else:
                    raise TimeoutError("Ingress route did not become ready")
                wrong_host = urllib.request.Request(
                    address, headers={"Host": "unmatched.course.test"}
                )
                try:
                    with opener.open(wrong_host, timeout=3):
                        raise AssertionError("Unexpected unmatched-host route")
                except urllib.error.HTTPError as error:
                    assert error.code == 404
                print("PASS: Ingress controller reached the ready backend")
    finally:
        if forward:
            forward.terminate()
            forward.wait(timeout=5)
        command("delete", "namespace", namespace, "--wait=true",
                "--timeout=90s")
        print(f"Owned namespace removed; evidence: {evidence}")


if __name__ == "__main__":
    main()
