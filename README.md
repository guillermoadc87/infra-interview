# Platform Engineering Interview Test

A practical, hands-on assessment for platform and infrastructure engineers. This test evaluates real-world skills through a working-but-suboptimal microservices setup running in a sandboxed Kubernetes environment.

## Overview

This interview test presents a working microservices application deployed on a local Kubernetes cluster (k3s in Colima). While the application runs, it has several areas that need improvement. Candidates choose **one path** to focus on, demonstrating their expertise in that domain.

## What You'll Work With

- **Environment**: k3s Kubernetes cluster running in Colima (fully sandboxed)
- **Application**: A simple e-commerce system with microservices and a database backend
- **Infrastructure**: OpenTofu configurations and Helm charts
- **Time**: 3-4 hours (we respect your time - this is not meant to be a week-long project)

## Choose Your Path

Select **ONE** path that best matches your interests and expertise:

### Path 1: Infrastructure as Code
**Focus**: Assess and refactor the infrastructure code following IaC best practices.

### Path 2: Monitoring & Observability
**Focus**: Assess observability gaps and implement a monitoring stack.

### Path 3: Security & Compliance
**Focus**: Audit, identify vulnerabilities, and harden the infrastructure.

### Path 4: CI/CD & GitOps
**Focus**: Design and implement automated deployment pipelines.

---

## Tips for Success

Please read this section — it reflects what we actually value, and it's how the strongest submissions stand apart. A real engineer on our team reviews every one of these by hand.

- **Do one path. Really.** One focused, finished path beats four half-finished ones every single time. "Choose ONE" is not a suggestion or a floor to exceed — it's the exercise. Breadth is not the flex here; a change we could confidently merge is. Depth over surface area.

- **Respect the reviewer's time — that's the whole point.** A human reads this, and reviewer attention is the scarcest thing on our team. Handing us thousands of lines across every domain, with no shared context, isn't generosity — it lands on someone's desk as hours of review load and unowned risk. Scope this like a pull request you'd hand a teammate who then has to own it forever. Would they thank you for it?

- **Finish what you start, and verify it for real.** One thing taken across the finish line beats five things left at 80%. "Verified" means you ran it and watched it work end-to-end — not "the pods started." If you claim something works, assume we'll check. An honest "I ran out of time for X" earns more trust than an overclaim we catch, because one overclaim makes us re-check everything else you said.

- **Don't squash or curate your commits.** We read your history to understand *how* you work, not just what you landed. Small, real, in-order commits — including the dead ends, the reverts, and the "oops, fix" — tell us more than a tidy narrative. Show your actual thinking.

- **Never fabricate or mask.** We would take a sloppy, honest history over a polished, fabricated one every time. Don't invent verification you didn't run, don't dress up unfinished work as done, and don't manufacture a clean story after the fact. Honesty is a hard requirement; we notice, and it's the fastest way to a no.

- **AI and agents are welcome — but own what you submit.** Use the tools you'd use on the job. Two conditions: tell us what you used (a short note in `SOLUTION.md` is plenty), and be ready to explain and defend any line of it. We're hiring you, not your model — if you couldn't walk a reviewer through why the code does what it does, don't submit it. Using an agent to generate breadth you can't personally stand behind is exactly what we're screening against.

**In short:** a single, finished, honestly-documented path that respects our time will always beat a large, impressive-looking, unfinished one. Optimize for a reviewer's confident "yes."

---

## Prerequisites

- [Colima](https://github.com/abiosoft/colima) installed
- [kubectl](https://kubernetes.io/docs/tasks/tools/) installed
- [OpenTofu](https://opentofu.org/docs/intro/install/) >= 1.11 installed
- [Helm](https://helm.sh/docs/intro/install/) installed
- Basic familiarity with Kubernetes and Docker
- 8GB+ RAM available for the VM

## Getting Started

1. **Start the environment**:
   ```bash
   ./scripts/setup.sh
   ```
   This will start Colima with k3s, build and deploy the application stack.

2. **Explore the environment**: Familiarize yourself with what's running and how it's configured.

3. **Choose your path**: Read the instructions in your chosen path directory under `paths/`.

4. **Work on your solution**: Make improvements according to your chosen path.

5. **Document your work**: Update `SOLUTION.md` with what you changed, why, trade-offs, and what you'd do with more time.

## Evaluation Criteria

We're looking for:

1. **Problem identification**: Can you find and articulate what's wrong?
2. **Technical competence**: Does the solution work? Is it production-ready?
3. **Design thinking**: Are trade-offs well-considered?
4. **Pragmatism**: Is the solution appropriately scoped?
5. **Communication**: Are decisions clearly explained?

## Submission

1. Ensure your solution works end-to-end
2. Complete the `SOLUTION.md` document
3. Create a git bundle: `git bundle create solution.bundle --all`
4. Send the bundle file back to us

## Questions?

If anything is unclear or you encounter issues with the environment setup, please reach out. We want you to spend time on the engineering challenge, not fighting tooling.

## Notes

- You're not expected to complete everything perfectly in 3-4 hours
- We value working code over perfect code — and a finished slice over a broad, unfinished one
- Document what you would improve given more time (this is where breadth belongs — as a written plan, not as half-built code)
- Feel free to add tools or technologies you think are appropriate *within your chosen path*
- The goal is to simulate real platform engineering work — including scoping it like real work
