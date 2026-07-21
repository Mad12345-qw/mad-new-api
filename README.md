# Mad New API

Custom build pipeline for Mad API.

This repository applies a small backend patch to the upstream New API source:

- four-digit numeric email verification codes
- a branded, mobile-friendly verification email template
- fixed-price task models charge exactly once per request
- fixed-price audio models preserve `ModelPrice` during settlement

GitHub Actions builds the Docker image away from the production server and publishes a stable release artifact. No production credentials are stored in this repository.
