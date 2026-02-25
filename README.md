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
- We value working code over perfect code
- Document what you would improve given more time
- Feel free to add tools or technologies you think are appropriate
- The goal is to simulate real platform engineering work
