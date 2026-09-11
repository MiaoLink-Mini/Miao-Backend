# Historical source snapshots

These three files were recovered byte-for-byte from the previously delivered
`WeAgent-Release-Source.zip` (2026-09-07). They are the original auxiliary inputs
recorded by `../source-baseline.json`, not the similarly named maintained files
under `docs/`.

| File | SHA-256 already recorded in the baseline |
| --- | --- |
| `backend-design.md` | `10eadf35d1b406dbf5eb5b8a30ee0cffb8f05dad0c8c4140d4b0dc8b4b997e51` |
| `frontend-design.md` | `504d22419517687951c1a9138a28a72208d3b645f8e10fa4c0017d7de5be362b` |
| `frontend-function-inventory.md` | `8666eff4de44c011dd093d45781a8a77df9826aab80d9c4e071bd1aad9d8d7a9` |

The repository split removed the common parent directory that the historical
fixture previously read. Keeping the snapshots inside the backend repository
makes that input reproducible without copying files outside a checkout.
`tests/frontend.test.cjs` continues to compare against the same recorded hashes;
no historical digest or test assertion was relaxed.

The local `.gitattributes` disables text conversion for these snapshots. Preserve
the bytes, including line endings. Do not regenerate the hashes when editing
current product documentation. Use `node scripts/check-baseline-sources.cjs` to
check the recovered files; missing or modified sources fail the command.
