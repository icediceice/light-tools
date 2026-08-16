# Security

light-tools makes one narrow claim: secret values stored through its CLI are
kept out of the model's normal context. Values are never accepted directly as
MCP arguments, never returned by vault operations, and are resolved only by
name into command environments or mode-0600 temporary files. Best-effort
output scrubbing replaces exact secret values before normal results or spills
are shaped.

The vault uses AES-GCM with a random local 32-byte key. Both the key and
ciphertext are stored under the separate secrets state root with mode 0600
where Unix permissions exist. This protects against accidental plaintext
storage and model-context exposure; it does not protect against another
process running as the same user, root, memory inspection, or a compromised
machine.

Temporary `file_refs`, SSH `key_ref`, and `cert_ref` files are created
with mode 0600 and best-effort overwritten and removed after the child process
exits. Filesystem journaling, snapshots, backups, and flash translation layers
mean overwrite is not a forensic-erasure guarantee.

This is not an OS sandbox. The server and every opt-in shell or remote command
run with the access of your user account. Output scrubbing cannot prevent
transformed, encoded, fragmented, or side-channel disclosure. Securing the
machine, account, SSH agent, known-hosts file, allowed roots, configuration,
and commands remains the operator's job.

The default profile exposes only `light_file`. Shell, remote, and operations
tools require explicit flags. File root checks are defense in depth against
accidental path escape, not a substitute for OS isolation. `light_ops` is
read-only but may read explicitly requested local log paths. There is no vault
web UI or value-reading MCP endpoint.

Report vulnerabilities privately through GitHub's security-advisory feature.
Do not include live credentials, private keys, or sensitive logs.
