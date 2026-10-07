# fastci on Jenkins

fastci is a single, dependency-free Go binary with no plugin, agent image,
or special integration required to use it from Jenkins - it runs from an
ordinary shell step in any job type (Declarative Pipeline, Scripted
Pipeline, or a classic Freestyle project). This page is a practical setup
guide: installing it on an agent, wiring `--base` to the right branch for
both regular and pull-request builds, and the Jenkins-specific things
worth knowing (workspace persistence, credentials for `fastci analyze`,
ephemeral agents and the remote cache). If you haven't used fastci at all
yet, start with [the main README's Quickstart](../README.md#quickstart)
first.

## Contents

- [Installing fastci on an agent](#installing-fastci-on-an-agent)
- [Declarative Pipeline](#declarative-pipeline)
- [Freestyle projects](#freestyle-projects)
- [Getting `--base` right: regular branches vs. pull requests](#getting---base-right-regular-branches-vs-pull-requests)
- [Shallow clones](#shallow-clones)
- [Workspace persistence and the test-result cache](#workspace-persistence-and-the-test-result-cache)
- [`fastci analyze` and credentials](#fastci-analyze-and-credentials)
- [`fastci guard` as its own stage](#fastci-guard-as-its-own-stage)
- [Forwarding results to Jenkins' own test reporting](#forwarding-results-to-jenkins-own-test-reporting)

## Installing fastci on an agent

fastci itself needs a Go toolchain to install (not your project - see
[Quickstart](../README.md#quickstart) for why). If your agent image
already has `go` on `PATH`, a plain shell step is all you need:

```sh
go install github.com/hpscript/fastci/cmd/fastci@latest
export PATH="$PATH:$(go env GOPATH)/bin"
```

If it doesn't, install Go first (a `tool`/`jdk`-style Jenkins tool
installer for Go, a `golang` Docker agent image, or your base image's own
package manager), then run the two lines above. Either way, this only
needs to happen once per agent/image - a persistent agent that already has
the binary installed doesn't need to repeat it every build (see
[Workspace persistence](#workspace-persistence-and-the-test-result-cache)
below for the same consideration applied to the cache).

## Declarative Pipeline

```groovy
pipeline {
    agent any
    stages {
        stage('Install fastci') {
            steps {
                sh 'go install github.com/hpscript/fastci/cmd/fastci@latest'
            }
        }
        stage('Test') {
            steps {
                script {
                    def base = env.CHANGE_TARGET ? "origin/${env.CHANGE_TARGET}" : 'origin/main'
                    sh "PATH=\$PATH:\$(go env GOPATH)/bin fastci test --base ${base}"
                }
            }
        }
    }
}
```

`env.CHANGE_TARGET` is set by Jenkins' Multibranch Pipeline (via the
GitHub/Bitbucket/GitLab Branch Source plugin) when the current build is a
pull/merge request - it's that PR's actual target branch, the direct
equivalent of GitHub Actions' `github.base_ref` used in
[the main README's own CI example](../README.md#github-actions). Falling
back to `origin/main` covers an ordinary branch build (no PR involved),
where there's no single "base" other than whatever your repo's default
branch is - adjust the literal fallback if that's not `main`.

## Freestyle projects

The same idea, without Pipeline syntax - add an "Execute shell" build
step:

```sh
go install github.com/hpscript/fastci/cmd/fastci@latest
export PATH="$PATH:$(go env GOPATH)/bin"
fastci test --base origin/main
```

A Freestyle project has no equivalent of `CHANGE_TARGET` - if you need
PR-aware base-branch detection without a Multibranch Pipeline, use `fastci
local` instead (see the next section) rather than hand-rolling branch
detection in shell.

## Getting `--base` right: regular branches vs. pull requests

Three options, in increasing order of how much Jenkins-specific wiring
they need:

1. **`fastci local`, no flags.** It inspects `.github/workflows/*.yml` for
   the base branch GitHub Actions CI would use, and - this is the part
   that matters even if you don't use GitHub Actions at all - **falls
   back gracefully through your repository's actual default branch
   (`refs/remotes/origin/HEAD`) and finally the literal string `"main"`**
   if no workflow file exists. Verified directly: run it in a repo with
   no `.github/workflows` directory at all, and it still correctly prints
   something like `fastci: reproducing CI locally against origin/main
   (no workflow specifies a branch and the repository's default branch
   could not be determined, falling back to "main")` and proceeds. This
   is the simplest option for an ordinary branch build, but it depends on
   `origin/HEAD` actually being set on the agent's checkout to resolve
   anything better than the literal `"main"` fallback - a plain `git
   clone` sets this automatically; a checkout assembled by hand (`git
   init` + `git remote add` + `git fetch`, as some custom Jenkins
   SCM setups do) does not, and needs `git remote set-head origin -a` to
   get the same benefit.
2. **`fastci test --base origin/$CHANGE_TARGET`**, for a Multibranch
   Pipeline building a pull/merge request (see the Declarative Pipeline
   example above) - the most precise option, since it's reading the
   actual PR target Jenkins itself resolved, not guessing.
3. **`fastci test --base origin/<branch>`** with the branch name hard-coded
   or computed however your own Jenkinsfile already determines it, for
   any other setup.

All three ultimately just set fastci's own `--base`; none of this is
fastci-specific plumbing beyond picking which ref string to pass.

## Shallow clones

Jenkins' Git plugin does a **full clone by default** - unlike some other
CI systems, there's usually nothing to configure here at all. If you (or
your Jenkins administrator) have explicitly added the "Shallow Clone"
additional behavior to the Git SCM configuration for faster checkouts,
the same automatic recovery documented for GitHub Actions applies
identically, since it's generic git handling, not GitHub-specific: fastci
detects an unresolvable merge base caused by a shallow checkout and runs
the equivalent of `git fetch --unshallow` (and/or fetches the missing
base ref) before diffing, at the cost of one extra fetch on the first
invocation - see
[the note under GitHub Actions](../README.md#github-actions) in the main
README for the full detail (it applies to Jenkins verbatim).

## Workspace persistence and the test-result cache

This is a genuine Jenkins-specific advantage worth knowing about: a
Jenkins agent's workspace directory **persists across builds on the same
node by default** (unlike, e.g., GitHub Actions' hosted runners, which are
a fresh VM every run) - unless your job explicitly wipes it (`cleanWs()`,
or "Delete workspace before build starts"). That means
[fastci's local test-result cache](../README.md#test-result-cache-local-and-distributed)
(`.fastci-cache/`, already git-ignored automatically) can accumulate real,
useful hits across builds on that same agent for free, with no extra
setup - a second build that re-selects a target whose content hasn't
actually changed since the last pass can skip it entirely.

If your agents *are* ephemeral (Kubernetes pod agents, Docker agents torn
down after each build, a pool where you can't rely on landing on the same
node twice), that local persistence doesn't apply, and
[`FASTCI_REMOTE_CACHE_URL`](../README.md#test-result-cache-local-and-distributed)
is the right tool instead - point every agent at the same shared HTTP
endpoint (a small self-hosted server, or an S3-compatible bucket via
presigned URLs your Jenkinsfile generates) as a Jenkins credential or
plain job parameter:

```groovy
environment {
    FASTCI_REMOTE_CACHE_URL = 'https://cache.example.com/fastci'
}
```

See
[the circuit breaker writeup](remote-cache-circuit-breaker.md) too if
you're running this across many agents against a remote that might be
flaky - it's automatic, nothing to configure, but worth knowing what it
does.

## `fastci analyze` and credentials

[`fastci analyze`](../README.md#fastci-analyze) needs `ANTHROPIC_API_KEY`
in the environment and makes a real, billed request to Anthropic's API -
bind it as a Jenkins **secret text credential**, never a plain
environment variable in the Jenkinsfile itself:

```groovy
pipeline {
    agent any
    stages {
        stage('Diagnose failure') {
            when { expression { currentBuild.result == 'FAILURE' } }
            steps {
                withCredentials([string(credentialsId: 'anthropic-api-key', variable: 'ANTHROPIC_API_KEY')]) {
                    sh 'fastci analyze'
                }
            }
        }
    }
}
```

Remember it only has something to diagnose after a `fastci test` run that
actually failed in the same workspace - see
[Troubleshooting](../README.md#troubleshooting) in the main README if it
says there's nothing to analyze.

## `fastci guard` as its own stage

[`fastci guard`](../README.md#fastci-guard) (supply-chain vulnerability
scanning) is independent of `fastci test` and makes sense as its own
stage, since it exits non-zero on a real finding the same way a failed
test stage would:

```groovy
stage('Security scan') {
    steps {
        sh 'fastci guard'
    }
}
```

Install whichever underlying scanners your project's ecosystem needs
(`govulncheck`, `pip-audit`, `cargo-audit`, ...) on the agent beforehand -
see the install column in
[the scanner table](../README.md#fastci-guard) or the fuller
[guard user guide](guard-guide.md) for exact commands per ecosystem. A
scanner that isn't installed is skipped with an install hint printed, not
a stage failure, so an incremental rollout (add one scanner at a time) is
safe.

## Forwarding results to Jenkins' own test reporting

fastci doesn't generate JUnit XML itself - it runs each ecosystem's own
test runner essentially as-is, selecting which targets get passed to it.
Flags after `--` [are forwarded to that underlying runner
unchanged](../README.md#usage), so the usual way to get JUnit output for
Jenkins' `junit` step still works, e.g. for pytest:

```sh
fastci test --base origin/main -- --junitxml=results.xml
```

```groovy
post {
    always {
        junit 'results.xml'
    }
}
```

The equivalent for Jest/Vitest is a JUnit reporter configured in
`jest.config.*`/`vitest.config.*` (or passed via `--`, if the reporter
accepts a CLI flag) - again, forwarded through unmodified, since fastci
never inspects or rewrites the underlying runner's own flags.
