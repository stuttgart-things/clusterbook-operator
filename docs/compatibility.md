# Compatibility

Which clusterbook server version pairs with which clusterbook-operator version. Features that talk to the clusterbook REST API have hard dependencies on server-side behaviour; this page lists the combinations that actually work end-to-end.

## Operator ↔ clusterbook server

| Operator version | Minimum clusterbook server | Notes |
|---|---|---|
| **v0.12.1 and later** | **v1.25.1** | First combination where `createDNS: true` reliably creates the PowerDNS record and returns an FQDN. v0.12.1 also adds `cr.Status.IP` idempotency, so the operator tolerates server-side listing drift cleanly. |
| v0.12.0 | v1.25.1 recommended | Ships all three CRDs, but the bundle is missing the `ClusterbookAllocation` CRD file (fixed in v0.12.1 / [#66](https://github.com/stuttgart-things/clusterbook-operator/pull/66)). Works if you install the CRD manually. |
| v0.11.x | ≤ v1.25.0 compatible | Only `ClusterbookCluster` + `ClusterbookLoadBalancer`. The old name-match idempotency pattern is fragile against server-side listing rewrites — upgrade to v0.12.1 when possible. |
| ≤ v0.10.x | ≤ v1.25.0 | Historic. `ClusterbookLoadBalancer` not yet present in v0.10 and earlier. |

## Feature-level requirements

### `createDNS: true` on any CRD

Needs **clusterbook ≥ v1.25.1**. On earlier server versions the `createDNS` flag was silently dropped on `POST /reserve` (upstream issue: camelCase vs. snake_case JSON tag mismatch), with three knock-on symptoms:

- `status: ASSIGNED` instead of `ASSIGNED:DNS` on the reservation record
- `cluster: "DNS"` written as the literal string instead of the requested name
- no PowerDNS record created → `status.fqdn` / `status.zone` on the CR never populate

If you see those symptoms, the fix is on the server side — upgrade clusterbook, then delete + re-apply the CR (or release the IP and let the reconciler re-reserve) to get a clean record.

### `ClusterbookAllocation` CRD

Needs **operator ≥ v0.12.1** for the CRD to ship inside the kustomize OCI bundle. For v0.12.0 you can apply the CRD manually from source (`kcl/crds/clusterbook.stuttgart-things.com_clusterbookallocations.yaml`) and it will work against the v0.12.0 binary.

### `ClusterbookCluster` enrich mode (`existingSecretRef`)

Needs operator ≥ v0.11.0. No server-side dependency — pure operator-side logic over existing Secret metadata.

### `ClusterbookLoadBalancer` `serviceRef` mode

Needs operator ≥ v0.11.0. No server-side dependency — patches `.spec.loadBalancerIP` on a Kubernetes Service directly.

## How to check what you're running

```bash
# Operator
kubectl -n clusterbook-system get deploy clusterbook-operator \
  -o jsonpath='{.spec.template.spec.containers[0].image}{"\n"}'

# Clusterbook server (from the web dashboard footer, or via API if exposed)
curl -s "$CLUSTERBOOK_URL/api/v1/version"
```

## Reporting incompatibilities

If you hit a behaviour that looks like a version mismatch (empty `status.fqdn` despite `createDNS: true`, stuck reconcile loops with `409` from the Reserve endpoint, reservations whose `cluster` field doesn't match what was requested), open an issue against whichever side looks responsible — server-side symptoms go to [stuttgart-things/clusterbook](https://github.com/stuttgart-things/clusterbook/issues), operator-side to this repo. The matrix above should help place it.
