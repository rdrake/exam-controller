# Instructor Guide: Running Pen-Testing Exams

This guide walks you through creating, monitoring, and managing a pen-testing exam using the exam-controller. Your platform team has already installed the controller on the cluster -- you just need to fill in your exam details and apply them.

## What happens automatically

Once you submit your exam configuration, the controller handles everything on a schedule:

1. **Before the exam** -- Spins up an isolated container for each student, sends them their unique URL by email, and runs a smoke test to verify everything works.
2. **During the exam** -- Opens network access so students can reach their instances. You receive a notification.
3. **After the exam** -- Cuts off student access, keeps instances running so you can review their work, then cleans everything up after a retention window.

You do not need to be online during the exam. The controller runs unattended.

---

## Step 1: Gather your information

Before creating your exam, you need:

| What | Example | Where to get it |
|---|---|---|
| Container image for the vulnerable app | `registry.example.com/vuln-app:v2.1` | Your platform team or course materials |
| Port the app listens on | `8080` | Documented with the image |
| Exam unlock time (ISO 8601) | `2026-04-10T14:00:00-04:00` | Your exam schedule |
| Exam duration | `2h` | Your syllabus |
| Student list (ID + email) | `john.smith` / `john.smith@ontariotechu.net` | Your LMS or registrar |
| Your instructor email | `instructor@ontariotechu.net` | -- |
| Number of spare instances | `2` | Rule of thumb: 1 per 15 students |
| SMTP secret name | `exam-smtp-credentials` | Your platform team |
| Exam domain | `exam.otu.ca` | Your platform team |
| TLS secret name | `exam-wildcard-tls` | Your platform team |

Your platform team provides the last three items. They are shared across all exams on the cluster.

---

## Step 2: Create your exam file

Copy the template below into a file called `my-exam.yaml`. Replace the placeholder values with your information from Step 1.

```yaml
apiVersion: exam.otu.ca/v1alpha1
kind: Exam
metadata:
  name: sofe4790u-midterm        # short name, lowercase, dashes OK
  namespace: exam-system
spec:
  template:
    image: registry.example.com/vuln-app:v2.1
    port: 8080
    resources:
      requests:
        cpu: "250m"
        memory: "256Mi"
      limits:
        cpu: "500m"
        memory: "512Mi"

  schedule:
    unlock: "2026-04-10T14:00:00-04:00"   # when students can access
    duration: "2h"                          # base exam length
    timeMultiplier: 1.5                     # lock at unlock + duration * 1.5
    provisionBefore: "1h"                   # spin up instances 1h early
    retention: "24h"                        # keep instances 24h after lock
    dryRun:
      before: "5m"                          # smoke test 5min before unlock
      duration: "2m"

  email:
    before: "30m"                           # start sending emails 30min before unlock
    rateLimit: 1                            # emails per second
    instructorEmail: instructor@ontariotechu.net
    secretRef: exam-smtp-credentials        # ask your platform team
    from: "noreply@otu.ca"
    subject: "SOFE4790U Midterm - Your Exam Instance"

  students:
    - id: john.smith
      email: john.smith@ontariotechu.net
    - id: jane.doe
      email: jane.doe@ontariotechu.net
    # add one entry per student

  spares: 2
  domain: exam.otu.ca                      # ask your platform team
  ingressTLS:
    secretName: exam-wildcard-tls           # ask your platform team
```

### Key timing details

For an exam that unlocks at **2:00 PM**:

| Time | What happens |
|---|---|
| 1:00 PM | Instances start spinning up (`provisionBefore: 1h`) |
| 1:30 PM | Student emails begin sending (`email.before: 30m`) |
| 1:55 PM | Smoke test runs (`dryRun.before: 5m`) |
| 2:00 PM | Exam unlocks -- students can access their instances |
| 5:00 PM | Exam locks -- `2h * 1.5 = 3h` after unlock |
| 5:00 PM + 24h | Instances torn down (`retention: 24h`) |

The `timeMultiplier` gives students extra time beyond the base duration. At `1.5`, a 2-hour exam runs for 3 hours. Set it to `1.0` for no extra time.

---

## Step 3: Submit your exam

Run this command from the folder where you saved `my-exam.yaml`:

```sh
kubectl apply -f my-exam.yaml
```

You should see:

```
exam.exam.otu.ca/sofe4790u-midterm created
```

That's it. The controller takes over from here. You can close your terminal.

---

## Step 4: Check exam status

At any point, you can check what phase your exam is in:

```sh
kubectl get exam sofe4790u-midterm -n exam-system
```

You will see output like:

```
NAME                 PHASE          AGE
sofe4790u-midterm    Ready          45m
```

For more detail:

```sh
kubectl get exam sofe4790u-midterm -n exam-system -o yaml
```

Look for these fields in the output:

| Field | What it tells you |
|---|---|
| `status.phase` | Current phase (Pending, Provisioning, Ready, Unlocked, Locked, TearingDown) |
| `status.students[].slug` | Each student's unique URL slug |
| `status.students[].url` | Each student's full URL |
| `status.students[].emailStatus` | `Sent`, `Pending`, or `Failed` |
| `status.spares[].url` | Spare instance URLs (also emailed to you) |
| `status.metrics.instancesHealthy` | How many instances are up |
| `status.dryRun.passed` | Smoke test results |
| `status.conditions` | Detailed status of provisioning, emails, dry run, network policies |

### Quick checks

```sh
# What phase is the exam in?
kubectl get exam sofe4790u-midterm -n exam-system -o jsonpath='{.status.phase}'

# Were all emails sent?
kubectl get exam sofe4790u-midterm -n exam-system \
  -o jsonpath='{range .status.students[*]}{.id}: {.emailStatus}{"\n"}{end}'

# How many instances are healthy?
kubectl get exam sofe4790u-midterm -n exam-system \
  -o jsonpath='{.status.metrics.instancesHealthy}'
```

---

## Step 5: Emails you will receive

The controller sends you three emails automatically:

1. **When provisioning completes** -- Lists all spare instance URLs. Bookmark these in case a student needs a replacement.
2. **When the exam unlocks** -- Confirms the exam is live. Includes a count of any failed email deliveries so you can follow up manually.
3. **When the exam locks** -- Confirms the exam has ended. Includes healthy and failed instance counts.

You do not need to be watching the cluster. The emails tell you everything you need to know.

---

## Step 6: Common situations

### A student says they didn't get their email

Look up their URL manually:

```sh
kubectl get exam sofe4790u-midterm -n exam-system \
  -o jsonpath='{range .status.students[*]}{.id}: {.url} ({.emailStatus}){"\n"}{end}'
```

Find their line and send them the URL yourself.

### A student's instance is broken and they need a spare

Your spare instance URLs were emailed to you when provisioning completed. You can also retrieve them:

```sh
kubectl get exam sofe4790u-midterm -n exam-system \
  -o jsonpath='{range .status.spares[*]}{.url}{"\n"}{end}'
```

Give the student one of these URLs. Spares are identical to student instances -- just not assigned to anyone.

### The exam is stuck in Provisioning

This means some instances haven't started yet. Check how many are healthy:

```sh
kubectl get exam sofe4790u-midterm -n exam-system \
  -o jsonpath='healthy: {.status.metrics.instancesHealthy} / total: {.status.metrics.instancesTotal}'
```

If the count isn't increasing, contact your platform team. Common causes are the container image not pulling or insufficient cluster resources.

### You need to cancel the exam

```sh
kubectl delete exam sofe4790u-midterm -n exam-system
```

This immediately begins teardown -- deletes the exam namespace and all student instances. This cannot be undone.

---

## Step 7: Emergency overrides

These are manual interventions. Use them only if something has gone wrong and you cannot wait for the normal schedule.

### Force-unlock an exam early

If instances are ready but the unlock time hasn't arrived yet:

```sh
kubectl patch exam sofe4790u-midterm -n exam-system --type=merge \
  -p '{"status":{"phase":"Unlocked"}}'
```

**Warning:** This skips the smoke test. Verify instances are healthy first:

```sh
kubectl get exam sofe4790u-midterm -n exam-system \
  -o jsonpath='{.status.metrics.instancesHealthy}'
```

### Force-lock an exam early

To end the exam immediately, cutting off all student access:

```sh
kubectl patch exam sofe4790u-midterm -n exam-system --type=merge \
  -p '{"status":{"phase":"Locked"}}'
```

Instances stay running for the retention period. Students just can't reach them.

---

## Quick reference

| Task | Command |
|---|---|
| Submit an exam | `kubectl apply -f my-exam.yaml` |
| Check the phase | `kubectl get exam <name> -n exam-system` |
| Check email status | `kubectl get exam <name> -n exam-system -o jsonpath='{range .status.students[*]}{.id}: {.emailStatus}{"\n"}{end}'` |
| Get student URLs | `kubectl get exam <name> -n exam-system -o jsonpath='{range .status.students[*]}{.id}: {.url}{"\n"}{end}'` |
| Get spare URLs | `kubectl get exam <name> -n exam-system -o jsonpath='{range .status.spares[*]}{.url}{"\n"}{end}'` |
| Check instance health | `kubectl get exam <name> -n exam-system -o jsonpath='{.status.metrics.instancesHealthy}'` |
| Force unlock | `kubectl patch exam <name> -n exam-system --type=merge -p '{"status":{"phase":"Unlocked"}}'` |
| Force lock | `kubectl patch exam <name> -n exam-system --type=merge -p '{"status":{"phase":"Locked"}}'` |
| Cancel / delete exam | `kubectl delete exam <name> -n exam-system` |
