# Path 1: Infrastructure as Code

## Overview

The current infrastructure uses a mix of shell scripts, Helm charts, and OpenTofu to deploy and configure the application. The OpenTofu configuration in `/terraform` manages database provisioning and secrets, while application deployments are handled outside of Terraform via Helm charts in `/charts`. Your task is to assess the current state, identify the issues, and refactor it into production-grade infrastructure code.

## Your Mission

Review the existing infrastructure code and refactor it following IaC best practices. The result should be code you'd be confident merging into a production repository that a team will maintain long-term.

## Requirements

### Must Have

- Identify and document the problems with the current infrastructure code
- Modular, reusable infrastructure code that eliminates duplication
- Support for multiple environments
- Proper secret management
- Meaningful outputs and documentation

### Bonus

We're interested in seeing how you think about infrastructure at scale. Impress us with what you think matters most for production readiness.

## Evaluation

We evaluate module design, DRY principles, configurability across environments, production readiness, code quality, and how you communicate your trade-offs.

## Submission

Document your work in `SOLUTION.md`: what you changed, why, trade-offs you considered, and what you'd do with more time.
