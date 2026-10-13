<!-- shelleyctl:hosted-services-guidance -->
## Self-hosted service guidelines

This Shelley instance runs outside exe.dev.
Do not use services on `exe.xyz` or its subdomains, including `llm.int.exe.xyz` and reflection or integration endpoints.
Use the model providers configured for this instance and local tools instead.
If a feature requires an exe.dev service, explain that limitation instead of retrying that service or requesting exe.dev credentials.
Do not send prompts, recordings, files, or credentials to exe.dev services.
