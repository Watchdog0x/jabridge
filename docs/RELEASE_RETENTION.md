# Keeping the newest preview

Only the newest published RC stays on the releases page. Stable releases and
Git tags are kept. Old RC download links stop working, so testing instructions
should link to the releases page instead of an old RC download.

The release workflow checks and signs the new archive before cleanup. It then
downloads older RC assets, checks their hashes and uploads a backup as an
Actions artifact. The backup is retained for 90 days. Cleanup does not run if
the backup upload fails or if the release changed during the process.

Release jobs are serialized. An older job cannot delete a newer published RC
or a future draft. Only RC tags matching the expected version format are in scope.
The cleanup uses release IDs and does not delete Git tags or commits.
