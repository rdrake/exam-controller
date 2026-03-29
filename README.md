# exam-controller

A Kubernetes operator that automates pen-testing exams for university courses. Define your exam once -- students, schedule, container image -- and the controller handles provisioning, email delivery, network isolation, and teardown. No manual intervention required on exam day.

## The problem

Running a hands-on penetration testing exam means giving each student their own isolated instance of a vulnerable application. Without automation, this involves:

- Manually spinning up dozens of containers
- Configuring network policies so students can't interfere with each other
- Sending each student their unique URL
- Opening access at the right time
- Cutting access when time is up
- Keeping instances around for grading
- Cleaning everything up afterward

The exam-controller does all of this on a schedule you define.

## What it does

You write a YAML file listing your students, pick a container image, and set the exam times. The controller takes it from there:

```
Pending --> Provisioning --> Ready --> Unlocked --> Locked --> TearingDown
```

| Phase | What happens |
|---|---|
| **Pending** | Waiting for the provisioning window to open. |
| **Provisioning** | Creates an isolated container, service, and network policies for each student. Provisions spare instances for replacements. |
| **Ready** | All instances healthy. Sends each student their unique URL by email. Runs a smoke test to verify network isolation. |
| **Unlocked** | Exam is live. Students can access their instances at `https://<slug>.<domain>`. You receive a notification. |
| **Locked** | Time's up. Student access is cut off. Instances stay running so you can review their work. |
| **TearingDown** | Retention window expired. Everything is cleaned up automatically. |

You receive email notifications at each major transition. If a student's email bounces, you get the list of failures so you can follow up.

## Example

```yaml
apiVersion: exam.otu.ca/v1alpha1
kind: Exam
metadata:
  name: sofe4790u-midterm
  namespace: exam-system
spec:
  template:
    image: registry.example.com/vuln-app:v2.1
    port: 8080
  schedule:
    unlock: "2026-04-10T14:00:00-04:00"
    duration: "2h"
    timeMultiplier: 1.5
    provisionBefore: "1h"
    retention: "24h"
  email:
    before: "30m"
    instructorEmail: instructor@ontariotechu.net
    secretRef: exam-smtp-credentials
    from: "noreply@otu.ca"
    subject: "SOFE4790U Midterm - Your Exam Instance"
  students:
    - id: john.smith
      email: john.smith@ontariotechu.net
    - id: jane.doe
      email: jane.doe@ontariotechu.net
  spares: 2
  domain: exam.otu.ca
  ingressTLS:
    secretName: exam-wildcard-tls
```

For a 2:00 PM exam, this provisions instances at 1:00 PM, emails students at 1:30 PM, smoke-tests at 1:55 PM, unlocks at 2:00 PM, locks at 5:00 PM, and tears down the next day. You don't need to be online for any of it.

## Key features

- **Per-student isolation** -- Each student gets their own container, service, and network policies in a dedicated namespace.
- **Automatic network enforcement** -- Deny-all policies block all traffic by default. Ingress is opened only during the exam window and only from the ingress controller.
- **Cilium auto-detection** -- Uses CiliumNetworkPolicy with L7 visibility when available, falls back to vanilla NetworkPolicy otherwise.
- **Smoke testing** -- Optional dry-run checks verify instance health and network policy enforcement before the exam starts.
- **Spare instances** -- Pre-provisioned replacements ready to hand out if a student's instance fails.
- **Rate-limited email** -- Sends student credentials on a schedule with retry, respecting SMTP rate limits.
- **Prometheus metrics** -- 12 metrics covering reconcile performance, instance health, email delivery, and countdown timers. Metric series are cleaned up on teardown.
- **Crash-safe** -- The controller resumes from the current phase on restart. It won't re-send emails or re-provision instances that already exist.

## Installation

Your platform team handles this. Instructors only need to write the exam YAML.

### Helm

```sh
helm install exam-controller \
  oci://ghcr.io/rdrake/charts/exam-controller \
  --namespace exam-controller-system --create-namespace \
  --version 0.1.0
```

### Kustomize

```sh
make deploy IMG=ghcr.io/rdrake/exam-controller:v0.1.0
```

### Prerequisites

- Kubernetes 1.28+
- An ingress controller (ingress-nginx expected by default)
- A wildcard TLS certificate for your exam domain
- An SMTP server for email delivery
- (Optional) Prometheus for monitoring
- (Optional) Cilium CNI for L7 network policy support

## Documentation

| Document | Audience | What it covers |
|---|---|---|
| [User Guide](docs/user-guide.md) | Instructors | Step-by-step: creating exams, checking status, handling common situations, emergency overrides |
| [Operations Runbook](docs/operations.md) | Platform teams | Deployment, monitoring, troubleshooting, emergency procedures, scaling |
| [CRD Reference](docs/crd-reference.md) | Both | Full spec field reference with types, defaults, and immutability rules |
| [CONTRIBUTING.md](CONTRIBUTING.md) | Developers | Setup, workflow, make targets, CI pipeline, releasing |

## Development

```sh
make setup         # one-time post-clone setup (installs git hooks)
make verify-fast   # fast preflight checks
make test          # full integration suite + coverage gate
make test-e2e      # end-to-end tests on Kind
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for details.

## License

Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
