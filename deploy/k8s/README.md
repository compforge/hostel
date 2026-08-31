# Kubernetes examples

This directory contains integration examples, not a complete production
deployment of hostel.

## Pod Security Admission exemption for suite

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

The exemption skips all Pod Security enforce, audit, and warn checks for Pods
created with that ServiceAccount identity; it is not limited to AppArmor. Keep
the ServiceAccount narrowly scoped and use this example only for trusted
carrier Pod creation. Other admission policies may still reject the Pod.
