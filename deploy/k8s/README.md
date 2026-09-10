# Kubernetes examples

This directory contains integration examples, not a complete production
deployment of hostel.

## Permission model

Sandbox-server usually creates the carrier Pod that runs Hostel. Keep the
three permission layers separate:

| Layer | Requirement |
| --- | --- |
| sandbox-server ServiceAccount | RBAC permission to create Pods in the carrier namespace; a narrowly scoped PSA/admission exception when the selected Pod security setting is otherwise denied. |
| Created Hostel container | Request `appArmorProfile.type: Unconfined` for bubblewrap, or `capabilities.add: ["SYS_PTRACE"]` for PRoot. A Pod only needs the setting for the backend selected by its creator. |
| Carrier node/runtime | Actually support the requested operation: user namespaces and mount policy for bubblewrap, or ptrace for PRoot. |

The sandbox-server container itself does not need either setting. Its
ServiceAccount is a Kubernetes API identity; Linux capabilities and AppArmor
belong to the Hostel container in the Pod it creates. Do not add `privileged`,
`hostPID`, or `SYS_ADMIN` for either flow.

## Enable bubblewrap suite isolation

[`pod-security-admission-exemption.yaml`](pod-security-admission-exemption.yaml)
applies when a cluster enforces the Baseline or Restricted Pod Security
Standard, while a trusted sandbox-server directly creates carrier Pods that
request `appArmorProfile.type: Unconfined` so bubblewrap can provide suite
isolation.

The file is kube-apiserver configuration, not an object accepted by `kubectl
apply`. A cluster administrator must merge its `exemptions.usernames` entry
into the existing Pod Security `AdmissionConfiguration`, replace
`<sandbox-server-namespace>` with the deployment's actual namespace, and roll
out that control-plane configuration using the cluster's normal procedure. The
carrier Pod template must still explicitly request the `Unconfined` AppArmor
profile.

Set the namespaces and authenticated ServiceAccount identity used by the
examples:

```bash
SANDBOX_SERVER_NAMESPACE="<sandbox-server-namespace>"
CARRIER_NAMESPACE="<carrier-namespace>"
SANDBOX_SERVER_USER="system:serviceaccount:${SANDBOX_SERVER_NAMESPACE}:sandbox-server"
```

Check that the ServiceAccount's RBAC permits direct carrier Pod creation:

```bash
kubectl auth can-i create pods \
  --namespace "${CARRIER_NAMESPACE}" \
  --as "${SANDBOX_SERVER_USER}"
```

After the cluster administrator rolls out the admission configuration, verify
the exemption through a server-side dry run. This exercises authentication,
RBAC, and admission without creating a Pod:

```bash
kubectl create --dry-run=server --output=yaml \
  --namespace "${CARRIER_NAMESPACE}" \
  --as "${SANDBOX_SERVER_USER}" \
  --filename - <<'EOF'
apiVersion: v1
kind: Pod
metadata:
  name: hostel-apparmor-admission-check
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
          drop: ["ALL"]
        appArmorProfile:
          type: Unconfined
EOF
```

A successful dry run proves that the API server accepts this ServiceAccount
creating a Pod that requests `Unconfined`. It does not prove that the real
carrier template requests the profile or that the container runtime applied
it. Verify a running carrier Pod separately:

```bash
POD="<running-carrier-pod>"
CONTAINER="<hostel-container-name>"
```

First inspect the Pod stored by the API server. For Kubernetes 1.30 and later,
the result must be `Unconfined`:

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
  -- curl --fail --silent --show-error http://127.0.0.1:8872/v1/diagnostics | \
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
check above independently confirms that the runtime applied `Unconfined`.

Use the first failing layer to locate the problem:

| Result | Meaning / next check |
| --- | --- |
| Server dry run is denied | The PSA exemption is not active for the exact `SANDBOX_SERVER_USER`, RBAC is missing, or another admission policy rejects `Unconfined`. Read the returned admission error. |
| Dry run passes, but the stored Pod value is unset or not `Unconfined` | The real carrier template did not request the profile, used the wrong container name, or a mutating policy changed it. |
| Stored Pod says `Unconfined`, but `/proc/1/attr/apparmor/current` is missing or not `unconfined` | The node/runtime did not expose or apply AppArmor as expected. Inspect Pod events, kubelet/runtime support, and node AppArmor enablement. |
| Runtime says `unconfined`, but Hostel does not reach `effective: "suite"` | AppArmor is no longer the blocker. Check `/v1/diagnostics`, bwrap startup logs, unprivileged user namespaces, and seccomp. |
| All checks pass | The request is admitted, the running container is unconfined, and bwrap suite isolation is operational. |

The exemption skips all Pod Security enforce, audit, and warn checks for Pods
created with that ServiceAccount identity; it is not limited to AppArmor. Keep
the ServiceAccount narrowly scoped and use this example only for trusted
carrier Pod creation. Other admission policies may still reject the Pod.

## Enable ptrace for the PRoot workspace view

The standard image places `proot` in `PATH`; there is no `HOSTEL_PROOT`
setting. When a mount workspace view is unavailable, Hostel can select PRoot
after both the ptrace and PRoot smoke probes succeed.

Apply the permission only to the Hostel container in carrier Pods created for
this backend:

```yaml
spec:
  containers:
    - name: hostel
      securityContext:
        capabilities:
          add: ["SYS_PTRACE"]
```

Merge this addition with the template's existing capability `drop`, seccomp,
and other security settings. Adding `SYS_PTRACE` does not change an existing
Pod; recreate carrier Pods through sandbox-server after updating its template.

The creator-side steps are:

1. Give the sandbox-server ServiceAccount RBAC permission to create Pods in the
   carrier namespace.
2. If PSA Baseline/Restricted or another admission policy rejects
   `SYS_PTRACE`, exempt or allowlist only the exact creator identity.
3. Have sandbox-server add `SYS_PTRACE` only when it selects the PRoot-capable
   template.
4. Verify the running container through `/v1/diagnostics`; admission success
   alone does not prove ptrace works.

Use a server-side dry run to check the exact creator identity without creating
a Pod:

```bash
SANDBOX_SERVER_NAMESPACE="<sandbox-server-namespace>"
CARRIER_NAMESPACE="<carrier-namespace>"
SANDBOX_SERVER_USER="system:serviceaccount:${SANDBOX_SERVER_NAMESPACE}:sandbox-server"

kubectl create --dry-run=server --output=yaml \
  --namespace "${CARRIER_NAMESPACE}" \
  --as "${SANDBOX_SERVER_USER}" \
  --filename - <<'EOF'
apiVersion: v1
kind: Pod
metadata:
  name: hostel-ptrace-admission-check
spec:
  restartPolicy: Never
  containers:
    - name: hostel
      image: registry.k8s.io/pause:3.10
      securityContext:
        capabilities:
          add: ["SYS_PTRACE"]
EOF
```

The exemption file in this directory exempts the named ServiceAccount from all
PSA checks; it does not mutate the Pod or limit the exemption to one capability.
Keep that creator narrowly scoped. Other admission policies may require their
own equivalent allowlist.

Verify the stored request and then the running behavior:

```bash
POD="<running-carrier-pod>"
CONTAINER="<hostel-container-name>"

kubectl get pod "${POD}" \
  --namespace "${CARRIER_NAMESPACE}" \
  --output json | jq --raw-output --arg container "${CONTAINER}" \
  '.spec.containers[] | select(.name == $container) | .securityContext.capabilities.add // []'

kubectl exec "${POD}" \
  --namespace "${CARRIER_NAMESPACE}" \
  --container "${CONTAINER}" \
  -- curl --fail --silent --show-error \
  http://127.0.0.1:8872/v1/diagnostics | \
  jq '{ptrace: .probes.ptrace, proot: .probes.proot, workspace_view}'
```

The image contract is visible when `proot` reports `exists: true`,
`executable: true`, and a non-empty `resolved_path`. Ptrace is usable when its
probe has `attempted: true`, `exit_code: 0`, and an empty `error`. Below suite,
a usable PRoot view additionally reports the same successful execution facts
for `proot` and `workspace_view={"mode":"proot","available":true}`. If suite
is already selected, PRoot remains discovered but is not smoke-tested, so
`attempted: false` is expected.

## 文件隔离机制的部署条件

本节只描述文件隔离机制的部署条件；网络 netns 有独立的权限与组合要求，见
[网络设计](../../docs/network.md) 和 [隔离总览](../../docs/isolation.md)。


先区分两类权限：Hostel/Bedbox **运行时隔离依赖 Pod securityContext、节点内核、容器运行时和准入策略**；ServiceAccount 的 RBAC 只决定 sandbox-server 能否创建/管理 Pod，不能单独授权某个 AppArmor profile。

| Level / 机制 | Hostel container 的要求 | 节点 / runtime 要求 | Pod Security Admission |
|---|---|---|---|
| `dorm` / direct | 无额外 capability、无需 privileged、无需 AppArmor 豁免；实际读写能力仍由 container UID、capability、只读根和 volume 权限决定 | 普通 Linux Pod 即可 | 可适配 Baseline/Restricted；是否能非 root 运行取决于镜像和 volume 属主，不是 Dorm 机制要求 |
| `room` / Landlock | 无额外 capability、无需 privileged；可保持 `RuntimeDefault` AppArmor/seccomp，只要实际 smoke 通过 | Linux ≥5.13、内核编译并启用 Landlock LSM，seccomp 不得拦截所需 Landlock syscall | 机制本身可适配 Restricted；当前镜像/volume 若仍要求 root，需另行收敛运行用户 |
| `room` / UID | daemon 以 root 运行，或至少具备 `CHOWN`、`SETUID`、`SETGID`；daemon 还需 `DAC_OVERRIDE`（非 root 形态可用 `DAC_READ_SEARCH`）读取归属不同 UID 的 BedFS | seccomp 必须允许 setgroups/setgid/setuid/chown；保持 `fs.protected_hardlinks=1`，并核对 UID 段 | 可适配 Baseline；不适配 Restricted（Restricted 只允许加回 `NET_BIND_SERVICE`，且要求非 root） |
| `suite` / bwrap | 推荐 `privileged: false`、drop `ALL`、不加 `CAP_SYS_ADMIN`；AppArmor 必须为 `Unconfined`，或节点预装一个允许 bwrap userns/mount 操作的 `Localhost` profile；seccomp profile 也必须允许实际 smoke | bwrap 可执行；内核允许进程创建 user namespace，并允许其中的 mount namespace 操作 | `Unconfined` 不满足 Baseline/Restricted，需 namespace/runtimeClass/user 级豁免或自定义 admission；合适的 `Localhost` profile 可满足 AppArmor 这一项 |

Suite 推荐的 container 片段（Kubernetes 1.30+ 原生 AppArmor 字段）：

```yaml
securityContext:
  privileged: false
  allowPrivilegeEscalation: false
  capabilities:
    drop: ["ALL"]
  seccompProfile:
    type: RuntimeDefault
  appArmorProfile:
    type: Unconfined
```

Pod Security Admission 豁免示例见 [`deploy/k8s/pod-security-admission-exemption.yaml`](pod-security-admission-exemption.yaml)；它由集群管理员合入 kube-apiserver 的 `AdmissionConfiguration`，不能通过 `kubectl apply` 安装。

若 `RuntimeDefault` seccomp 仍拦截 bwrap，使用节点预装且只放行所需 syscall 的 `Localhost` seccomp profile；不要直接把 `privileged: true` 当成 suite 的默认解法。AppArmor 也优先选择精确适配 bwrap 的 `Localhost` profile；无法维护该 profile 时才使用 `Unconfined`。Kubernetes 官方说明 1.30 前 AppArmor 通过 annotation 指定，当前 API 支持 `RuntimeDefault`、`Localhost`、`Unconfined`；Pod Security Baseline 只允许前两者。

UID room 的最小 capability 片段：

```yaml
securityContext:
  runAsUser: 0
  allowPrivilegeEscalation: false
  capabilities:
    drop: ["ALL"]
    add: ["CHOWN", "DAC_OVERRIDE", "SETGID", "SETUID"]
  seccompProfile:
    type: RuntimeDefault
```

Hostel carrier 本身不调用 Kubernetes API，建议 `automountServiceAccountToken: false`。负责创建 carrier Pod 的 sandbox-server ServiceAccount 只需要目标 namespace 中 Pod 生命周期所需的普通 RBAC（如 create/get/list/watch/delete）；Pod 能否声明 `Unconfined` 或额外 capability 最终由 Pod Security Admission、ValidatingAdmissionPolicy/Gatekeeper/Kyverno 等准入层决定，而不是由 RBAC verb 决定。

参考 Kubernetes 官方文档：[AppArmor](https://kubernetes.io/docs/tutorials/security/apparmor/)、[Pod Security Standards](https://kubernetes.io/docs/concepts/security/pod-security-standards/)、[Security Context](https://kubernetes.io/docs/tasks/configure-pod-container/security-context/)。
