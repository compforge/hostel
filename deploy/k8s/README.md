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

The exemption skips all Pod Security enforce, audit, and warn checks for Pods
created with that ServiceAccount identity; it is not limited to AppArmor. Keep
the ServiceAccount narrowly scoped and use this example only for trusted
carrier Pod creation. Other admission policies may still reject the Pod.
