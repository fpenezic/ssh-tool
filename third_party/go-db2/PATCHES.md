# go-db2, vendored with one patch

Source: github.com/go-db2/go-db2 at v0.0.0-20261029213137-75cf9ae2fc03
(MIT, see LICENSE). Non-test Go files only; go.mod's `replace` points
the module here.

## Patch

`network/ddm.go`, `ParseSQLCARD`: the affected-row count was read from
bytes 22:26 (inside SQLERRD1) and a 0 was turned into 1, so every
statement reported one row. SQLCAXGRP starts after the 18 bytes of
SQLCAGRP with its null indicator; SQLERRD3, the row count, is bytes
27:31. Measured against Db2 12.1.5: INSERT ... SELECT of 1200 rows,
UPDATE of 9, DELETE of 0 and of 200 all report their real count.

Drop this directory and the `replace` once upstream carries the fix.
