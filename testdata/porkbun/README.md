# Porkbun conformance-test data

Used by `make test-conformance` (`main_test.go`, build tag `conformance`). The
cert-manager test harness starts a local kube-apiserver, loads every manifest in this
directory, and passes `config.json` to the solver as its webhook config.

| File | Purpose |
| --- | --- |
| `config.json.sample` | Solver config referencing the Secret below. Copy to `config.json`. |
| `porkbun-credentials.yaml.sample` | Secret with base64-encoded Porkbun keys. Copy to `porkbun-credentials.yaml` and fill it in. |

`config.json` and `porkbun-credentials.yaml` are git-ignored. **Never commit real keys.**
