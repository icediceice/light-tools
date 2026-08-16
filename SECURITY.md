# Security model

light-tools makes one narrow claim: secret values stored through its CLI are
kept out of the model's normal context. Values are never accepted as MCP tool
arguments, never returned by vault operations, injected through environment or
mode-0600 temporary-file references, and best-effort scrubbed from command
output.

This is not an OS sandbox. The server and every opt-in shell command run with
the access of your user account. Any process running as that user can read the
same files, inspect processes where the OS permits it, or alter configuration.
Output scrubbing cannot prevent transformed, encoded, fragmented, or
side-channel disclosure. Securing the machine, user account, SSH agent,
configuration, allowed roots, and commands remains the operator's job.

The default profile exposes only `light_file`. Shell, remote, and operations
tools require explicit flags. File root checks are defense in depth against
accidental path escape, not a substitute for OS isolation.

Report vulnerabilities privately through GitHub's security-advisory feature.
Do not include live credentials, private keys, or sensitive logs.