# Project rules
- Go for the harness core. Python only for the load driver.
- No dependencies beyond stdlib + yaml without asking.
- Every fault must have a reversal path that runs on crash.
- Measurement code must not be in the request path.
- Explain the mechanism before writing the code.