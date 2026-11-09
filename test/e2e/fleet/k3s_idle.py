"""Check native k3s capacity and first-request delivery on the disposable guest."""
import http.client
import json
import secrets
import subprocess
import sys
import time

SOCKET = "/run/hostlier/controller.sock"


def run(*args):
    return subprocess.check_output(args, text=True, timeout=35).strip()


def check(condition, message):
    if not condition:
        raise RuntimeError(message)


def kube(*args):
    return json.loads(run("k3s", "kubectl", *args, "--request-timeout=15s", "-o", "json"))


def app():
    return json.loads(run("hostlier", "status", "echo", "--socket", SOCKET, "--json"))


class Deployment:
    def __init__(self):
        name = app()["runtimeId"]
        matches = [obj for obj in kube("get", "deployments", "-A", "-l", "io.hostlier.app=echo")["items"]
                   if obj["metadata"]["name"] == name]
        check(len(matches) == 1, f"Expected a single Kubernetes Deployment for runtime ID {name}")
        obj = matches[0]
        self.name, self.ns, self.uid = name, obj["metadata"]["namespace"], obj["metadata"]["uid"]
        self.selector = ",".join(f"{k}={v}" for k, v in obj["spec"]["selector"]["matchLabels"].items())

    def capacity(self, expected):
        deadline = time.monotonic() + 120
        while time.monotonic() < deadline:
            obj = kube("get", "deployment", self.name, "-n", self.ns)
            meta, status = obj["metadata"], obj.get("status", {})
            check(meta["uid"] == self.uid, "Hostlier replaced the Deployment during idle scaling")
            pods = kube("get", "pods", "-n", self.ns, "-l", self.selector)["items"]
            observed = (obj["spec"]["replicas"], status.get("replicas", 0),
                        status.get("readyReplicas", 0), len(pods))
            if (observed == (expected,) * 4 and status.get("observedGeneration") == meta["generation"]
                    and all(ready(p) for p in pods)):
                return meta["generation"], pods
            time.sleep(0.5)
        raise RuntimeError(f"{self.ns}/{self.name} did not converge to {expected}; "
                           f"last (desired, observed, ready, pods) = {observed}")

    def wake(self, route, previous_uid):
        marker = secrets.token_hex(12)
        path = "/order/" + marker
        body = b"first POST after sleep\n" + marker.encode()
        # HTTPSConnection sends exactly one request, without redirects or retries.
        conn = http.client.HTTPSConnection(route, timeout=95)
        try:
            conn.request("POST", path, body, {"Content-Type": "application/octet-stream"})
            reply = conn.getresponse()
            check(reply.status == 200 and reply.read() == body, "Cold POST did not return the original body")
        finally:
            conn.close()
        _, (pod,) = self.capacity(1)
        check(pod["metadata"]["uid"] != previous_uid, "Expected a newly scheduled Pod")
        check(all(c["restartCount"] == 0 for c in pod["status"]["containerStatuses"]),
              "A container restarted; current logs cannot prove single delivery")
        logs = run("k3s", "kubectl", "logs", "-n", self.ns, pod["metadata"]["name"], "-c", "app")
        deliveries = sum(line.endswith(" POST " + path) for line in logs.splitlines())
        check(deliveries == 1, f"Expected one POST {path} in application logs, got {deliveries}")
        return pod["metadata"]["uid"]


def ready(pod):
    return not pod["metadata"].get("deletionTimestamp") and any(
        c["type"] == "Ready" and c["status"] == "True" for c in pod["status"].get("conditions", []))


def restart_asleep(deployment, generation):
    def invocation():
        return run("systemctl", "show", "-p", "InvocationID", "--value", "hostlier.service")

    before = invocation()
    run("systemctl", "restart", "hostlier.service")
    after = invocation()
    check(before and after and before != after, "Hostlier service did not restart")
    for _ in range(30):
        try:
            status = app()
            break
        except subprocess.CalledProcessError:
            time.sleep(1)
    else:
        raise RuntimeError("Restarted controller did not become available")
    check(status["runtimeStatus"]["idleSleeping"], "Controller lost sleep intent after restart")
    check(deployment.capacity(0)[0] == generation, "Deployment generation changed during restart")


def main(route):
    deployment = Deployment()
    _, (pod,) = deployment.capacity(1)
    uid = pod["metadata"]["uid"]
    for restart in (False, True):
        generation, _ = deployment.capacity(0)
        if restart:
            restart_asleep(deployment, generation)
        uid = deployment.wake(route, uid)
        print(f"PASS k3s {deployment.ns}/{deployment.name}: 1 ready → 0 replicas and 0 Pods → "
              f"1 new ready Pod; first POST echoed and logged once (restart={restart})", flush=True)


if __name__ == "__main__":
    main(sys.argv[1])
