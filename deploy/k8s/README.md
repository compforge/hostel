# Kubernetes examples

This directory contains integration examples, not a complete production
deployment of hostel.

## Permissions for the carrier Pod creator

In the usual deployment, sandbox-server directly creates the carrier Pod that
runs Hostel. There are three separate permission layers:

| Layer | Required configuration |
| --- | --- |
| sandbox-server ServiceAccount | RBAC permission to create Pods in the carrier namespace. If Pod Security Admission (PSA) enforces Baseline or Restricted, exempt this exact authenticated ServiceAccount username. |
| Created carrier Pod | Put the required Linux security settings on the Hostel container: `appArmorProfile.type: Unconfined` for bubblewrap, and `capabilities.add: ["SYS_PTRACE"]` for PRoot. |
| Carrier node and container runtime | The kernel/runtime must actually support the requested operation: usable unprivileged user namespaces for bubblewrap, and usable `ptrace(2)` for PRoot. |

The sandbox-server container itself does not need `SYS_PTRACE`, an Unconfined
AppArmor profile, `privileged`, `hostPID`, or `SYS_ADMIN`. Its ServiceAccount is
a Kubernetes API identity; Linux capabilities and AppArmor settings belong to
the Hostel container in the Pod it creates.

Set the namespaces and exact creator identity used by the examples:

```bash
SANDBOX_SERVER_NAMESPACE="<sandbox-server-namespace>"
CARRIER_NAMESPACE="<carrier-namespace>"
SANDBOX_SERVER_USER="system:serviceaccount:${SANDBOX_SERVER_NAMESPACE}:sandbox-server"
```

Check the creator's minimum feature-specific RBAC permission:

```bash
kubectl auth can-i create pods \
  --namespace "${CARRIER_NAMESPACE}" \
  --as "${SANDBOX_SERVER_USER}"
```

Sandbox-server may also need `get`, `watch`, `delete`, and other Pod permissions
for its lifecycle responsibilities; those are outside Hostel's isolation
requirements. The requirement here is that the same ServiceAccount identity
directly submits the carrier Pod. An exemption for sandbox-server does not
follow through a Deployment or Job controller, because those controllers
create Pods under a different authenticated identity.

For a carrier that should use bubblewrap when available and retain PRoot as a
fallback, have sandbox-server create the Hostel container with both settings:

```yaml
spec:
  containers:
    - name: hostel
      securityContext:
        appArmorProfile:
          type: Unconfined
        capabilities:
          add: ["SYS_PTRACE"]
```

These settings are incremental: merge them into the template's existing
security context and preserve its current capability `drop`, seccomp, and
other security settings. Neither setting grants `privileged`, `hostPID`, or
`SYS_ADMIN`. If only one feature is wanted, include only its corresponding
setting described below.

## Enable bubblewrap suite isolation

Bubblewrap gives a Bed a private mount view and a canonical `/workspace`
mount. The standard Hostel image includes bubblewrap. To make it usable:

1. Give the sandbox-server ServiceAccount RBAC permission to create Pods in the
   carrier namespace.
2. If PSA Baseline or Restricted is enforced, add the exact ServiceAccount
   username to the PSA exemption described below. The exemption is needed
   because the carrier Pod requests an Unconfined AppArmor profile.
3. Have sandbox-server set `appArmorProfile.type: Unconfined` on the Hostel
   container in every newly created carrier Pod. On Kubernetes versions before
   1.30, use the equivalent legacy per-container AppArmor annotation.
4. Schedule the Pod to a node where unprivileged user namespaces are enabled
   and usable. A non-zero `/proc/sys/user/max_user_namespaces` is a necessary
   host fact, but Hostel's bubblewrap probe is the final result.

Do not add `SYS_ADMIN`, `privileged`, or `hostPID` for bubblewrap. Recreate
existing carrier Pods after changing the sandbox-server Pod template; changing
the template does not mutate running Pods.

## Enable ptrace for the PRoot workspace view

The standard Hostel image already includes PRoot. When bubblewrap and pathshim
cannot provide the per-Bed `/workspace` view, Hostel can fall back to it. PRoot
traces only the processes that it starts, but the container runtime must allow
`ptrace(2)`.

Apply the permission only to the Hostel container in the carrier Pod template
created by sandbox-server. Do not change kubelet flags or node-wide sysctls,
and do not enable `privileged` or `hostPID`:

```yaml
spec:
  containers:
    - name: hostel
      securityContext:
        capabilities:
          add: ["SYS_PTRACE"]
```

This is an incremental patch: merge `SYS_PTRACE` into the template's existing
capability list and leave its current `drop`, seccomp, AppArmor, and other
security settings unchanged. The change applies only to newly created Pods, so
recreate existing carrier Pods through sandbox-server after updating its
template.

The creator-side procedure is:

1. Give the sandbox-server ServiceAccount RBAC permission to create Pods in the
   carrier namespace.
2. If PSA Baseline or Restricted is enforced, add the exact ServiceAccount
   username to the PSA exemption described below. The exemption is needed
   because those standards do not allow adding `SYS_PTRACE`.
3. Have sandbox-server add `SYS_PTRACE` to the Hostel container in every newly
   created carrier Pod.
4. Confirm the running container can actually trace a child process using
   `/v1/diagnostics`; admission success alone does not prove ptrace is usable.

The Kubernetes [Baseline and Restricted Pod Security
Standards](https://kubernetes.io/docs/concepts/security/pod-security-standards/)
do not permit adding `SYS_PTRACE`. If Pod Security Admission rejects the
template, exempt the narrow sandbox-server ServiceAccount identity or add an
equivalent narrowly scoped exception in the cluster's admission policy. The
exemption example in the next section can be reused; do not relax the whole
carrier namespace. Other admission controllers may still require their own
allowlist.

Verify the newly created Pod without interpreting kernel-specific knobs:

```bash
CARRIER_NAMESPACE="<carrier-namespace>"
POD="<running-carrier-pod>"
CONTAINER="<hostel-container-name>"

kubectl exec "${POD}" \
  --namespace "${CARRIER_NAMESPACE}" \
  --container "${CONTAINER}" \
  -- curl --fail --silent --show-error \
  http://127.0.0.1:8872/v1/diagnostics | \
  jq '{
    ptrace: .probes.ptrace,
    proot: .probes.proot,
    workspace_view
  }'
```

The permission is usable when `ptrace` reports `attempted: true`,
`exit_code: 0`, and an empty `error`. When a suite mount is unavailable, the
`proot` probe should report the same result and a selected PRoot view reports
`{"mode":"proot","available":true}`. If suite is already available, Hostel
selects its mount view without attempting PRoot. A missing
`/proc/sys/kernel/yama/ptrace_scope` is only an observed host fact; the probe
result is authoritative. If the probe still fails, send the complete
`/v1/diagnostics` response to the Hostel maintainer instead of changing node
settings one by one.

## Pod Security Admission exemption for the carrier Pod creator

[`pod-security-admission-exemption.yaml`](pod-security-admission-exemption.yaml)
applies when a cluster enforces the Baseline or Restricted Pod Security
Standard, while a trusted sandbox-server directly creates carrier Pods that
request either of these settings:

- `appArmorProfile.type: Unconfined` for bubblewrap suite isolation.
- `capabilities.add: ["SYS_PTRACE"]` for the PRoot workspace view.

The file is kube-apiserver configuration, not an object accepted by `kubectl
apply`. A cluster administrator must merge its `exemptions.usernames` entry
into the existing Pod Security `AdmissionConfiguration`, replace
`<sandbox-server-namespace>` with the deployment's actual namespace, and roll
out that control-plane configuration using the cluster's normal procedure. The
exemption only skips PSA checks; it does not mutate the request. The carrier
Pod template must still explicitly request the AppArmor profile, `SYS_PTRACE`,
or both.

After the cluster administrator rolls out the admission configuration, verify
the exemption through a server-side dry run. This example requests both Hostel
features and exercises authentication, RBAC, and admission without creating a
Pod:

```bash
kubectl create --dry-run=server --output=yaml \
  --namespace "${CARRIER_NAMESPACE}" \
  --as "${SANDBOX_SERVER_USER}" \
  --filename - <<'EOF'
apiVersion: v1
kind: Pod
metadata:
  name: hostel-isolation-admission-check
spec:
  restartPolicy: Never
  securityContext:
    runAsNonRoot: true
    seccompProfile:
      type: RuntimeDefault
  containers:
    - name: check
      image: registry.k8s.io/pause:3.10
      securityContext:
        allowPrivilegeEscalation: false
        capabilities:
          add: ["SYS_PTRACE"]
        appArmorProfile:
          type: Unconfined
EOF
```

A successful dry run proves that the API server accepts this ServiceAccount
creating a Pod that requests `Unconfined` and `SYS_PTRACE`. It does not prove
that the real carrier template requests these settings or that the container
runtime applied them. If the deployment intentionally enables only one
feature, remove the other feature's setting from this check. Verify a running
carrier Pod separately:

```bash
POD="<running-carrier-pod>"
CONTAINER="<hostel-container-name>"
```

When PRoot is enabled, first confirm that the stored Pod requests
`SYS_PTRACE`:

```bash
kubectl get pod "${POD}" \
  --namespace "${CARRIER_NAMESPACE}" \
  --output json | jq --raw-output --arg container "${CONTAINER}" \
  '.spec.containers[] | select(.name == $container) | .securityContext.capabilities.add // []'
```

The result must contain `SYS_PTRACE`. This confirms the submitted template,
not that the runtime made ptrace usable; use the PRoot diagnostics check above
for the runtime verdict.

When bubblewrap suite isolation is enabled, inspect the Pod stored by the API
server. For Kubernetes 1.30 and later, the result must be `Unconfined`:

```bash
kubectl get pod "${POD}" \
  --namespace "${CARRIER_NAMESPACE}" \
  --output json | jq --raw-output --arg container "${CONTAINER}" \
  '.spec.containers[] | select(.name == $container) | .securityContext.appArmorProfile.type // "<unset>"'
```

For a pre-1.30 cluster that still uses the legacy annotation, the equivalent
value must be `unconfined`:

```bash
kubectl get pod "${POD}" \
  --namespace "${CARRIER_NAMESPACE}" \
  --output json | jq --raw-output --arg container "${CONTAINER}" \
  '.metadata.annotations["container.apparmor.security.beta.kubernetes.io/" + $container] // "<unset>"'
```

Next read the AppArmor-specific process attribute from inside the container.
The result must be exactly `unconfined`; do not fall back to
`/proc/1/attr/current`, because that shared path can contain another Linux
Security Module's label on a non-AppArmor node:

```bash
kubectl exec "${POD}" \
  --namespace "${CARRIER_NAMESPACE}" \
  --container "${CONTAINER}" \
  -- cat /proc/1/attr/apparmor/current
```

Finally verify the behavior Hostel needs. Adjust port `8872` if the deployment
overrides the listen address:

```bash
kubectl exec "${POD}" \
  --namespace "${CARRIER_NAMESPACE}" \
  --container "${CONTAINER}" \
  -- curl --fail --silent --show-error \
  http://127.0.0.1:8872/v1/diagnostics | \
  jq '{
    isolation,
    bwrap: .probes.bwrap,
    namespace_limits: .system.namespace_limits,
    security_modules: .system.security_modules
  }'
```

For a carrier configured for `suite` (or `auto` on a capable node), expect
`isolation.effective: "suite"`, `isolation.mechanism: "bwrap"`, and a bwrap
probe with `attempted: true`, `exit_code: 0`, and an empty `error`. The `/proc`
check above independently confirms that the runtime applied the requested
AppArmor profile.

Use the first failing layer to locate the problem:

| Result | Meaning / next check |
| --- | --- |
| Server dry run is denied | The PSA exemption is not active for the exact `SANDBOX_SERVER_USER`, RBAC is missing, or another admission policy rejects `Unconfined` or `SYS_PTRACE`. Read the returned admission error. |
| Dry run passes, but the stored Pod value is unset or not `Unconfined` | The real carrier template did not request the profile, used the wrong container name, or a mutating policy changed it. |
| Stored Pod says `Unconfined`, but `/proc/1/attr/apparmor/current` is missing or not `unconfined` | The node/runtime did not expose or apply AppArmor as expected. Inspect Pod events, kubelet/runtime support, and node AppArmor enablement. |
| Runtime says `unconfined`, but Hostel does not reach `effective: "suite"` | AppArmor is no longer the blocker. Check `/v1/diagnostics`, bwrap startup logs, unprivileged user namespaces, and seccomp. |
| All checks pass | The request is admitted, the running container is unconfined, and bwrap suite isolation is operational. |

The exemption skips all Pod Security enforce, audit, and warn checks for Pods
created with that ServiceAccount identity; it is not limited to AppArmor or
`SYS_PTRACE`. Keep the ServiceAccount narrowly scoped and use this example only
for trusted carrier Pod creation. Other admission policies may still reject
the Pod.
