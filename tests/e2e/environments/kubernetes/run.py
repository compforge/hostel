#!/usr/bin/env python3
"""Run on the Environment's access Host, using its kubeconfig through toolbox.

Requires harness-toolbox[kube] with native resource operations. The caller owns
image/node preparation. This fixture owns only its disposable namespace.
"""

import argparse
import asyncio
import json
import uuid
from dataclasses import asdict
from pathlib import Path

from harness_common.client import ClientManager
from harness_common.environment import KubernetesEnvironment
from harness_toolbox.environment import kubernetes_source
from harness_toolbox.kube import Options, PodRef
from kubernetes_asyncio.client import ApiException


async def main() -> int:
    profiles = json.loads(
        Path(__file__).with_name("security-contexts.json").read_text()
    )
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--kubeconfig", required=True)
    parser.add_argument("--context")
    parser.add_argument("--image", required=True)
    parser.add_argument(
        "--host", required=True, help="SSH address identifying this access Host"
    )
    parser.add_argument("--profile", choices=profiles, required=True)
    parser.add_argument(
        "--pod-security",
        choices=["privileged", "baseline", "restricted"],
        default="privileged",
    )
    parser.add_argument(
        "--test-filter",
        default="TestRuntimeContract|TestFeaturePoliciesOff|TestFeatureRequiredStartupFailure",
    )
    parser.add_argument("--timeout", type=int, default=300)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    args.output.mkdir(parents=True, exist_ok=False)
    namespace = "hostel-e2e-" + uuid.uuid4().hex[:12]

    def save(name: str, value: object) -> None:
        if isinstance(value, bytes):
            (args.output / name).write_bytes(value)
        else:
            (args.output / name).write_text(
                json.dumps(value, indent=2, default=str) + "\n"
            )

    async def capture(name, operation):
        try:
            result = await operation
            save(name, result)
            return result
        except (ApiException, RuntimeError, ValueError, TimeoutError) as error:
            save(name + ".error.json", {"error": str(error)})
            return None

    profile = profiles[args.profile]
    config = {
        "service": {
            "name": "hostel",
            "environment": {
                "name": "devbox-k8s",
                "kind": "kubernetes",
                "host": {"name": "devbox", "transport": "ssh", "address": args.host},
                "kubeconfig": args.kubeconfig,
                "context": args.context,
            },
        },
        "profile": args.profile,
        "custom": {
            "require.os": "linux",
            **{"require." + k: v for k, v in profile["required"].items()},
        },
    }
    pod = {
        "apiVersion": "v1",
        "kind": "Pod",
        "metadata": {"name": "suite", "namespace": namespace},
        "spec": {
            "restartPolicy": "Never",
            "activeDeadlineSeconds": args.timeout,
            "automountServiceAccountToken": False,
            "containers": [
                {
                    "name": "suite",
                    "image": args.image,
                    "imagePullPolicy": "IfNotPresent",
                    "securityContext": profile["securityContext"],
                    "args": [
                        "-test.v",
                        "-test.timeout=" + str(args.timeout) + "s",
                        "-test.run=" + args.test_filter,
                    ],
                    "env": [
                        {
                            "name": "HOSTEL_E2E_POD_UID",
                            "valueFrom": {"fieldRef": {"fieldPath": "metadata.uid"}},
                        }
                    ],
                    "volumeMounts": [
                        {
                            "name": "config",
                            "mountPath": "/e2e/config.yaml",
                            "subPath": "config.yaml",
                            "readOnly": True,
                        }
                    ],
                    "resources": {
                        "requests": {"cpu": "1", "memory": "1Gi"},
                        "limits": {"cpu": "4", "memory": "4Gi"},
                    },
                }
            ],
            "volumes": [{"name": "config", "configMap": {"name": "suite"}}],
        },
    }
    if "command" in profile:
        pod["spec"]["containers"][0]["command"] = profile["command"]
    save("submitted-pod.json", pod)
    save("environment-config.json", config)
    summary = {
        "profile": args.profile,
        "namespace": namespace,
        "status": "error",
        "reason": "runner interrupted",
    }
    # This process is already on the access Host. Omitted Host means local API
    # access; the Pod's metadata still identifies the externally reachable Host.
    environment = KubernetesEnvironment("devbox-k8s", args.kubeconfig, args.context)
    source = kubernetes_source(
        environment, Options(namespace, request_timeout_s=20, connection_pool_maxsize=4)
    )
    async with ClientManager() as clients:
        kube = await clients.get(source)
        resources = kube.resources
        observed_namespace = None
        ref = None
        try:
            save("nodes.json", await resources.list("v1", "Node"))
            for kind in (
                "MutatingWebhookConfiguration",
                "ValidatingWebhookConfiguration",
                "ValidatingAdmissionPolicy",
                "ValidatingAdmissionPolicyBinding",
            ):
                save(
                    kind + ".json",
                    await resources.list("admissionregistration.k8s.io/v1", kind),
                )
            ns = {
                "apiVersion": "v1",
                "kind": "Namespace",
                "metadata": {
                    "name": namespace,
                    "labels": {
                        "hostel-e2e": "true",
                        "pod-security.kubernetes.io/enforce": args.pod_security,
                    },
                },
            }
            observed_namespace = await resources.create(ns)
            save("namespace.json", observed_namespace)
            await resources.create(
                {
                    "apiVersion": "v1",
                    "kind": "ConfigMap",
                    "metadata": {"name": "suite", "namespace": namespace},
                    "data": {"config.yaml": json.dumps(config)},
                }
            )
            admitted = await resources.create(pod)
            save("admitted-pod.json", admitted)
            ref = PodRef("suite", admitted["metadata"]["uid"])
            terminal = await kube.wait_completed(
                ref, timeout_s=args.timeout, interval_s=2
            )
            observed = await resources.get("v1", "Pod", ref.name)
            save("observed-pod.json", observed)
            if not terminal.containers:
                pod_status = observed.get("status", {})
                raise RuntimeError(
                    f"Pod {terminal.phase}: {pod_status.get('reason', '')}: "
                    f"{pod_status.get('message', 'no container was started')}"
                )
            logs = await kube.read_logs(
                ref, container="suite", max_bytes=32 * 1024 * 1024
            )
            save("suite.log", logs)
            output = logs.decode(errors="replace")
            evidence = [
                json.loads(line.split(" ", 1)[1])
                for line in output.splitlines()
                if line.startswith("E2E_ENVIRONMENT_JSON ")
            ]
            save("environments.json", evidence)
            executed = "=== RUN" in output
            passed = (
                terminal.phase == "Succeeded"
                and evidence
                and executed
                and "\nPASS\n" in output
            )
            status = (
                "pass" if passed else ("fail" if evidence and executed else "error")
            )
            summary.update(
                status=status, reason="native suite " + terminal.phase.lower()
            )
        except (ApiException, RuntimeError, ValueError, TimeoutError) as error:
            summary.update(reason=str(error) or type(error).__name__)
        finally:
            if observed_namespace is not None:
                try:
                    if ref is not None:
                        events = await capture("events.json", kube.list_events(ref))
                        if events is not None:
                            save("events.json", [asdict(event) for event in events])
                        await capture(
                            "final-pod.json", resources.get("v1", "Pod", ref.name)
                        )
                        await capture(
                            "suite-final.log",
                            kube.read_logs(
                                ref, container="suite", max_bytes=32 * 1024 * 1024
                            ),
                        )
                finally:
                    try:
                        await resources.delete(observed_namespace)
                        await resources.wait_deleted(
                            observed_namespace, timeout_s=25, interval_s=1
                        )
                        summary["cleanup"] = "complete"
                    except (
                        ApiException,
                        RuntimeError,
                        ValueError,
                        TimeoutError,
                    ) as error:
                        summary.update(
                            status="error",
                            reason="namespace cleanup failed",
                            cleanup=str(error),
                        )
            save("summary.json", summary)
            print(json.dumps(summary), flush=True)
    return 0 if summary["status"] == "pass" else 1


if __name__ == "__main__":
    raise SystemExit(asyncio.run(main()))
