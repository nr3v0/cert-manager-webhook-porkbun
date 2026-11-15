<p align="center">
  <img src="https://raw.githubusercontent.com/cert-manager/cert-manager/d53c0b9270f8cd90d908460d69502694e1838f5f/logo/logo-small.png" height="256" width="256" alt="cert-manager project logo" />
</p>

# Porkbun Webhook for cert-manager

> **This is a clone of [Talinx/cert-manager-webhook-porkbun](https://github.com/Talinx/cert-manager-webhook-porkbun)**
> by Christoph Raitzig ([@Talinx](https://github.com/Talinx)). That project builds on
> [Brandon Sprague's original Porkbun webhook](https://github.com/bcspragu/cert-manager-webhook-porkbun),
> which was created from [cert-manager/webhook-example](https://github.com/cert-manager/webhook-example).
> The upstream commit history is preserved here. See [NOTICE](NOTICE) and [Credits](#credits).
>
> Differences from upstream: deployed with **Kustomize** instead of Helm, runs on a
> **UBI 10 micro** image published to **quay.io/nr3v0**, uses the API group
> `porkbun.openshift.works`, and has hardened RBAC, pod security and API-client error handling.

An implementation of the cert-manager
[`webhook.Solver` interface](https://pkg.go.dev/github.com/cert-manager/cert-manager/pkg/acme/webhook#Solver)
for [Porkbun](https://porkbun.com/) DNS. It lets cert-manager solve ACME **DNS01**
challenges (needed, for example, for wildcard certificates) for domains hosted at Porkbun.

## Repository layout

| Path | What it is |
| --- | --- |
| [`main.go`](main.go) | The solver: `Present` creates the `_acme-challenge` TXT record, `CleanUp` deletes it, and `Initialize` builds the Kubernetes client that reads the API-key Secret. `cmd.RunWebhookServer` serves it as a Kubernetes aggregated API. |
| [`porkbun/`](porkbun/) | Small Porkbun DNS API v3 client (retrieve, create and delete records; ping). It has timeouts, escaped URL paths, HTTP status checks and typed `StatusError`s. |
| [`deploy/kustomize/base/`](deploy/kustomize/base/) | Plain Kubernetes manifests: ServiceAccount, RBAC, cert-manager PKI (self-signed CA and serving cert), Service, Deployment, APIService, and the `groupName` ConfigMap. |
| [`deploy/kustomize/components/group-name/`](deploy/kustomize/components/group-name/) | A Kustomize component that copies `groupName` from the ConfigMap into the Deployment, APIService and RBAC. |
| [`deploy/kustomize/overlays/openshift/`](deploy/kustomize/overlays/openshift/) | The overlay you deploy. It pins the image tag and is where you override `groupName`. |
| [`Dockerfile`](Dockerfile) | All-UBI 10 multi-stage build: `ubi10/go-toolset:latest` builder → static binary on `ubi10/ubi-micro`, plus the CA bundle from `ubi10/ubi-minimal`. Runs as non-root. |
| [`Makefile`](Makefile) | `test`, `test-conformance`, `build`, `push`, `rendered-manifest.yaml`, `deploy`. |
| [`.tekton/`](.tekton/), [`deploy/pipelines/`](deploy/pipelines/) | Pipelines-as-Code build: on push, OpenShift Pipelines builds the amd64+arm64 image and pushes it to Quay. |
| [`testdata/porkbun/`](testdata/porkbun/) | Sample solver config and Secret for the conformance suite. |
| [`main_test.go`](main_test.go), [`solver_test.go`](solver_test.go), [`porkbun/porkbun_test.go`](porkbun/porkbun_test.go) | The conformance suite (build tag `conformance`) and offline unit tests. |
| [`example/`](example/) | The in-memory DNS solver example from the upstream cert-manager template, kept for reference (its startup race is fixed). It is not part of the webhook binary. |

## How it fits together

### At runtime

```mermaid
flowchart LR
    subgraph cluster["Kubernetes / OpenShift cluster"]
        cert["Certificate<br/>(your app)"]
        issuer["ClusterIssuer / Issuer<br/>acme.solvers.dns01.webhook<br/>groupName: porkbun.openshift.works<br/>solverName: porkbun"]
        cm["cert-manager<br/>controller"]
        kas["kube-apiserver<br/>(aggregation layer)"]
        apisvc["APIService<br/>v1alpha1.porkbun.openshift.works"]
        subgraph ns["namespace: cert-manager"]
            svc["Service :443"]
            pod["webhook Pod :8443<br/>(this repo)"]
            secret[("Secret<br/>porkbun-secret<br/>API key + secret key")]
            tls[("Secret<br/>webhook-tls")]
            pki["cert-manager PKI<br/>self-signed → CA → serving cert"]
        end
    end
    porkbun["Porkbun API<br/>api.porkbun.com"]
    le["Let's Encrypt<br/>(ACME server)"]
    dns["Porkbun authoritative DNS"]

    cert --> cm
    issuer --> cm
    cm -- "POST ChallengePayload<br/>(Present / CleanUp)" --> kas
    kas -- "routes group via" --> apisvc
    apisvc --> svc --> pod
    pki -- issues --> tls
    tls -- "mounted at /tls" --> pod
    pki -. "CA injected (cainjector)" .-> apisvc
    pod -- "get" --> secret
    pod -- "HTTPS: create / delete<br/>_acme-challenge TXT" --> porkbun
    porkbun --> dns
    le -- "DNS01 lookup" --> dns
    cm -- "order / finalize" --> le
```

1. You create a `Certificate` that references an ACME `Issuer`/`ClusterIssuer` configured with the Porkbun webhook solver.
2. cert-manager places an ACME order and gets a DNS01 challenge. It sends a `ChallengePayload` to the API group `porkbun.openshift.works` through the kube-apiserver.
3. The `APIService` routes that group to this webhook's Service. The webhook's TLS certificate comes from the small cert-manager PKI in `pki.yaml`, and cainjector injects its CA into the APIService.
4. The webhook reads the Porkbun API keys from the Secret named in the solver config. It then creates the `_acme-challenge` TXT record through the Porkbun API (`Present`).
5. Let's Encrypt validates the record, cert-manager finalises the order, and the webhook deletes the record (`CleanUp`).

### Build and deploy

```mermaid
flowchart LR
    gh["git push / tag<br/>(GitHub)"] -- "webhook" --> pac["OpenShift Pipelines<br/>Pipelines-as-Code<br/>.tekton/"]
    pac -- "buildah amd64+arm64<br/>Dockerfile (UBI 10)" --> quay
    dev["make push IMAGE_TAG=x.y.z<br/>(manual)"] -- "podman amd64+arm64" --> quay[("quay.io/nr3v0/<br/>cert-manager-webhook-porkbun")]
    repo["deploy/kustomize<br/>base + components + overlays/openshift"] --> apply{"oc apply -k<br/>or Argo CD"}
    quay -- "image pulled by" --> apply
    apply --> cluster["cluster<br/>(namespace cert-manager)"]
```

## Installation

### Prerequisites

- cert-manager installed in the `cert-manager` namespace ([docs](https://cert-manager.io/docs/installation/); on OpenShift, the *cert-manager Operator for Red Hat OpenShift* works too).
  If cert-manager's ServiceAccount or namespace differs, patch `cert-manager-webhook-porkbun:domain-solver` in an overlay.
- A [Porkbun API key pair](https://kb.porkbun.com/article/190-getting-started-with-the-porkbun-api), with **API Access enabled on each domain** in the Porkbun dashboard.

### Deploy the webhook

```bash
oc apply -k deploy/kustomize/overlays/openshift      # or: kubectl apply -k ...
# or render first:  make rendered-manifest.yaml  →  _out/rendered-manifest.yaml
```

With Argo CD, point an `Application` at this repo with `path: deploy/kustomize/overlays/openshift`.

> Do **not** set a top-level `namespace:` in an overlay. It would move the
> `kube-system` RoleBinding the webhook needs for authentication. Create your own
> overlay (copy `overlays/openshift`) to change the image tag or `groupName`.

### Create the credentials Secret

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: porkbun-secret
  namespace: cert-manager   # the namespace of the Issuer, or cert-manager for a ClusterIssuer
type: Opaque
stringData:
  PORKBUN_API_KEY: pk1_yourapikeyhere
  PORKBUN_SECRET_API_KEY: sk1_yoursecretkeyhere
```

### Create an issuer

`groupName` must match the value in `deploy/kustomize/base/kustomization.yaml` (default `porkbun.openshift.works`):

```yaml
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: letsencrypt-porkbun
spec:
  acme:
    email: you@example.com
    server: https://acme-v02.api.letsencrypt.org/directory
    privateKeySecretRef:
      name: letsencrypt-porkbun-account
    solvers:
      - dns01:
          webhook:
            groupName: porkbun.openshift.works
            solverName: porkbun
            config:
              apiKey:
                name: porkbun-secret
                key: PORKBUN_API_KEY
              secretApiKey:
                name: porkbun-secret
                key: PORKBUN_SECRET_API_KEY
```

### Request a certificate

```yaml
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: example-cert
spec:
  secretName: example-cert
  issuerRef:
    name: letsencrypt-porkbun
    kind: ClusterIssuer
  dnsNames:
    - "example.com"
    - "*.example.com"
```

## Building the image

```bash
make build                       # podman (or docker) build -t quay.io/nr3v0/cert-manager-webhook-porkbun:latest .
make build IMAGE_TAG=1.2.0
```

Publish a multi-arch (amd64 + arm64) release from your workstation with podman:

```bash
podman login quay.io
make push IMAGE_TAG=1.2.0        # builds a manifest list and pushes it to quay.io/nr3v0/cert-manager-webhook-porkbun:1.2.0
```

Cross-building arm64 on an amd64 host needs `qemu-user-static` installed.

### Automated builds (OpenShift Pipelines)

Pushes to GitHub trigger a [Pipelines-as-Code](https://pipelinesascode.com/) run on an OpenShift
cluster. The run builds the amd64 and arm64 images with buildah and pushes a manifest list to Quay.
(Quay.io's own build triggers produce only one architecture.)

| Push | Image tags |
| --- | --- |
| `main` | `latest`, `sha-<commit>` |
| `dev` | `dev`, `sha-<commit>` |
| tag `v1.2.3` or `1.2.3` | `1.2.3`, `sha-<commit>` |

The pipeline is defined in [`.tekton/build-pipeline.yaml`](.tekton/build-pipeline.yaml), and
[`.tekton/push.yaml`](.tekton/push.yaml) says when it runs. It runs on an amd64 node. The Dockerfile
cross-compiles, so no QEMU is needed for the arm64 image.

One-time setup on the cluster:

1. **Install the Red Hat OpenShift Pipelines operator.** It includes Pipelines-as-Code and the `tkn pac` CLI plugin.
2. **Create a Quay robot account** with *Write* access to `quay.io/nr3v0/cert-manager-webhook-porkbun`.
   In Quay, go to Account settings → Robot Accounts → Create, then grant it the repository.
3. **Create the namespace and Repository, then the push secret.** The secret is not stored in git:
   ```bash
   oc apply -f deploy/pipelines/repository.yaml
   oc create secret docker-registry quay-push -n porkbun-ci \
     --docker-server=quay.io --docker-username='nr3v0+pipelines' --docker-password='<robot token>'
   ```
4. **Connect GitHub to the cluster.** Use one of:
   - **GitHub App** (recommended, cluster-wide): `tkn pac bootstrap`, then install the app on this repository.
   - **Repository webhook:** `tkn pac webhook add -n porkbun-ci cert-manager-webhook-porkbun`.
     It needs a GitHub token that can manage webhooks, and the cluster's Pipelines-as-Code route must be reachable from GitHub.
   - **Cluster not reachable from the internet** (for example, a lab cluster on a private network): run the
     [gosmee](https://github.com/chmouel/gosmee) relay from [`deploy/pipelines/gosmee.yaml`](deploy/pipelines/gosmee.yaml).
     It connects outbound to `hook.pipelinesascode.com` and forwards events to the in-cluster controller. Then set the GitHub
     webhook's *Payload URL* to the channel URL (Settings → Webhooks → Edit), keeping the same secret:
     ```bash
     URL=$(curl -s https://hook.pipelinesascode.com/new)
     oc create secret generic gosmee-channel -n porkbun-ci --from-literal=url="$URL"
     oc apply -f deploy/pipelines/gosmee.yaml
     echo "$URL"   # paste into the GitHub webhook Payload URL
     ```
     Events stay HMAC-signed with the webhook secret, so Pipelines-as-Code rejects anything forged through the relay.
5. **Push to `dev` or `main`.** Watch the run with `tkn pac logs -n porkbun-ci -L` or in the console under Pipelines.

The full runbook (setup, release steps and the fixes for problems hit along the way) is in [`docs/BUILDING.md`](docs/BUILDING.md).

## Running the tests

```bash
make test                        # offline unit tests (Porkbun client, solver, example), no credentials needed
```

All DNS01 webhooks **must** pass cert-manager's conformance suite. It runs against the real
Porkbun API and needs a zone you control:

```bash
cp testdata/porkbun/config.json.sample testdata/porkbun/config.json
cp testdata/porkbun/porkbun-credentials.yaml.sample testdata/porkbun/porkbun-credentials.yaml
# edit porkbun-credentials.yaml with your base64-encoded keys (both files are git-ignored)
TEST_ZONE_NAME=example.com. make test-conformance
```

## Credits

- **[Talinx/cert-manager-webhook-porkbun](https://github.com/Talinx/cert-manager-webhook-porkbun)**, by Christoph Raitzig, is the project this repository clones. It contributed the Helm chart (converted to Kustomize here), release automation and dependency updates.
- **[bcspragu/cert-manager-webhook-porkbun](https://github.com/bcspragu/cert-manager-webhook-porkbun)**, by Brandon Sprague, wrote the original Porkbun client and solver.
- **[cert-manager/webhook-example](https://github.com/cert-manager/webhook-example)**, by the cert-manager Authors, is the template and test harness.
- **[baarde/cert-manager-webhook-ovh](https://github.com/baarde/cert-manager-webhook-ovh)** was an inspiration for the original.

Licensed under the [Apache License 2.0](LICENSE).
