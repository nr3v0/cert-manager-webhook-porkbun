# Building the image with OpenShift Pipelines

This document describes how the automated multi-arch (amd64 + arm64) image builds were set up for
this repository, and the problems that had to be solved to make them work. The short version lives
in the README under "Automated builds"; this is the full runbook.

```
GitHub push ──▶ webhook ──▶ hook.pipelinesascode.com (gosmee relay channel)
                                     │  (outbound connection from the cluster)
                                     ▼
                       gosmee-client pod (porkbun-ci)
                                     │
                                     ▼
              Pipelines-as-Code controller (openshift-pipelines)
                                     │  reads .tekton/ from the pushed commit
                                     ▼
          PipelineRun: fetch-source (git-clone) ──▶ build-push (buildah)
                                                          │
                                                          ▼
                              quay.io/nr3v0/cert-manager-webhook-porkbun
                              (manifest list: linux/amd64 + linux/arm64)
```

## What gets built

| Git push | Image tags pushed |
| --- | --- |
| branch `main` | `latest`, `sha-<7-char commit>` |
| branch `dev` | `dev`, `sha-<7-char commit>` |
| tag `v1.2.3` (or `1.2.3`) | `1.2.3`, `sha-<7-char commit>` |

Every tag is a manifest list covering `linux/amd64` and `linux/arm64`.

## Files involved

| File | Purpose |
| --- | --- |
| [`.tekton/push.yaml`](../.tekton/push.yaml) | The `PipelineRun` template. Its `on-cel-expression` annotation decides which pushes trigger a run. |
| [`.tekton/build-pipeline.yaml`](../.tekton/build-pipeline.yaml) | The `Pipeline`: clone the repo, then build each platform with buildah and push the manifest list. |
| [`deploy/pipelines/repository.yaml`](../deploy/pipelines/repository.yaml) | The `porkbun-ci` namespace and the Pipelines-as-Code `Repository` that maps the GitHub repo to it. |
| [`deploy/pipelines/gosmee.yaml`](../deploy/pipelines/gosmee.yaml) | The webhook relay client, for a cluster GitHub can't reach. |
| [`Dockerfile`](../Dockerfile) | UBI 10 multi-stage build. The Go stage cross-compiles, so arm64 needs no emulation. |

> **Note: there is no `Pipeline` object in the cluster.** Pipelines-as-Code reads `.tekton/` from the
> pushed commit, resolves `pipelineRef` against the files there, and embeds the whole
> `pipelineSpec` into each `PipelineRun`. The console therefore shows PipelineRuns with no
> Pipeline. That is expected, and it means pipeline changes take effect on the next push, with no
> `oc apply` needed.

## One-time setup

All commands run against the build cluster (`export KUBECONFIG=...`).

### 1. Install OpenShift Pipelines

Install the **Red Hat OpenShift Pipelines** operator from OperatorHub. It includes Tekton,
Pipelines-as-Code (the controller in `openshift-pipelines`) and the `pipeline` ServiceAccount that
is created in every namespace. Install the `tkn` CLI with its `pac` plugin on your workstation.

### 2. Create the Quay robot account

In Quay go to **Account settings → Robot Accounts → Create** (for example `nr3v0+pipelines`) and
give it **Write** permission on `quay.io/nr3v0/cert-manager-webhook-porkbun`. Make the repository
public if anonymous pulls are wanted.

### 3. Create the namespace, Repository and push secret

```bash
oc apply -f deploy/pipelines/repository.yaml

# The robot token never goes into git.
oc create secret docker-registry quay-push -n porkbun-ci \
  --docker-server=quay.io \
  --docker-username='nr3v0+pipelines' \
  --docker-password='<robot token>'
```

The pipeline mounts this secret as the `quay-auth` workspace and points buildah's
`REGISTRY_AUTH_FILE` at its `.dockerconfigjson`.

### 4. Connect GitHub to the cluster

Pipelines-as-Code needs to receive GitHub push events. Pick one:

- **GitHub App** (cluster-wide): `tkn pac bootstrap`, then install the app on the repository.
- **Repository webhook** (cluster reachable from the internet):
  `tkn pac webhook add -n porkbun-ci cert-manager-webhook-porkbun`.
- **Private cluster** (used for snok, which lives on 192.168.x): a repository webhook delivered
  through the [gosmee](https://github.com/chmouel/gosmee) relay.

For the private-cluster case:

```bash
# 1. Create a relay channel. The URL is effectively a secret; keep it out of git.
URL=$(curl -s https://hook.pipelinesascode.com/new)
oc create secret generic gosmee-channel -n porkbun-ci --from-literal=url="$URL"

# 2. Run the relay client. It connects outbound to the channel and forwards
#    events to the in-cluster controller Service.
oc apply -f deploy/pipelines/gosmee.yaml

# 3. Create the webhook and its secret on the Repository (needs a GitHub token
#    that can manage webhooks), or create it by hand in GitHub.
tkn pac webhook add -n porkbun-ci cert-manager-webhook-porkbun

# 4. In GitHub: Settings → Webhooks → Edit, set the Payload URL to $URL and
#    keep the existing secret. Content type: application/json. Event: push.
echo "$URL"
```

Events through the relay are still HMAC-signed with the webhook secret, and Pipelines-as-Code
verifies the signature, so anyone who learns the channel URL can't forge a build.

### 5. Push and verify

```bash
git push origin dev

tkn pac logs -n porkbun-ci -L                  # follow the latest run
oc get pipelineruns -n porkbun-ci              # status
skopeo inspect --raw docker://quay.io/nr3v0/cert-manager-webhook-porkbun:dev | jq '.manifests[].platform'
```

GitHub's **Recent Deliveries** tab on the webhook should show `202` responses. A healthy run takes
about 5 minutes: around 10 s to clone and about 4.5 min to build both architectures and push.

## Problems solved along the way

These are the failures hit while bringing the pipeline up, and the fix for each. Check here first
if a build breaks.

### Webhook deliveries time out

**Symptom:** GitHub shows failed deliveries, and no PipelineRun appears.
**Cause:** the cluster's Pipelines-as-Code route is on a private network.
**Fix:** the gosmee relay (step 4). Check that the `gosmee-client` pod is running and that its log
shows forwarded events.

### `exec /usr/bin/fuse-overlayfs: exit status 1`

```
Error: mounting an overlay over build context directory: creating overlay scaffolding
for build context directory: exec /usr/bin/fuse-overlayfs: exit status 1
```

**Cause:** current buildah always overlay-mounts the build context directory, even with
`STORAGE_DRIVER=vfs`. Without `CAP_SYS_ADMIN` it falls back to fuse-overlayfs, which needs
`/dev/fuse`. The pod didn't have that device. This has nothing to do with multi-arch builds;
building one platform at a time fails the same way.
**Fix:** annotate the build TaskRun pod so CRI-O passes the device through. The pod stays
unprivileged:

```yaml
taskSpec:
  metadata:
    annotations:
      io.kubernetes.cri-o.Devices: /dev/fuse
```

The step also keeps `STORAGE_DRIVER=vfs`, `BUILDAH_ISOLATION=chroot` and the `SETFCAP` capability.
That matches the stock OpenShift Pipelines buildah task, which the `pipelines-scc` SCC allows for the
`pipeline` ServiceAccount.

### The build runs for 15+ minutes and never finishes

**Symptom:** `go build` inside the image build makes almost no progress, and the pod sits at its
memory limit.
**Cause:** Go runs one compile per CPU by default. On a ~48-core node that meant about 40 parallel
compiles of the Kubernetes and cert-manager dependency tree, inside a 4 GiB limit, so the kernel
spent its time reclaiming memory instead of compiling.
**Fix:** cap the parallelism and raise the limit:

- The Dockerfile accepts `ARG GOFLAGS=""`, which `go build` reads from the environment.
- The pipeline passes `--build-arg GOFLAGS=-p=8`.
- The step requests 2 CPU / 2 GiB and is limited to 8 GiB of memory.

Build time dropped to about 4.5 minutes for both architectures. Local `make build` is unaffected:
`GOFLAGS` is empty there and Go uses its defaults.

### No QEMU needed for arm64

The Go builder stage uses `FROM --platform=$BUILDPLATFORM` and cross-compiles with
`GOOS`/`GOARCH` from `TARGETOS`/`TARGETARCH`. The arm64 runtime stage only `COPY`s files and runs no
`RUN` steps, so the whole build runs natively on an amd64 node. `push.yaml` pins the run to
`kubernetes.io/arch: amd64` nodes.

### `ubi-micro` has no CA bundle

`ubi10/ubi-micro` doesn't ship `ca-certificates`, so HTTPS calls to `api.porkbun.com` would fail.
The Dockerfile copies `tls-ca-bundle.pem` from a `ubi10/ubi-minimal` stage and sets
`SSL_CERT_FILE`.

### API server restarts interrupt runs (snok only)

On the single-node snok cluster, the kube-apiserver operator keeps rolling out new revisions
("required configmap/config has changed"). Each rollout takes the API down for 1–2 minutes. A run
that is already executing usually survives, but `tkn`/`oc` watches drop with `connection refused`.
That is a cluster issue, not a pipeline issue. Re-run with
`tkn pac logs -n porkbun-ci -L` once the API is back. To rebuild without a new commit, push an
empty commit or start a new run from the console.

## Cutting a release

1. Update the image tag in `deploy/kustomize/base/kustomization.yaml` and
   `deploy/kustomize/overlays/openshift/kustomization.yaml` (and the examples in the README).
2. Commit and push the branch.
3. Tag the commit and push the tag. The tag push triggers a build that publishes the version tag:
   ```bash
   git tag -a v1.2.3 -m "v1.2.3"
   git push origin v1.2.3
   ```
4. Confirm that `quay.io/nr3v0/cert-manager-webhook-porkbun:1.2.3` lists both platforms, then deploy
   with `make deploy` (or `oc apply -k deploy/kustomize/overlays/openshift`).

## Building by hand

The pipeline isn't required. From a workstation with podman:

```bash
make build IMAGE_TAG=1.2.3     # local multi-arch manifest
make push  IMAGE_TAG=1.2.3     # push the manifest list to Quay (needs podman login quay.io)
```
