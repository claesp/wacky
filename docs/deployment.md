---
title: Deployment
---

# Deployment

How to build the container image, run it, and deploy it on Kubernetes with the
manifests in [`k8s/`](../k8s).

## The image

```bash
docker build -t wacky .
```

Three stages: a `golang:1.24-alpine` build, an `alpine:3.21` stage that collects
`git` and the shared objects it links against, and a `scratch` runtime holding
just those plus the binary. The result is about 13 MB.

wacky must run `git` to read a repository, so a completely empty image is
impossible — but nothing beyond git is carried. Templates, stylesheet, Markdown
renderer and timezone database are compiled into the binary.

### Running it

```bash
docker run --rm -p 8080:8080 -v ~/notes:/repo:ro wacky
```

The mounted repository can be read-only; the server never writes to it. Two
variables are already set in the image:

- `WACKY_ADDR=0.0.0.0:8080`, because the usual loopback default would be
  unreachable from outside the container.
- `WACKY_GIT_REPO=/repo`, the conventional mount point.

Flags are appended to the entrypoint, so `docker run … wacky -git-ref v1.0`
works, and every `WACKY_` variable can be passed with `-e`.

The image also sets `safe.directory=*` through `GIT_CONFIG_*`. A repository
mounted from the host belongs to a different uid than the one the container
runs as, and git refuses to touch it otherwise. wacky only ever reads — it runs
no hooks and writes nothing — so trusting the path the operator deliberately
mounted is safe.

### Health checks

The image has no shell and no HTTP client, so it probes itself:

```bash
wacky -health-check
```

This asks the process to `GET /healthz` on its own configured address and exit
`0` or `1`, rather than start a server. It honours `WACKY_ADDR`, rewriting a
wildcard listen address such as `0.0.0.0:8080` into something dialable. The
image declares it as a `HEALTHCHECK`, so `docker ps` reports health with no
extra tooling.

Podman writes OCI images by default and silently drops `HEALTHCHECK`. To keep
it, build with:

```bash
podman build --format docker -t wacky .
```

Under Kubernetes the flag is unnecessary: an `httpGet` probe is performed by
the kubelet from outside, which costs the container nothing. The manifests use
`httpGet` for exactly that reason.

## Kubernetes

```bash
kubectl apply -k k8s/
```

`k8s/` contains a ConfigMap, a Deployment, a Service, an optional Ingress and a
`kustomization.yaml` tying them together. `kubectl apply -f k8s/` works too, if
you would rather not use kustomize.

Before applying, change at least:

- `GITSYNC_REPO` and `GITSYNC_REF` in `k8s/configmap.yaml` — they ship pointing
  at a public example repository.
- the image in `k8s/kustomization.yaml`, which points at
  `ghcr.io/claesp/wacky:latest`. Prefer a digest in production: a tag can be
  moved underneath a running rollout.
- the host in `k8s/ingress.yaml`, or delete that file if the Service is reached
  some other way.

### How content reaches the server

wacky reads a repository from the filesystem and has no idea how it got there.
The Deployment fills that gap with
[git-sync](https://github.com/kubernetes/git-sync), the Kubernetes project's own
tool for the job:

1. An **init container** clones the repository into an `emptyDir` and exits
   (`GITSYNC_ONE_TIME`), so wacky never indexes an empty directory.
2. A **sidecar** keeps that clone current, polling every `GITSYNC_PERIOD`.
3. **wacky** serves the result, re-indexing every `WACKY_RELOAD_INTERVAL`.

Each replica keeps its own clone in an `emptyDir`, so there is no shared
storage to provision and a rescheduled Pod starts from a clean checkout.

The two intervals are independent: git-sync decides how quickly a new commit
reaches the Pod, and `WACKY_RELOAD_INTERVAL` how quickly the site notices.
Setting the latter far below the former only re-reads unchanged files. A change
is visible after roughly the sum of the two.

Point `WACKY_GIT_REPO` at `/repo/current`, the symlink git-sync maintains — not
at the directory behind it. Each sync checks the new commit out into a fresh
directory, repoints that symlink and deletes the old one; wacky re-resolves it
on every reload, so publishing needs no restart.

### Configuration

Everything lives in `k8s/configmap.yaml`: `GITSYNC_`-prefixed variables
configure git-sync, `WACKY_`-prefixed ones configure the server. Every flag in
the README's table has an environment variable, and all of them work here.

Two settings are deliberately not in the ConfigMap, because they describe the
Pod rather than the content: `GITSYNC_ROOT`/`GITSYNC_LINK` and
`WACKY_GIT_REPO`, all set on the containers themselves.

wacky reads its configuration once, at start-up, so editing the ConfigMap does
not by itself reach a running replica:

```bash
kubectl rollout restart deployment/wacky
```

### A private repository

git-sync authenticates over SSH or HTTPS. For SSH, mount a key and point
git-sync at it:

```yaml
env:
  - { name: GITSYNC_SSH, value: "true" }
  - { name: GITSYNC_SSH_KEY_FILE, value: /etc/git-secret/ssh }
volumeMounts:
  - { name: git-secret, mountPath: /etc/git-secret, readOnly: true }
```

with a `secret` volume holding the key, `defaultMode: 0400`. For HTTPS, set
`GITSYNC_USERNAME` and `GITSYNC_PASSWORD` from a Secret rather than the
ConfigMap. Nothing about wacky changes — it only ever sees a directory.

### Security context

Both images run unprivileged and the manifests keep them that way:
`runAsNonRoot` with uid 10001, `readOnlyRootFilesystem`, all capabilities
dropped, no privilege escalation, and the `RuntimeDefault` seccomp profile. The
Pod satisfies the *restricted* Pod Security Standard.

Two `emptyDir` volumes make the read-only root filesystem workable: `/repo` for
the clone and `/tmp` for git's scratch space. `HOME=/tmp` is set on the
git-sync containers because git writes its configuration under `HOME`.
`fsGroup: 10001` is what lets one container write the clone and another read
it.

wacky mounts `/repo` read-only, since it never writes there.

### Scaling

The server is stateless: every route is a safe `GET`, and each replica holds its
own index of its own clone. Replicas can be added freely, and no session
affinity is needed. The cost is that each one clones the repository separately,
so a large repository is worth keeping shallow — `GITSYNC_DEPTH: "1"` is the
default here.

`terminationGracePeriodSeconds: 30` sits well outside wacky's own shutdown
timeout, so in-flight requests finish before the Pod goes away.

### Verifying a deployment

```bash
kubectl rollout status deployment/wacky
kubectl port-forward svc/wacky 8080:80
curl -s localhost:8080/healthz
```

`/healthz` returns the indexed commit, the page count and the load time, which
between them answer both "is it up" and "is it serving what I published":

```json
{"status":"ok","pages":34,"files":825,"commit":"cf98d838…","ref":"working tree"}
```

If `pages` is `0`, the clone arrived but held no Markdown — check `GITSYNC_REF`
and whether the repository has `.md` files where you expect. If the commit is
stale, look at the git-sync sidecar:

```bash
kubectl logs deployment/wacky -c git-sync --tail=20
```
